package cloudinit

import (
	"net/netip"
	"strings"
	"testing"

	"github.com/cuneyt/vmaas-engine/internal/config"
)

func TestRenderPlainContainsExpectedFields(t *testing.T) {
	r, err := New(
		&config.CloudInitConfig{DefaultUser: "cuneyt", SSHKeysFile: ""},
		&config.NetworkConfig{
			Prefix: 24, Gateway: "192.0.2.254",
			DNS: []string{"1.1.1.1", "8.8.8.8"}, NICName: "ens34",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	ip, _ := netip.ParseAddr("192.0.2.62")
	meta, user, err := r.RenderPlain(Vars{
		InstanceID: "vmaas-abc123",
		Hostname:   "vm1",
		IP:         ip,
		SSHKeys:    []string{"ssh-ed25519 AAAA test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"instance-id: vmaas-abc123",
		"local-hostname: vm1",
		"192.0.2.62/24",
		"gateway4: 192.0.2.254",
		"- 1.1.1.1",
		"- 8.8.8.8",
		"ens34:",
	} {
		if !strings.Contains(meta, want) {
			t.Errorf("meta missing %q\n---\n%s", want, meta)
		}
	}
	for _, want := range []string{
		"#cloud-config",
		"hostname: vm1",
		"name: cuneyt",
		"ssh-ed25519 AAAA test",
		"vmaas-sentinel",
	} {
		if !strings.Contains(user, want) {
			t.Errorf("user missing %q\n---\n%s", want, user)
		}
	}
}
