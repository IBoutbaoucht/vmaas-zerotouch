// Package clone implements the 5-step file-level VM clone procedure that
// stands in for CloneVM_Task on standalone ESXi (CloneVM_Task only works
// when the host is managed by vCenter).
//
// The procedure (all paths relative to the datastore):
//
//  1. MakeDirectory               → [datastore1] <new>/
//  2. CopyVirtualDisk (thin)      → [datastore1] <new>/<new>.vmdk
//  3. CopyDatastoreFile (.vmx)    → [datastore1] <new>/<new>.vmx
//  4. DownloadText + Patch text   → fix displayName, nvram, scsi0:0.fileName
//     + UploadText                → re-upload patched .vmx
//  5. RegisterVM                  → make hostd aware of the new .vmx
//
// Each step writes a checkpoint to the store; rerunning Clone with the same
// destination name resumes from the last-incomplete step. Checkpoints are
// cleared on a successful end-to-end run.
package clone

import (
	"context"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"

	"github.com/cuneyt/vmaas-engine/internal/esxi"
	"github.com/cuneyt/vmaas-engine/internal/store"
)

// Request is the input to Clone.
type Request struct {
	VMID    string // unique opaque id used for the store checkpoint
	NewName string // inventory display name (also datastore folder/file basename)
}

// Result is what Clone returns.
type Result struct {
	VM        *object.VirtualMachine
	VMXPath   string // relative datastore path of the new .vmx
	FolderRel string // relative datastore folder
}

// Steps (and their names) — kept as constants so checkpoint keys are stable.
const (
	StepMkDir    = "mkdir"
	StepCopyDisk = "copydisk"
	StepCopyVMX  = "copyvmx"
	StepPatchVMX = "patchvmx"
	StepRegister = "register"
)

// Clone runs the 5-step procedure idempotently against a store.
func Clone(ctx context.Context, ex *esxi.Client, st *store.Store, req Request) (*Result, error) {
	if req.VMID == "" || req.NewName == "" {
		return nil, errors.New("clone: VMID and NewName required")
	}

	// 0. Resolve gold-image source files (.vmx path + .vmdk path) from properties.
	src, err := resolveGoldPaths(ctx, ex.Gold)
	if err != nil {
		return nil, fmt.Errorf("resolve gold: %w", err)
	}

	newDir := req.NewName
	newVMX := path.Join(newDir, req.NewName+".vmx")
	newVMDK := path.Join(newDir, req.NewName+".vmdk")

	// 1. MakeDirectory
	if err := once(st, req.VMID, StepMkDir, func() error {
		return ex.MakeDirectory(ctx, newDir)
	}); err != nil {
		return nil, fmt.Errorf("mkdir: %w", err)
	}

	// 2. CopyVirtualDisk (thin clone)
	if err := once(st, req.VMID, StepCopyDisk, func() error {
		return ex.CopyVirtualDisk(ctx, src.vmdkRel, newVMDK)
	}); err != nil {
		return nil, fmt.Errorf("copy disk: %w", err)
	}

	// 3. CopyDatastoreFile (.vmx)
	if err := once(st, req.VMID, StepCopyVMX, func() error {
		return ex.CopyDatastoreFile(ctx, src.vmxRel, newVMX)
	}); err != nil {
		return nil, fmt.Errorf("copy vmx: %w", err)
	}

	// 4. Download → Patch → Upload
	if err := once(st, req.VMID, StepPatchVMX, func() error {
		body, err := ex.DownloadText(ctx, newVMX)
		if err != nil {
			return err
		}
		patched, err := Patch(body, req.NewName)
		if err != nil {
			return err
		}
		return ex.UploadText(ctx, newVMX, patched)
	}); err != nil {
		return nil, fmt.Errorf("patch vmx: %w", err)
	}

	// 5. RegisterVM
	var newVM *object.VirtualMachine
	if err := once(st, req.VMID, StepRegister, func() error {
		v, err := ex.RegisterVM(ctx, newVMX, req.NewName)
		if err != nil {
			return err
		}
		newVM = v
		return nil
	}); err != nil {
		return nil, fmt.Errorf("register: %w", err)
	}

	// Resume case: if Register was already checkpointed, look up by name.
	if newVM == nil {
		v, err := ex.FindVM(ctx, req.NewName)
		if err != nil {
			return nil, fmt.Errorf("post-register find: %w", err)
		}
		newVM = v
	}

	_ = st.CheckpointClear(req.VMID)
	return &Result{VM: newVM, VMXPath: newVMX, FolderRel: newDir}, nil
}

// once runs step iff its checkpoint isn't already set; on success it sets it.
func once(st *store.Store, vmID, step string, fn func() error) error {
	done, err := st.CheckpointHas(vmID, step)
	if err != nil {
		return err
	}
	if done {
		return nil
	}
	if err := fn(); err != nil {
		return err
	}
	return st.CheckpointSet(vmID, step)
}

type goldPaths struct {
	vmxRel  string // e.g. "ubuntu-22.04-template/ubuntu-22.04-template.vmx"
	vmdkRel string // e.g. "ubuntu-22.04-template/ubuntu-22.04-template.vmdk"
}

// resolveGoldPaths queries the gold VM's config for its .vmx and primary disk.
func resolveGoldPaths(ctx context.Context, vm *object.VirtualMachine) (*goldPaths, error) {
	var props mo.VirtualMachine
	err := vm.Properties(ctx, vm.Reference(),
		[]string{"config.files.vmPathName", "config.hardware.device"}, &props)
	if err != nil {
		return nil, err
	}
	if props.Config == nil {
		return nil, errors.New("gold vm has no Config")
	}

	vmxRel, err := stripDS(props.Config.Files.VmPathName)
	if err != nil {
		return nil, fmt.Errorf("vmx path: %w", err)
	}

	var vmdkRel string
	for _, d := range props.Config.Hardware.Device {
		if disk, ok := d.(*types.VirtualDisk); ok {
			if back, ok := disk.Backing.(*types.VirtualDiskFlatVer2BackingInfo); ok {
				vmdkRel, err = stripDS(back.FileName)
				if err == nil {
					break
				}
			}
		}
	}
	if vmdkRel == "" {
		return nil, errors.New("gold vm has no flat-v2 disk")
	}

	return &goldPaths{vmxRel: vmxRel, vmdkRel: vmdkRel}, nil
}

// stripDS converts "[datastore1] dir/file.ext" → "dir/file.ext".
func stripDS(p string) (string, error) {
	i := strings.Index(p, "]")
	if i < 0 || !strings.HasPrefix(p, "[") {
		return "", fmt.Errorf("not a datastore path: %q", p)
	}
	return strings.TrimSpace(p[i+1:]), nil
}

// Cleanup removes the datastore folder for a failed/deleted VM.
// Best-effort: errors are returned but the caller usually logs and continues.
func Cleanup(ctx context.Context, ex *esxi.Client, st *store.Store, vmID, name string) error {
	_ = st.CheckpointClear(vmID)
	if name == "" {
		return nil
	}
	dir := filepath.ToSlash(name)
	return ex.DeleteDir(ctx, dir)
}
