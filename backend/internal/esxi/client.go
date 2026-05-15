// Package esxi wraps the govmomi SOAP client with VMaaS-specific helpers.
//
// One Client is created at startup and shared across the process. It caches
// references to the datastore, folder, host, and gold-image VM so handlers
// don't re-walk the inventory on every request.
package esxi

import (
	"context"
	"fmt"
	"net/url"

	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/vim25/soap"
	vim25types "github.com/vmware/govmomi/vim25/types"

	"github.com/cuneyt/vmaas-engine/internal/config"
)

// Client is a long-lived ESXi connection plus the inventory references
// we need repeatedly (datastore, folder, host, gold image).
type Client struct {
	Govmomi   *govmomi.Client
	Finder    *find.Finder
	Datacenter *object.Datacenter
	Datastore *object.Datastore
	Folder    *object.Folder
	Host      *object.HostSystem
	Pool      *object.ResourcePool   // standalone ESXi's default "Resources" pool — RegisterVM requires it
	Gold      *object.VirtualMachine
	cfg       *config.ESXiConfig
}

// New logs into ESXi, resolves the inventory references, and returns a Client.
func New(ctx context.Context, c *config.ESXiConfig) (*Client, error) {
	u, err := soap.ParseURL(c.URL)
	if err != nil {
		return nil, fmt.Errorf("parse esxi.url: %w", err)
	}
	u.User = url.UserPassword(c.User, c.Password)

	gv, err := govmomi.NewClient(ctx, u, c.Insecure)
	if err != nil {
		return nil, fmt.Errorf("esxi login: %w", err)
	}

	finder := find.NewFinder(gv.Client, true)
	dc, err := finder.DefaultDatacenter(ctx)
	if err != nil {
		return nil, fmt.Errorf("default datacenter: %w", err)
	}
	finder.SetDatacenter(dc)

	ds, err := finder.Datastore(ctx, c.Datastore)
	if err != nil {
		return nil, fmt.Errorf("datastore %q: %w", c.Datastore, err)
	}

	folder, err := finder.Folder(ctx, c.Folder)
	if err != nil {
		return nil, fmt.Errorf("folder %q: %w", c.Folder, err)
	}

	host, err := finder.DefaultHostSystem(ctx)
	if err != nil {
		return nil, fmt.Errorf("default host: %w", err)
	}

	// On standalone ESXi, RegisterVM_Task rejects a nil resource pool. Every
	// host has a built-in pool named "Resources" under the ComputeResource.
	pool, err := finder.DefaultResourcePool(ctx)
	if err != nil {
		return nil, fmt.Errorf("default resource pool: %w", err)
	}

	gold, err := finder.VirtualMachine(ctx, c.GoldVM)
	if err != nil {
		return nil, fmt.Errorf("gold vm %q: %w", c.GoldVM, err)
	}

	return &Client{
		Govmomi:    gv,
		Finder:     finder,
		Datacenter: dc,
		Datastore:  ds,
		Folder:     folder,
		Host:       host,
		Pool:       pool,
		Gold:       gold,
		cfg:        c,
	}, nil
}

// Close logs out the SOAP session. Safe to call multiple times.
func (c *Client) Close(ctx context.Context) {
	if c == nil || c.Govmomi == nil {
		return
	}
	_ = c.Govmomi.Logout(ctx)
}

// About returns the AboutInfo from the ESXi ServiceContent — the same data
// `govc about` prints. Useful as a smoke test ("am I really talking to B?").
func (c *Client) About(ctx context.Context) vim25types.AboutInfo {
	return c.Govmomi.Client.ServiceContent.About
}
