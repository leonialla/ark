// Command arkd is the ark daemon: it keeps SSH tunnels to private services
// available on local ports, (re)connecting on demand. launchd runs it; use the
// ark command to install it and to query or reload it.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/leonialla/ark/internal/config"
	"github.com/leonialla/ark/internal/control"
	"github.com/leonialla/ark/internal/daemon"
	"github.com/leonialla/ark/internal/logging"
	"github.com/leonialla/ark/internal/netwatch"
	"github.com/leonialla/ark/internal/version"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "arkd:", err)
		os.Exit(1)
	}
}

func run() error {
	cfgPath := flag.String("config", config.DefaultPath(), "config file")
	logPath := flag.String("log", logging.DefaultPath(), `log file ("-" for stderr)`)
	debug := flag.Bool("debug", false, "log every connection")
	sock := flag.String("socket", control.DefaultSocketPath(), "control socket")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println("arkd", version.String())
		return nil
	}

	if *logPath == "-" {
		*logPath = ""
	}
	log, closer, err := logging.New(*logPath, *debug)
	if err != nil {
		return err
	}
	defer closer.Close()
	log.Info("arkd starting", "version", version.String())

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
