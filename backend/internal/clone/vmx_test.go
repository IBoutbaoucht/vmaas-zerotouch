package clone

import (
	"strings"
	"testing"
)

func TestPatchRewritesKeys(t *testing.T) {
	in := `displayName = "ubuntu-22.04-template"
nvram = "ubuntu-22.04-template.nvram"
scsi0:0.fileName = "ubuntu-22.04-template.vmdk"
uuid.bios = "56 4d ab cd..."
ethernet0.generatedAddress = "00:50:56:97:aa:bb"
guestOS = "ubuntu-64"
`
	out, err := Patch(in, "vmaas-abc")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`displayName = "vmaas-abc"`,
		`nvram = "vmaas-abc.nvram"`,
		`scsi0:0.fileName = "vmaas-abc.vmdk"`,
		`guestOS = "ubuntu-64"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q\n---\n%s", want, out)
		}
	}
	for _, badSub := range []string{
		"uuid.bios",
		"ethernet0.generatedAddress",
		"ubuntu-22.04-template",
	} {
		if strings.Contains(out, badSub) {
			t.Errorf("did not strip %q\n---\n%s", badSub, out)
		}
	}
}

func TestPatchAddsMissingKeys(t *testing.T) {
	in := `guestOS = "ubuntu-64"
`
	out, err := Patch(in, "vmaas-xyz")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`displayName = "vmaas-xyz"`,
		`nvram = "vmaas-xyz.nvram"`,
		`scsi0:0.fileName = "vmaas-xyz.vmdk"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q\n---\n%s", want, out)
		}
	}
}
