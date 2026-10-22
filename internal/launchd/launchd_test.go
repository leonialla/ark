package launchd

import (
	"bytes"
	"encoding/xml"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestRenderPlist(t *testing.T) {
	out := RenderPlist("/Users/a&b/.local/bin/ssh-tunnel-daemon", "/Users/a&b/.config/ssh-tunnel-daemon/config.yaml", "/Users/a&b/Library/Logs/ssh-tunnel-daemon")
	dec := xml.NewDecoder(bytes.NewReader(out))
	dec.Strict = true
	dec.Entity = xml.HTMLEntity
	for {
		if _, err := dec.Token(); err == io.EOF {
			break
		} else if err != nil {
			t.Fatalf("invalid XML: %v\n%s", err, out)
		}
	}
	s := string(out)
	for _, want := range []string{
		"<string>local.ssh-tunnel-daemon</string>",
		"<string>/Users/a&amp;b/.local/bin/ssh-tunnel-daemon</string>",
		"<string>run</string>",
		"<string>--config</string>",
		"<string>/Users/a&amp;b/.config/ssh-tunnel-daemon/config.yaml</string>",
		"<string>/Users/a&amp;b/Library/Logs/ssh-tunnel-daemon/launchd.out.log</string>",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s", want)
		}
	}
	for _, key := range []string{"KeepAlive", "RunAtLoad"} {
		if !regexp.MustCompile(`<key>` + key + `</key>\s*<true/>`).MatchString(s) {
			t.Errorf("%s not true", key)
		}
	}
	if !regexp.MustCompile(`<key>ProcessType</key>\s*<string>Background</string>`).MatchString(s) {
		t.Error("ProcessType not Background")
	}
}

func TestPlistPath(t *testing.T) {
	home, _ := os.UserHomeDir()
	if got := PlistPath(); got != filepath.Join(home, "Library/LaunchAgents/local.ssh-tunnel-daemon.plist") {
		t.Fatalf("PlistPath = %s", got)
	}
}

func TestCheckBinaryLocation(t *testing.T) {
	if err := CheckBinaryLocation(filepath.Join(os.TempDir(), "go-build123/b001/exe/ssh-tunnel-daemon")); err == nil {
		t.Error("temp binary accepted")
	}
	if err := CheckBinaryLocation("/private/var/folders/xx/T/go-build99/exe/x"); err == nil {
		t.Error("go-build binary accepted")
	}
	home, _ := os.UserHomeDir()
	if err := CheckBinaryLocation(filepath.Join(home, ".local/bin/ssh-tunnel-daemon")); err != nil {
		t.Errorf("stable binary rejected: %v", err)
	}
}

func TestWriteExampleConfigKeepsExisting(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "config.yaml")
	if err := writeConfigIfMissing(p, []byte("new")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "new" {
		t.Fatalf("config = %q", b)
	}
	os.WriteFile(p, []byte("mine"), 0o600)
	if err := writeConfigIfMissing(p, []byte("new")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "mine" {
		t.Fatal("existing config overwritten")
	}
}

func TestInstallWithoutConfigOnlyWritesExample(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg := filepath.Join(home, "cfg", "config.yaml")
	created, err := Install("/usr/local/bin/ssh-tunnel-daemon", cfg, filepath.Join(home, "logs"), []byte("example"))
	if err != nil || !created {
		t.Fatalf("created=%v err=%v", created, err)
	}
	if b, _ := os.ReadFile(cfg); string(b) != "example" {
		t.Fatalf("config = %q", b)
	}
	if _, err := os.Stat(PlistPath()); !os.IsNotExist(err) {
		t.Fatalf("plist written before config existed: %v", err)
	}
}
