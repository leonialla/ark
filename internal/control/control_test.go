package control

import (
	"context"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leonialla/ark/internal/daemon"
	"github.com/leonialla/ark/internal/testsshd"
	"github.com/leonialla/ark/internal/version"
)

type fake struct {
	rows      []daemon.TunnelStatus
	reloadErr error
	reloads   int
}

func (f *fake) Status() []daemon.TunnelStatus { return f.rows }
func (f *fake) Reload() error                 { f.reloads++; return f.reloadErr }

func serve(t *testing.T, sock string, b Backend) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- Serve(ctx, sock, b) }()
	t.Cleanup(func() {
		cancel()
		<-errc
	})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := FetchStatus(sock); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("server did not come up")
}

func TestServeStatusAndReload(t *testing.T) {
	sock := filepath.Join(testsshd.ShortTempDir(t), "d.sock")
	f := &fake{rows: []daemon.TunnelStatus{{Name: "db", LocalPort: 4333, State: "up"}}}
	serve(t, sock, f)
	rows, err := FetchStatus(sock)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Name != "db" || rows[0].State != "up" {
		t.Fatalf("rows = %+v", rows)
	}
	if err := Reload(sock); err != nil || f.reloads != 1 {
		t.Fatalf("reload err=%v reloads=%d", err, f.reloads)
	}
	f.reloadErr = errors.New("tunnels[0]: ssh is required")
	if err := Reload(sock); err == nil || !strings.Contains(err.Error(), "tunnels[0]: ssh is required") {
		t.Fatalf("reload err = %v", err)
	}
}

func TestNotRunning(t *testing.T) {
	sock := filepath.Join(testsshd.ShortTempDir(t), "none.sock")
	if _, err := FetchStatus(sock); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("err = %v", err)
	}
	if err := Reload(sock); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("err = %v", err)
	}
}

func TestAlreadyRunning(t *testing.T) {
	sock := filepath.Join(testsshd.ShortTempDir(t), "d.sock")
	serve(t, sock, &fake{})
	err := Serve(context.Background(), sock, &fake{})
	if err == nil || !strings.Contains(err.Error(), "daemon already running") {
		t.Fatalf("err = %v", err)
	}
}

func TestStaleSocketReplaced(t *testing.T) {
	dir := testsshd.ShortTempDir(t)
	sock := filepath.Join(dir, "d.sock")
	// A socket file left behind by a crashed daemon: present, but nobody listening.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { Serve(ctx, sock, &fake{}); close(done) }()
	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done
	writeStale(t, sock)
	serve(t, sock, &fake{rows: []daemon.TunnelStatus{{Name: "x"}}})
}

func TestServeVersion(t *testing.T) {
	sock := filepath.Join(testsshd.ShortTempDir(t), "d.sock")
	serve(t, sock, &fake{})
	got, err := FetchVersion(sock)
	if err != nil {
		t.Fatal(err)
	}
	if want := version.String(); got != want {
		t.Fatalf("FetchVersion = %q, want %q", got, want)
	}
	if _, err := FetchVersion(filepath.Join(testsshd.ShortTempDir(t), "none.sock")); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("not running: err = %v", err)
	}
}

func TestFetchVersionFromOldDaemon(t *testing.T) {
	// A daemon built before /version existed answers 404.
	sock := filepath.Join(testsshd.ShortTempDir(t), "old.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.NotFoundHandler()}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
	if _, err := FetchVersion(sock); !errors.Is(err, ErrNoVersion) {
		t.Fatalf("err = %v, want ErrNoVersion", err)
	}
}
