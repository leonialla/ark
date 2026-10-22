// Package config loads and validates the daemon's YAML configuration.
package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Tunnel maps one local loopback port to a remote address reachable from an SSH target.
type Tunnel struct {
	Name      string `yaml:"name"`
	LocalPort int    `yaml:"local_port"`
	// SSH is a user@host[:port] destination or a Host alias from ~/.ssh/config.
	SSH string `yaml:"ssh"`
	// Remote is the host:port to connect to, as seen from the SSH server.
	Remote string `yaml:"remote"`
}

// Label is the tunnel's display name.
func (t Tunnel) Label() string {
	if t.Name != "" {
		return t.Name
	}
	return strconv.Itoa(t.LocalPort)
}

type Config struct {
	SSHBinary      string
	SSHConfigFile  string
	ConnectTimeout time.Duration
	SocketDir      string
	Tunnels        []Tunnel
}

type rawConfig struct {
	SSHBinary      string   `yaml:"ssh_binary"`
	SSHConfigFile  string   `yaml:"ssh_config_file"`
	ConnectTimeout string   `yaml:"connect_timeout"`
	SocketDir      string   `yaml:"socket_dir"`
	Tunnels        []Tunnel `yaml:"tunnels"`
}

// DefaultPath is ~/.config/ssh-tunnel-daemon/config.yaml.
func DefaultPath() string {
	return ExpandHome("~/.config/ssh-tunnel-daemon/config.yaml")
}

// ExpandHome replaces a leading "~/" with the user's home directory.
func ExpandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

func Parse(data []byte) (*Config, error) {
	var raw rawConfig
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	cfg := &Config{
		SSHBinary:     raw.SSHBinary,
		SSHConfigFile: ExpandHome(raw.SSHConfigFile),
		SocketDir:     ExpandHome(raw.SocketDir),
		Tunnels:       raw.Tunnels,
	}
	if cfg.SSHBinary == "" {
		cfg.SSHBinary = "/usr/bin/ssh"
	}
	if cfg.SocketDir == "" {
		cfg.SocketDir = filepath.Join(os.TempDir(), "stdd")
	}
	cfg.ConnectTimeout = 10 * time.Second
	if raw.ConnectTimeout != "" {
		d, err := time.ParseDuration(raw.ConnectTimeout)
		if err != nil || d <= 0 {
			return nil, fmt.Errorf("connect_timeout: invalid duration %q", raw.ConnectTimeout)
		}
		cfg.ConnectTimeout = d
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) validate() error {
	seen := map[int]bool{}
	for i, t := range c.Tunnels {
		if t.LocalPort < 1 || t.LocalPort > 65535 {
			return fmt.Errorf("tunnels[%d]: local_port must be 1-65535, got %d", i, t.LocalPort)
		}
		if seen[t.LocalPort] {
			return fmt.Errorf("tunnels[%d]: duplicate local_port %d", i, t.LocalPort)
		}
		seen[t.LocalPort] = true
		if strings.TrimSpace(t.SSH) == "" {
			return fmt.Errorf("tunnels[%d]: ssh is required", i)
		}
		host, port, err := net.SplitHostPort(t.Remote)
		if err != nil || host == "" {
			return fmt.Errorf("tunnels[%d]: remote must be host:port, got %q", i, t.Remote)
		}
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			return fmt.Errorf("tunnels[%d]: remote port must be numeric 1-65535, got %q", i, t.Remote)
		}
	}
	return nil
}
