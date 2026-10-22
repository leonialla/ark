package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/leonialla/ark/internal/control"
	"github.com/leonialla/ark/internal/version"
)

func versionCmd(args []string) error {
	fs := flag.NewFlagSet("version", flag.ExitOnError)
	sock := fs.String("socket", control.DefaultSocketPath(), "control socket")
	fs.Parse(args)
	daemon, err := control.FetchVersion(*sock)
	renderVersion(os.Stdout, version.String(), daemon, err)
	return nil
}

const restartHint = "\narkd is running a different version; run `ark install` to restart it with this one."

// renderVersion prints the client's version and the running daemon's, and
// points out when they differ (e.g. after an upgrade without `ark install`).
func renderVersion(w io.Writer, client, daemon string, daemonErr error) {
	fmt.Fprintf(w, "ark   %s\n", client)
	switch {
	case errors.Is(daemonErr, control.ErrNotRunning):
		fmt.Fprintln(w, "arkd  not running")
	case errors.Is(daemonErr, control.ErrNoVersion):
		fmt.Fprintln(w, "arkd  older than this ark (no version info)")
		fmt.Fprintln(w, restartHint)
	case daemonErr != nil:
		fmt.Fprintf(w, "arkd  unknown (%v)\n", daemonErr)
	default:
		fmt.Fprintf(w, "arkd  %s\n", daemon)
		if daemon != client {
			fmt.Fprintln(w, restartHint)
		}
	}
}
