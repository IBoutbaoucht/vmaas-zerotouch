// Package lifecycle is the orchestrator that turns "POST /v1/vms" into a
// booted, network-configured VM on ESXi (and turns "DELETE /v1/vms/{id}"
// back into a freed IP and a clean datastore).
//
// Provision runs asynchronously: the HTTP handler returns 202 immediately
// with a VM ID; a goroutine drives the state machine to completion (or
// failure). State transitions are persisted to the bbolt store so the UI
// can poll for status.
//
// On graceful shutdown the orchestrator's Wait() blocks until all in-flight
// goroutines finish.
package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/vim25/mo"
	_ "github.com/vmware/govmomi/vim25/types"

	"github.com/cuneyt/vmaas-engine/internal/clone"
	"github.com/cuneyt/vmaas-engine/internal/cloudinit"
	"github.com/cuneyt/vmaas-engine/internal/config"
	"github.com/cuneyt/vmaas-engine/internal/esxi"
	"github.com/cuneyt/vmaas-engine/internal/ipalloc"
	"github.com/cuneyt/vmaas-engine/internal/store"
)

// CreateRequest is the input to Provision.
type CreateRequest struct {
	Name string // optional; auto-generated when empty
}

// Orchestrator owns the state machine for provisioning and deletion.
type Orchestrator struct {
	ex       *esxi.Client
	alloc    *ipalloc.Allocator
	st       *store.Store
	renderer *cloudinit.Renderer
	cfg      *config.Config

	wg       sync.WaitGroup
	shutdown context.Context
	cancel   context.CancelFunc
}

// New constructs an Orchestrator.
func New(ex *esxi.Client, alloc *ipalloc.Allocator, st *store.Store, r *cloudinit.Renderer, cfg *config.Config) *Orchestrator {
	sCtx, sCancel := context.WithCancel(context.Background())
	return &Orchestrator{
		ex: ex, alloc: alloc, st: st, renderer: r, cfg: cfg,
		shutdown: sCtx, cancel: sCancel,
	}
}

// Wait blocks until all in-flight provisions finish.
func (o *Orchestrator) Wait() { o.wg.Wait() }

// Stop signals running provisions to abort at their next checkpoint.
func (o *Orchestrator) Stop() { o.cancel() }

// Provision creates a new VM record and kicks off the async pipeline.
// Returns the new ID immediately.
func (o *Orchestrator) Provision(req CreateRequest) (string, error) {
	id := "vmaas-" + uuid.NewString()[:8]
	name := req.Name
	if name == "" {
		name = id
	}
	name = sanitizeName(name)

	rec := store.VM{
		ID: id, Name: name, Status: store.StatusPending,
		GoldVM: o.cfg.ESXi.GoldVM,
	}
	if err := o.st.Put(rec); err != nil {
		return "", fmt.Errorf("persist new vm: %w", err)
	}

	o.wg.Add(1)
	go func() {
		defer o.wg.Done()
		o.runProvision(o.shutdown, rec)
	}()
	return id, nil
}

func (o *Orchestrator) runProvision(ctx context.Context, rec store.VM) {
	log := slog.With("vm_id", rec.ID, "vm_name", rec.Name)

	// allocating
	o.setStatus(rec.ID, store.StatusAllocating, "")
	ip, err := o.alloc.Acquire(rec.ID)
	if err != nil {
		o.fail(rec.ID, err, log)
		return
	}
	_ = o.st.SetIP(rec.ID, ip.String())
	log.Info("ip allocated", "ip", ip.String())

	// cloning
	o.setStatus(rec.ID, store.StatusCloning, "")
	cr, err := clone.Clone(ctx, o.ex, o.st, clone.Request{VMID: rec.ID, NewName: rec.Name})
	if err != nil {
		o.fail(rec.ID, fmt.Errorf("clone: %w", err), log)
		_ = o.alloc.Release(ip)
		return
	}
	log.Info("clone complete", "vmx", cr.VMXPath)

	// injecting
	o.setStatus(rec.ID, store.StatusInjecting, "")
	metaB64, userB64, err := o.renderer.Render(cloudinit.Vars{
		InstanceID: rec.ID,
		Hostname:   rec.Name,
		IP:         ip,
	})
	if err != nil {
		o.fail(rec.ID, fmt.Errorf("render: %w", err), log)
		o.bestEffortCleanup(ctx, rec.ID, rec.Name, ip)
		return
	}
	if err := o.ex.SetExtraConfig(ctx, cr.VM, map[string]string{
		"guestinfo.metadata":          metaB64,
		"guestinfo.metadata.encoding": "base64",
		"guestinfo.userdata":          userB64,
		"guestinfo.userdata.encoding": "base64",
	}); err != nil {
		o.fail(rec.ID, fmt.Errorf("inject: %w", err), log)
		o.bestEffortCleanup(ctx, rec.ID, rec.Name, ip)
		return
	}
	log.Info("guestinfo injected")

	// starting
	o.setStatus(rec.ID, store.StatusStarting, "")
	if err := o.ex.PowerOn(ctx, cr.VM); err != nil {
		o.fail(rec.ID, fmt.Errorf("power on: %w", err), log)
		o.bestEffortCleanup(ctx, rec.ID, rec.Name, ip)
		return
	}
	log.Info("vm powered on")

	// wait for ready
	if err := o.waitReady(ctx, cr.VM, ip); err != nil {
		o.fail(rec.ID, fmt.Errorf("wait ready: %w", err), log)
		// leave VM intact for inspection; just release IP-pool slot? No — keep it so
		// the user can decide. They'll see status=failed in the UI with the error.
		return
	}

	o.setStatus(rec.ID, store.StatusReady, "")
	log.Info("vm ready", "ip", ip.String())
}

