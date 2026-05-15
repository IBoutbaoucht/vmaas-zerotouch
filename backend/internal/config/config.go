// Package config loads and validates the VMaaS runtime configuration.
//
// The configuration is a single YAML file (see configs/vmaas.yaml). Values
// using the `${VAR}` syntax are substituted from the process environment
// when Load is called. Missing required values cause Load to return an
// error — we fail fast at startup rather than later, mid-request.
package config

import (
	"errors"
	"fmt"
	"net/netip"
	"os"
	"regexp"

	"gopkg.in/yaml.v3"
)

// Config is the full runtime configuration.
type Config struct {
	Listen    string          `yaml:"listen"`
	AuthToken string          `yaml:"auth_token"`
	ESXi      ESXiConfig      `yaml:"esxi"`
	Network   NetworkConfig   `yaml:"network"`
	CloudInit CloudInitConfig `yaml:"cloudinit"`
	Store     StoreConfig     `yaml:"store"`
}

type ESXiConfig struct {
	URL                      string `yaml:"url"`
	User                     string `yaml:"user"`
	Password                 string `yaml:"password"`
	Insecure                 bool   `yaml:"insecure"`
	Datastore                string `yaml:"datastore"`
	Folder                   string `yaml:"folder"`
	GoldVM                   string `yaml:"gold_vm"`
	SessionKeepaliveSeconds  int    `yaml:"session_keepalive_seconds"`
}

type NetworkConfig struct {
	PoolStart string   `yaml:"pool_start"`
	PoolEnd   string   `yaml:"pool_end"`
	Prefix    int      `yaml:"prefix"`
	Gateway   string   `yaml:"gateway"`
	DNS       []string `yaml:"dns"`
	NICName   string   `yaml:"nic_name"`
}

type CloudInitConfig struct {
	DefaultUser  string `yaml:"default_user"`
	SSHKeysFile  string `yaml:"ssh_keys_file"`
}

type StoreConfig struct {
	Path string `yaml:"path"`
}

// envVarRE matches ${VAR_NAME} placeholders.
var envVarRE = regexp.MustCompile(`\$\{([A-Z0-9_]+)\}`)

// Load reads and parses the YAML config at path, expanding ${ENV} references.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %q: %w", path, err)
	}
	expanded := envVarRE.ReplaceAllStringFunc(string(raw), func(match string) string {
		name := match[2 : len(match)-1]
		return os.Getenv(name)
	})

	var cfg Config
	if err := yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Validate sanity-checks fields that must be present and well-formed.
func (c *Config) Validate() error {
	if c.Listen == "" {
		return errors.New("listen is required")
	}
	if c.AuthToken == "" {
		return errors.New("auth_token is empty (set VMAAS_TOKEN env)")
	}
	if c.ESXi.URL == "" || c.ESXi.User == "" || c.ESXi.Password == "" {
		return errors.New("esxi.url/user/password are required (set ESXI_PASSWORD env)")
	}
	if c.ESXi.Datastore == "" || c.ESXi.GoldVM == "" || c.ESXi.Folder == "" {
		return errors.New("esxi.datastore/folder/gold_vm are required")
	}
	if _, err := netip.ParseAddr(c.Network.PoolStart); err != nil {
		return fmt.Errorf("network.pool_start: %w", err)
	}
	if _, err := netip.ParseAddr(c.Network.PoolEnd); err != nil {
		return fmt.Errorf("network.pool_end: %w", err)
	}
	if _, err := netip.ParseAddr(c.Network.Gateway); err != nil {
		return fmt.Errorf("network.gateway: %w", err)
	}
	for i, d := range c.Network.DNS {
		if _, err := netip.ParseAddr(d); err != nil {
			return fmt.Errorf("network.dns[%d]: %w", i, err)
		}
	}
	if c.Network.Prefix < 1 || c.Network.Prefix > 32 {
		return errors.New("network.prefix must be in [1,32]")
	}
	if c.Network.NICName == "" {
		return errors.New("network.nic_name is required")
	}
	if c.CloudInit.DefaultUser == "" {
		return errors.New("cloudinit.default_user is required")
	}
	if c.Store.Path == "" {
		return errors.New("store.path is required")
	}
	if c.ESXi.SessionKeepaliveSeconds <= 0 {
		c.ESXi.SessionKeepaliveSeconds = 300
	}
	return nil
}
