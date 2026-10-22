// Package control exposes the daemon's status and reload over HTTP on a unix socket.
package control

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/leonialla/ark/internal/config"
	"github.com/leonialla/ark/internal/daemon"
	"github.com/leonialla/ark/internal/version"
)

type Backend interface {
	Status() []daemon.TunnelStatus
	Reload() error
}

// DefaultSocketPath is ~/.config/ark/arkd.sock.
func DefaultSocketPath() string {
	return config.ExpandHome("~/.config/ark/arkd.sock")
}

// Serve answers GET /status, GET /version and POST /reload on sockPath until ctx ends.
func Serve(ctx context.Context, sockPath string, b Backend) error {
	if err := os.MkdirAll(filepath.Dir(sockPath), 0o700); err != nil {
		return err
	}
	if _, err := os.Lstat(sockPath); err == nil {
		if c, err := net.DialTimeout("unix", sockPath, time.Second); err == nil {
			c.Close()
			return errors.New("daemon already running (control socket " + sockPath + " is answering)")
		}
		os.Remove(sockPath)
	}
	l, err := net.Listen("unix", sockPath)
	if err != nil {
		return err
	}
	os.Chmod(sockPath, 0o600)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(b.Status())
	})
	mux.HandleFunc("GET /version", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, version.String())
	})
	mux.HandleFunc("POST /reload", func(w http.ResponseWriter, r *http.Request) {
		if err := b.Reload(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	srv := &http.Server{Handler: mux}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()
	if err := srv.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
