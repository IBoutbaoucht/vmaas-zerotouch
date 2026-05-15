// Datastore helpers — wrap the raw govmomi calls our clone procedure needs.
//
// Naming convention: a "datastore path" is the VMware-style "[datastore1] dir/file.ext"
// string. A "relative path" is "dir/file.ext" (no brackets, no datastore name).
// All helpers take relative paths and bracket-quote them internally.
package esxi

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/vim25/soap"
	"github.com/vmware/govmomi/vim25/types"
)

// DSPath builds the datastore path "[datastore1] rel" from a relative path.
func (c *Client) DSPath(rel string) string {
	return fmt.Sprintf("[%s] %s", c.cfg.Datastore, strings.TrimPrefix(rel, "/"))
}

// MakeDirectory creates a directory on the datastore. Parents are created
// as needed; an "already exists" error is squashed so the call is idempotent.
func (c *Client) MakeDirectory(ctx context.Context, rel string) error {
	fm := object.NewFileManager(c.Govmomi.Client)
	err := fm.MakeDirectory(ctx, c.DSPath(rel), c.Datacenter, true)
	if err != nil && strings.Contains(err.Error(), "already exists") {
		return nil
	}
	return err
}

// CopyDatastoreFile copies one file inside the datastore (the VMX, the NVRAM, etc.).
// Returns once the underlying _Task completes.
func (c *Client) CopyDatastoreFile(ctx context.Context, srcRel, dstRel string) error {
	fm := object.NewFileManager(c.Govmomi.Client)
	task, err := fm.CopyDatastoreFile(ctx, c.DSPath(srcRel), c.Datacenter, c.DSPath(dstRel), c.Datacenter, true)
	if err != nil {
		return err
	}
	return task.Wait(ctx)
}

// CopyVirtualDisk clones a .vmdk on the datastore using the disk-manager API
// (which understands VMDK descriptor + extent files together).
func (c *Client) CopyVirtualDisk(ctx context.Context, srcRel, dstRel string) error {
	vdm := object.NewVirtualDiskManager(c.Govmomi.Client)
	spec := &types.VirtualDiskSpec{
		AdapterType: "lsiLogic",
		DiskType:    "thin",
	}
	task, err := vdm.CopyVirtualDisk(ctx, c.DSPath(srcRel), c.Datacenter, c.DSPath(dstRel), c.Datacenter, spec, true)
	if err != nil {
		return err
	}
	return task.Wait(ctx)
}

// DeleteDir removes a datastore directory (or file) recursively.
func (c *Client) DeleteDir(ctx context.Context, rel string) error {
	fm := object.NewFileManager(c.Govmomi.Client)
	task, err := fm.DeleteDatastoreFile(ctx, c.DSPath(rel), c.Datacenter)
	if err != nil {
		return err
	}
	return task.Wait(ctx)
}

// DownloadText fetches a small text file from the datastore (e.g. a .vmx)
// and returns its content as a string.
func (c *Client) DownloadText(ctx context.Context, rel string) (string, error) {
	rc, _, err := c.Datastore.Download(ctx, rel, &soap.DefaultDownload)
	if err != nil {
		return "", fmt.Errorf("download %s: %w", rel, err)
	}
	defer rc.Close()
	body, err := io.ReadAll(rc)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// UploadText writes a small text file to the datastore (the patched .vmx).
func (c *Client) UploadText(ctx context.Context, rel, content string) error {
	p := soap.DefaultUpload
	p.ContentLength = int64(len(content))
	if err := c.Datastore.Upload(ctx, bytes.NewReader([]byte(content)), rel, &p); err != nil {
		return fmt.Errorf("upload %s: %w", rel, err)
	}
	return nil
}
