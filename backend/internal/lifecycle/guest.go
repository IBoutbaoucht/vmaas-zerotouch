package lifecycle

import (
	"github.com/vmware/govmomi/vim25/types"
)

// (Override the placeholder in orchestrator.go with the real impl.)
//
// We accept *types.GuestInfo (which is what mo.VirtualMachine.Guest is) and
// check ipAddress + every NIC's IpAddress for the wanted IP.

// Use a fresh function name so it overrides at link time? Go doesn't allow
// that. Instead we just import this file's helper from waitReady.
// For now, expose check via guestHasIPv2 (replacing the placeholder).
func guestHasIPv2(g *types.GuestInfo, want string) bool {
	if g == nil {
		return false
	}
	if g.IpAddress == want {
		return true
	}
	for _, nic := range g.Net {
		for _, a := range nic.IpAddress {
			if a == want {
				return true
			}
		}
	}
	return false
}
