// VM helpers — register/power/destroy + extraConfig injection for guestinfo.
package esxi

import (
	"context"
	"errors"
	"fmt"

	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"
)

// RegisterVM registers an existing .vmx as a VM. The "name" becomes the
// inventory display name. We pass the host's default resource pool —
// standalone ESXi rejects a nil pool with "A specified parameter was not
// correct: pool".
func (c *Client) RegisterVM(ctx context.Context, vmxRel, name string) (*object.VirtualMachine, error) {
	task, err := c.Folder.RegisterVM(ctx, c.DSPath(vmxRel), name, false, c.Pool, c.Host)
	if err != nil {
		return nil, fmt.Errorf("register vm: %w", err)
	}
	info, err := task.WaitForResult(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("register vm wait: %w", err)
	}
	ref, ok := info.Result.(types.ManagedObjectReference)
	if !ok {
		return nil, fmt.Errorf("register vm: unexpected result %T", info.Result)
	}
	return object.NewVirtualMachine(c.Govmomi.Client, ref), nil
}

// FindVM returns the VM with the given inventory name, or ErrVMNotFound.
func (c *Client) FindVM(ctx context.Context, name string) (*object.VirtualMachine, error) {
	vm, err := c.Finder.VirtualMachine(ctx, name)
	if err != nil {
		var nfe *find.NotFoundError
		if errors.As(err, &nfe) {
			return nil, ErrVMNotFound
		}
		return nil, err
	}
	return vm, nil
}

// ErrVMNotFound is returned by FindVM when no VM matches the name.
var ErrVMNotFound = errors.New("vm not found")

// SetExtraConfig writes guestinfo (or any other extraConfig) keys onto a VM.
// Idempotent: re-applying the same keys is a no-op on the hypervisor side.
func (c *Client) SetExtraConfig(ctx context.Context, vm *object.VirtualMachine, kv map[string]string) error {
	var opts []types.BaseOptionValue
	for k, v := range kv {
		opts = append(opts, &types.OptionValue{Key: k, Value: v})
	}
	spec := types.VirtualMachineConfigSpec{ExtraConfig: opts}
	task, err := vm.Reconfigure(ctx, spec)
	if err != nil {
		return fmt.Errorf("reconfigure vm: %w", err)
	}
	return task.Wait(ctx)
}

// PowerOn starts the VM and waits for the task to complete.
func (c *Client) PowerOn(ctx context.Context, vm *object.VirtualMachine) error {
	task, err := vm.PowerOn(ctx)
	if err != nil {
		return err
	}
	return task.Wait(ctx)
}

// PowerOff stops the VM (hard power-off). Returns nil if already off.
func (c *Client) PowerOff(ctx context.Context, vm *object.VirtualMachine) error {
	state, err := vm.PowerState(ctx)
	if err != nil {
		return err
	}
	if state == types.VirtualMachinePowerStatePoweredOff {
		return nil
	}
	task, err := vm.PowerOff(ctx)
	if err != nil {
		return err
	}
	return task.Wait(ctx)
}

// Unregister removes the VM from inventory WITHOUT deleting the underlying files.
// Our caller then DeleteDir's the datastore folder for a full cleanup.
func (c *Client) Unregister(ctx context.Context, vm *object.VirtualMachine) error {
	return vm.Unregister(ctx)
}

// VMProps fetches a small set of common properties in a single call.
func (c *Client) VMProps(ctx context.Context, vm *object.VirtualMachine, paths ...string) (mo.VirtualMachine, error) {
	var out mo.VirtualMachine
	err := vm.Properties(ctx, vm.Reference(), paths, &out)
	return out, err
}
