// Package launchd installs the daemon as a per-user macOS LaunchAgent.
package launchd

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"ark/internal/config"
)

const Label = "local.ark"

func PlistPath() string {
	return config.ExpandHome("~/Library/LaunchAgents/" + Label + ".plist")
}

func esc(s string) string {
	var b bytes.Buffer
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

// RenderPlist returns a LaunchAgent plist that runs `binary --config configPath`
// at login and restarts it whenever it exits.
func RenderPlist(binary, configPath, logDir string) []byte {
	out := esc(filepath.Join(logDir, "launchd.out.log"))
	return []byte(fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
		<string>--config</string>
		<string>%s</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>ProcessType</key>
	<string>Background</string>
	<key>StandardOutPath</key>
	<string>%s</string>
	<key>StandardErrorPath</key>
	<string>%s</string>
</dict>
</plist>
`, Label, esc(binary), esc(configPath), out, out))
}

// CheckBinaryLocation rejects binaries that will disappear, such as `go run` builds.
func CheckBinaryLocation(binary string) error {
	tmp, _ := filepath.EvalSymlinks(os.TempDir())
	if strings.Contains(binary, "/go-build") ||
		strings.HasPrefix(binary, filepath.Clean(os.TempDir())+"/") ||
		(tmp != "" && strings.HasPrefix(binary, tmp+"/")) {
		return fmt.Errorf("%s is a temporary build; install the binary somewhere stable first (e.g. `make install`)", binary)
	}
	return nil
}

func writeConfigIfMissing(path string, example []byte) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, example, 0o600)
}

func domain() string { return fmt.Sprintf("gui/%d", os.Getuid()) }

// Install writes the plist and (re)loads the agent. If configPath does not exist
// it only writes the example config there and returns createdConfig=true without
// loading anything, so the daemon never starts on placeholder tunnels.
func Install(binary, configPath, logDir string, example []byte) (createdConfig bool, err error) {
	if err := CheckBinaryLocation(binary); err != nil {
		return false, err
	}
	if _, err := os.Stat(configPath); errors.Is(err, os.ErrNotExist) {
		return true, writeConfigIfMissing(configPath, example)
	}
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return false, err
	}
	plist := PlistPath()
	if err := os.MkdirAll(filepath.Dir(plist), 0o755); err != nil {
		return false, err
	}
	if err := os.WriteFile(plist, RenderPlist(binary, configPath, logDir), 0o644); err != nil {
		return false, err
	}
	exec.Command("launchctl", "bootout", domain()+"/"+Label).Run()
	if out, err := exec.Command("launchctl", "bootstrap", domain(), plist).CombinedOutput(); err != nil {
		return false, fmt.Errorf("launchctl bootstrap: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return false, nil
}

// Uninstall stops the agent and removes the plist. Config and logs are kept.
func Uninstall() error {
	exec.Command("launchctl", "bootout", domain()+"/"+Label).Run()
	if err := os.Remove(PlistPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
