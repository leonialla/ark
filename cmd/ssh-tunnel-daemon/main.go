// Command ssh-tunnel-daemon keeps SSH tunnels to private services available on
// local ports, (re)connecting on demand.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	sshtunneldaemon "ssh-tunnel-daemon"
	"ssh-tunnel-daemon/internal/config"
	"ssh-tunnel-daemon/internal/control"
	"ssh-tunnel-daemon/internal/daemon"
	"ssh-tunnel-daemon/internal/launchd"
	"ssh-tunnel-daemon/internal/logging"
	"ssh-tunnel-daemon/internal/netwatch"
)

const usage = `Usage: ssh-tunnel-daemon <command> [flags]

Commands:
  run        run the daemon in the foreground (used by launchd)
  status     show the state of every tunnel (--json for JSON)
  reload     re-read the config file
  install    install and start the launchd agent
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
	case "run":
		err = runCmd(args)
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
		fmt.Fprintln(os.Stderr, "ssh-tunnel-daemon:", err)
		os.Exit(1)
	}
}

func runCmd(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	cfgPath := fs.String("config", config.DefaultPath(), "config file")
	logPath := fs.String("log", logging.DefaultPath(), `log file ("-" for stderr)`)
	debug := fs.Bool("debug", false, "log every connection")
	sock := fs.String("socket", control.DefaultSocketPath(), "control socket")
	fs.Parse(args)

	if *logPath == "-" {
		*logPath = ""
	}
	log, closer, err := logging.New(*logPath, *debug)
	if err != nil {
		return err
	}
	defer closer.Close()

	d, err := daemon.New(*cfgPath, log)
	if err != nil {
		log.Error("cannot start", "err", err)
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	ctlErr := make(chan error, 1)
	go func() { ctlErr <- control.Serve(ctx, *sock, d) }()
	select {
	case err := <-ctlErr:
		// Serve only returns this early on failure, e.g. another daemon is running.
		log.Error("control socket failed", "err", err)
		return err
	case <-time.After(200 * time.Millisecond):
	}

	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	go func() {
		for range hup {
			d.Reload()
		}
	}()

	return d.Run(ctx, netwatch.Watch(ctx, 3*time.Second, log))
}

func statusCmd(args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "print JSON")
	sock := fs.String("socket", control.DefaultSocketPath(), "control socket")
	fs.Parse(args)
	rows, err := control.FetchStatus(*sock)
	if errors.Is(err, control.ErrNotRunning) {
		return fmt.Errorf("ssh-tunnel-daemon is not running (no control socket at %s)", *sock)
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
			return fmt.Errorf("ssh-tunnel-daemon is not running (no control socket at %s)", *sock)
		}
		return fmt.Errorf("reload failed: %w", err)
	}
	fmt.Println("config reloaded")
	return nil
}

func installCmd(args []string) error {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	cfgPath := fs.String("config", config.DefaultPath(), "config file")
	fs.Parse(args)
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return err
	}
	abs, err := filepath.Abs(*cfgPath)
	if err != nil {
		return err
	}
	created, err := launchd.Install(exe, abs, filepath.Dir(logging.DefaultPath()), sshtunneldaemon.ExampleConfig)
	if err != nil {
		return err
	}
	if created {
		fmt.Printf("no config found; wrote an example to %s\nedit it, then run `ssh-tunnel-daemon install` again\n", abs)
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
