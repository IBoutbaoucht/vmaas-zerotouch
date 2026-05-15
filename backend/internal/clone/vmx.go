// VMX file patching. The .vmx is just a text file of `key = "value"` lines;
// we rewrite the three keys that reference the VM by name so the clone
// doesn't accidentally point its disk/nvram at the gold image's files.
package clone

import (
	"fmt"
	"regexp"
	"strings"
)

// Patch rewrites displayName, nvram, and scsi0:0.fileName in raw VMX text
// to refer to the new VM's name. Returns the new VMX text.
func Patch(vmx, newName string) (string, error) {
	out := vmx
	patches := []struct {
		key   string
		value string
		re    *regexp.Regexp
	}{
		{
			key:   "displayName",
			value: newName,
			re:    regexp.MustCompile(`(?m)^displayName\s*=\s*".*"\s*$`),
		},
		{
			key:   "nvram",
			value: newName + ".nvram",
			re:    regexp.MustCompile(`(?m)^nvram\s*=\s*".*"\s*$`),
		},
		{
			key:   "scsi0:0.fileName",
			value: newName + ".vmdk",
			re:    regexp.MustCompile(`(?m)^scsi0:0\.fileName\s*=\s*".*"\s*$`),
		},
	}
	for _, p := range patches {
		replacement := fmt.Sprintf(`%s = "%s"`, p.key, p.value)
		if p.re.MatchString(out) {
			out = p.re.ReplaceAllString(out, replacement)
		} else {
			// Key absent from source VMX — append it so the clone still works.
			out = strings.TrimRight(out, "\n") + "\n" + replacement + "\n"
		}
	}
	// Also strip uuid/ethernet0.generatedAddress so the clone gets fresh ones.
	stripRE := regexp.MustCompile(`(?m)^(uuid\.bios|uuid\.location|ethernet0\.generatedAddress|ethernet0\.generatedAddressOffset)\s*=\s*".*"\s*\n`)
	out = stripRE.ReplaceAllString(out, "")
	return out, nil
}
