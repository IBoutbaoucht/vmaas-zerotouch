// Package cloudinit renders per-VM metadata.yaml and userdata.yaml from
// templates and returns base64-encoded strings suitable for direct injection
// into a VM's extraConfig (guestinfo.metadata, guestinfo.userdata).
//
// Why base64: VMware's guestinfo facility passes config to the guest as VMX
// option values, and binary-safe transport requires encoding. cloud-init's
// VMware datasource looks at guestinfo.metadata.encoding to decide whether
// to base64-decode before parsing — we set that to "base64" explicitly.
package cloudinit

import (
	"bytes"
	_ "embed"
	"encoding/base64"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"strings"
	"text/template"

	"github.com/cuneyt/vmaas-engine/internal/config"
)

//go:embed templates_embed/metadata.yaml.tmpl
var metaTmplSrc string

//go:embed templates_embed/userdata.yaml.tmpl
var userTmplSrc string

// Vars is the input to Render.
type Vars struct {
	InstanceID string
	Hostname   string
	NIC        string
	User       string
	IP         netip.Addr
	Prefix     int
	Gateway    netip.Addr
	DNS        []netip.Addr
	SSHKeys    []string
}

// Renderer holds parsed templates + the static defaults from config.
type Renderer struct {
	meta, user *template.Template
	ci         *config.CloudInitConfig
	net        *config.NetworkConfig
	sshKeys    []string
}

// New parses the embedded templates and loads SSH authorized_keys from disk.
func New(ci *config.CloudInitConfig, nc *config.NetworkConfig) (*Renderer, error) {
	mt, err := template.New("meta").Parse(metaTmplSrc)
	if err != nil {
		return nil, fmt.Errorf("parse meta tmpl: %w", err)
	}
	ut, err := template.New("user").Parse(userTmplSrc)
	if err != nil {
		return nil, fmt.Errorf("parse user tmpl: %w", err)
	}
	keys, err := loadKeysFile(ci.SSHKeysFile)
	if err != nil {
		return nil, err
	}
	return &Renderer{meta: mt, user: ut, ci: ci, net: nc, sshKeys: keys}, nil
}

// Render returns metadata.yaml and userdata.yaml base64-encoded.
func (r *Renderer) Render(v Vars) (metaB64, userB64 string, err error) {
	// Fill defaults from config when caller didn't override.
	if v.NIC == "" {
		v.NIC = r.net.NICName
	}
	if v.User == "" {
		v.User = r.ci.DefaultUser
	}
	if v.Prefix == 0 {
		v.Prefix = r.net.Prefix
	}
	if !v.Gateway.IsValid() {
		v.Gateway, _ = netip.ParseAddr(r.net.Gateway)
	}
	if len(v.DNS) == 0 {
		for _, d := range r.net.DNS {
			if a, err := netip.ParseAddr(d); err == nil {
				v.DNS = append(v.DNS, a)
			}
		}
	}
	if len(v.SSHKeys) == 0 {
		v.SSHKeys = r.sshKeys
	}
	if v.InstanceID == "" || v.Hostname == "" || !v.IP.IsValid() {
		return "", "", errors.New("InstanceID, Hostname, IP are required")
	}

	var mbuf, ubuf bytes.Buffer
	if err := r.meta.Execute(&mbuf, v); err != nil {
		return "", "", fmt.Errorf("render meta: %w", err)
	}
	if err := r.user.Execute(&ubuf, v); err != nil {
		return "", "", fmt.Errorf("render user: %w", err)
	}
	return base64.StdEncoding.EncodeToString(mbuf.Bytes()),
		base64.StdEncoding.EncodeToString(ubuf.Bytes()),
		nil
}

// RenderPlain returns the un-encoded yaml — useful for tests and debugging.
func (r *Renderer) RenderPlain(v Vars) (meta, user string, err error) {
	m64, u64, err := r.Render(v)
	if err != nil {
		return "", "", err
	}
	mb, _ := base64.StdEncoding.DecodeString(m64)
	ub, _ := base64.StdEncoding.DecodeString(u64)
	return string(mb), string(ub), nil
}

func loadKeysFile(path string) ([]string, error) {
	if path == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// Allow startup without keys; UI will warn.
			return nil, nil
		}
		return nil, fmt.Errorf("read ssh keys: %w", err)
	}
	var keys []string
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		keys = append(keys, line)
	}
	return keys, nil
}
