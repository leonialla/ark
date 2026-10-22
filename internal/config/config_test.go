package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseDefaults(t *testing.T) {
	cfg, err := Parse([]byte(`
tunnels:
  - local_port: 4333
    ssh: ubuntu@203.0.113.180
    remote: localhost:4333
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SSHBinary != "/usr/bin/ssh" {
		t.Errorf("SSHBinary = %q", cfg.SSHBinary)
	}
	if cfg.ConnectTimeout != 10*time.Second {
		t.Errorf("ConnectTimeout = %v", cfg.ConnectTimeout)
	}
	if want := filepath.Join(os.TempDir(), "ark"); cfg.SocketDir != want {
		t.Errorf("SocketDir = %q, want %q", cfg.SocketDir, want)
	}
	if got := cfg.Tunnels[0].Label(); got != "4333" {
		t.Errorf("Label = %q", got)
	}
}

func TestParseConnectTimeout(t *testing.T) {
	cfg, err := Parse([]byte(`
connect_timeout: 3s
tunnels:
  - {name: a, local_port: 1, ssh: h, remote: "x:1"}
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ConnectTimeout != 3*time.Second {
		t.Errorf("ConnectTimeout = %v", cfg.ConnectTimeout)
	}
	if cfg.Tunnels[0].Label() != "a" {
		t.Errorf("Label = %q", cfg.Tunnels[0].Label())
	}
	if _, err := Parse([]byte("connect_timeout: soon\n")); err == nil {
		t.Error("expected error for bad duration")
	}
}

func TestParseDuplicatePort(t *testing.T) {
	_, err := Parse([]byte(`
tunnels:
  - {local_port: 4333, ssh: a, remote: "localhost:4333"}
  - {local_port: 4333, ssh: b, remote: "localhost:4334"}
`))
	if err == nil || !strings.Contains(err.Error(), "duplicate local_port 4333") {
		t.Fatalf("err = %v", err)
	}
}

func TestParseBadRemote(t *testing.T) {
	_, err := Parse([]byte(`
tunnels:
  - {local_port: 6379, ssh: a, remote: localhost}
`))
	if err == nil || !strings.Contains(err.Error(), "remote") {
		t.Fatalf("err = %v", err)
	}
	_, err = Parse([]byte(`
tunnels:
  - {local_port: 6379, ssh: a, remote: "localhost:redis"}
`))
	if err == nil || !strings.Contains(err.Error(), "remote") {
		t.Fatalf("non-numeric port: err = %v", err)
	}
	if _, err := Parse([]byte(`
tunnels:
  - {local_port: 6379, ssh: a, remote: "[::1]:6379"}
`)); err != nil {
		t.Fatalf("ipv6 remote: %v", err)
	}
}

func TestParseMissingFields(t *testing.T) {
	_, err := Parse([]byte(`
tunnels:
  - {local_port: 1, ssh: a, remote: "h:1"}
  - {local_port: 2, remote: "h:1"}
`))
	if err == nil || !strings.Contains(err.Error(), "tunnels[1]") || !strings.Contains(err.Error(), "ssh") {
		t.Fatalf("missing ssh: err = %v", err)
	}
	_, err = Parse([]byte(`
tunnels:
  - {local_port: 70000, ssh: a, remote: "h:1"}
`))
	if err == nil || !strings.Contains(err.Error(), "tunnels[0]") || !strings.Contains(err.Error(), "local_port") {
		t.Fatalf("bad port: err = %v", err)
	}
	_, err = Parse([]byte(`
tunnels:
  - {ssh: a, remote: "h:1"}
`))
	if err == nil || !strings.Contains(err.Error(), "local_port") {
		t.Fatalf("missing port: err = %v", err)
	}
}

func TestExpandHome(t *testing.T) {
	home, _ := os.UserHomeDir()
	if got := ExpandHome("~/x"); got != filepath.Join(home, "x") {
		t.Errorf("ExpandHome = %q", got)
	}
	if got := ExpandHome("/abs"); got != "/abs" {
		t.Errorf("ExpandHome = %q", got)
	}

	path := filepath.Join(t.TempDir(), "c.yaml")
	os.WriteFile(path, []byte(`
ssh_config_file: ~/.ssh/test_config
socket_dir: ~/sock
tunnels:
  - {local_port: 1, ssh: a, remote: "h:1"}
`), 0o600)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SSHConfigFile != filepath.Join(home, ".ssh/test_config") {
		t.Errorf("SSHConfigFile = %q", cfg.SSHConfigFile)
	}
	if cfg.SocketDir != filepath.Join(home, "sock") {
		t.Errorf("SocketDir = %q", cfg.SocketDir)
	}
}

func TestDefaultPath(t *testing.T) {
	home, _ := os.UserHomeDir()
	if got := DefaultPath(); got != filepath.Join(home, ".config/ark/config.yaml") {
		t.Errorf("DefaultPath = %q", got)
	}
}

func TestExampleConfigParses(t *testing.T) {
	cfg, err := Load("../../config.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Tunnels) != 2 {
		t.Errorf("tunnels = %d", len(cfg.Tunnels))
	}
}

func TestIdleTimeout(t *testing.T) {
	tunnel := "tunnels:\n  - {local_port: 1, ssh: a, remote: \"h:1\"}\n"
	cases := map[string]time.Duration{
		"":                      30 * time.Minute,
		"idle_timeout: 10m\n":   10 * time.Minute,
		"idle_timeout: 0\n":     0,
		"idle_timeout: \"0\"\n": 0,
		"idle_timeout: 0s\n":    0,
	}
	for prefix, want := range cases {
		cfg, err := Parse([]byte(prefix + tunnel))
		if err != nil {
			t.Fatalf("%q: %v", prefix, err)
		}
		if cfg.IdleTimeout != want {
			t.Errorf("%q: IdleTimeout = %v, want %v", prefix, cfg.IdleTimeout, want)
		}
	}
	for _, bad := range []string{"idle_timeout: -5m\n", "idle_timeout: soon\n"} {
		if _, err := Parse([]byte(bad + tunnel)); err == nil || !strings.Contains(err.Error(), "idle_timeout") {
			t.Errorf("%q: err = %v", bad, err)
		}
	}
}
