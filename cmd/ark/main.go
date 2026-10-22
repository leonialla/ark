// Command ark is the client for arkd: it shows tunnel status, reloads the
// config, and installs or removes arkd as a launchd agent.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"ark"
	"ark/internal/config"
	"ark/internal/control"
	"ark/internal/launchd"
	"ark/internal/logging"
)

const usage = `Usage: ark <command> [flags]

Commands:
  status     show the state of every tunnel (--json for JSON)
  reload     make arkd re-read the config file
  install    install and start arkd as a launchd agent
  uninstall  stop and remove the launchd agent

Config: %s
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, usage, config.DefaultPath())
		os.Exit(2)
	}
	var err error
	switch cmd, args := os.Args[1], os.Args[2:]; cmd {
	case "status":
		err = statusCmd(args)
	case "reload":
		err = reloadCmd(args)
	case "install":
		err = installCmd(args)
	case "uninstall":
		err = uninstallCmd(args)
	case "-h", "--help", "help":
		fmt.Printf(usage, config.DefaultPath())
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n"+usage, cmd, config.DefaultPath())
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "ark:", err)
		os.Exit(1)
	}
}

func notRunning(sock string) error {
	return fmt.Errorf("arkd is not running (no control socket at %s); start it with `ark install`", sock)
}

func statusCmd(args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "print JSON")
	sock := fs.String("socket", control.DefaultSocketPath(), "control socket")
	fs.Parse(args)
	rows, err := control.FetchStatus(*sock)
	if errors.Is(err, control.ErrNotRunning) {
		return notRunning(*sock)
	}
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rows)
	}
	renderTable(os.Stdout, rows, time.Now())
	return nil
}

func reloadCmd(args []string) error {
	fs := flag.NewFlagSet("reload", flag.ExitOnError)
	sock := fs.String("socket", control.DefaultSocketPath(), "control socket")
	fs.Parse(args)
	if err := control.Reload(*sock); err != nil {
		if errors.Is(err, control.ErrNotRunning) {
			return notRunning(*sock)
		}
		return fmt.Errorf("reload failed: %w", err)
	}
	fmt.Println("config reloaded")
	return nil
}

// arkdPath is the arkd binary installed next to this ark binary.
func arkdPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return "", err
	}
	p := filepath.Join(filepath.Dir(exe), "arkd")
	if _, err := os.Stat(p); err != nil {
		return "", fmt.Errorf("arkd not found next to ark (%s); install both binaries into the same directory", p)
	}
	return p, nil
}

func installCmd(args []string) error {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	cfgPath := fs.String("config", config.DefaultPath(), "config file")
	fs.Parse(args)
	arkd, err := arkdPath()
	if err != nil {
		return err
	}
	abs, err := filepath.Abs(*cfgPath)
	if err != nil {
		return err
	}
	created, err := launchd.Install(arkd, abs, filepath.Dir(logging.DefaultPath()), ark.ExampleConfig)
	if err != nil {
		return err
	}
	if created {
		fmt.Printf("no config found; wrote an example to %s\nedit it, then run `ark install` again\n", abs)
		return nil
	}
	fmt.Printf("installed launchd agent %s (%s)\nlogs: %s\n", launchd.Label, launchd.PlistPath(), logging.DefaultPath())
	return nil
}

func uninstallCmd(args []string) error {
	if err := launchd.Uninstall(); err != nil {
		return err
	}
	fmt.Println("launchd agent removed (config and logs kept)")
	return nil
}