// waitReady polls until Tools is running AND the guest reports the expected IP.
// Up to 5 minutes by default.
func (o *Orchestrator) waitReady(ctx context.Context, vm *object.VirtualMachine, expected netip.Addr) error {
	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		var p mo.VirtualMachine
		if err := vm.Properties(ctx, vm.Reference(),
			[]string{"guest.toolsRunningStatus", "guest.guestState", "guest.ipAddress", "guest.net"},
			&p); err != nil {
			return err
		}
		if p.Guest != nil {
			toolsRunning := p.Guest.ToolsRunningStatus == "guestToolsRunning"
			ipReported := guestHasIPv2(p.Guest, expected.String())
			if toolsRunning && ipReported {
				return nil
			}
		}
		time.Sleep(3 * time.Second)
	}
	return errors.New("timeout waiting for VMware Tools and configured IP")
}

// Delete tears down a VM: power off → unregister → delete files → release IP.
func (o *Orchestrator) Delete(ctx context.Context, id string) error {
	v, err := o.st.Get(id)
	if err != nil {
		return err
	}
	o.setStatus(id, store.StatusDeleting, "")

	vm, err := o.ex.FindVM(ctx, v.Name)
	if err != nil && !errors.Is(err, esxi.ErrVMNotFound) {
		return fmt.Errorf("find vm: %w", err)
	}
	if vm != nil {
		if err := o.ex.PowerOff(ctx, vm); err != nil {
			slog.Warn("power off (continuing)", "err", err)
		}
		if err := o.ex.Unregister(ctx, vm); err != nil {
			slog.Warn("unregister (continuing)", "err", err)
		}
	}
	if err := o.ex.DeleteDir(ctx, v.Name); err != nil {
		slog.Warn("delete dir (continuing)", "err", err)
	}
	if v.IP != "" {
		if ip, err := netip.ParseAddr(v.IP); err == nil {
			_ = o.alloc.Release(ip)
		}
	}
	_ = o.st.CheckpointClear(id)
	return o.st.Delete(id)
}

// Get returns one VM record.
func (o *Orchestrator) Get(id string) (store.VM, error) { return o.st.Get(id) }

// List returns all VM records.
func (o *Orchestrator) List() ([]store.VM, error) { return o.st.List() }

func (o *Orchestrator) setStatus(id string, s store.Status, errStr string) {
	if err := o.st.UpdateStatus(id, s, errStr); err != nil {
		slog.Error("set status", "id", id, "status", s, "err", err)
	}
}

func (o *Orchestrator) fail(id string, err error, log *slog.Logger) {
	log.Error("provision failed", "err", err)
	_ = o.st.UpdateStatus(id, store.StatusFailed, err.Error())
}

// bestEffortCleanup releases IP, unregisters, deletes files. Used on mid-pipeline error
// AFTER we've already cloned (but before/during/after inject/poweron).
func (o *Orchestrator) bestEffortCleanup(ctx context.Context, id, name string, ip netip.Addr) {
	if vm, err := o.ex.FindVM(ctx, name); err == nil && vm != nil {
		_ = o.ex.PowerOff(ctx, vm)
		_ = o.ex.Unregister(ctx, vm)
	}
	_ = o.ex.DeleteDir(ctx, name)
	_ = o.alloc.Release(ip)
	_ = o.st.CheckpointClear(id)
}

// sanitizeName produces a safe inventory/folder name: lowercase, alnum + dashes.
func sanitizeName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		case r == '_', r == ' ':
			b.WriteByte('-')
		}
	}
	out := b.String()
	if out == "" {
		out = "vmaas-vm"
	}
	if len(out) > 63 {
		out = out[:63]
	}
	return out
}
