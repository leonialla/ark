package daemon

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"ssh-tunnel-daemon/internal/netwatch"
	"ssh-tunnel-daemon/internal/testsshd"
)

type env struct {
	s       *testsshd.Server
	cfgPath string
	sockDir string
}

func newEnv(t *testing.T) *env {
	return &env{s: testsshd.Start(t), cfgPath: filepath.Join(t.TempDir(), "config.yaml"), sockDir: testsshd.ShortTempDir(t)}
}

type tun struct {
	port   int
	remote string
}

func (e *env) write(t *testing.T, tunnels ...tun) {
	t.Helper()
	cfg := fmt.Sprintf("ssh_config_file: %s\nsocket_dir: %s\nconnect_timeout: 5s\ntunnels:\n", e.s.ClientConfig, e.sockDir)
	for _, tu := range tunnels {
		cfg += fmt.Sprintf("  - {local_port: %d, ssh: testhost, remote: %q}\n", tu.port, tu.remote)
	}
	if err := os.WriteFile(e.cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (e *env) run(t *testing.T) (*Daemon, chan netwatch.Event) {
	t.Helper()
	d, err := New(e.cfgPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	events := make(chan netwatch.Event)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		d.Run(ctx, events)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return d, events
}

func dialEcho(t *testing.T, port int, msg string) net.Conn {
	t.Helper()
	var c net.Conn
	var err error
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if c, err = net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port)); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("dial %d: %v", port, err)
	}
	roundTrip(t, c, msg)
	return c
}

func roundTrip(t *testing.T, c net.Conn, msg string) {
	t.Helper()
	c.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.WriteString(c, msg); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != msg {
		t.Fatalf("round trip %q: got %q err %v", msg, buf, err)
	}
}

func refuses(port int) bool {
	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
	if err != nil {
		return true
	}
	c.Close()
	return false
}

func TestDaemonRelays(t *testing.T) {
	e := newEnv(t)
	echo := testsshd.EchoServer(t)
	a, b := testsshd.FreePort(t), testsshd.FreePort(t)
	e.write(t, tun{a, echo}, tun{b, echo})
	d, _ := e.run(t)
	dialEcho(t, a, "a").Close()
	dialEcho(t, b, "b").Close()
	rows := d.Status()
	if len(rows) != 2 {
		t.Fatalf("rows = %d", len(rows))
	}
	for _, r := range rows {
		if r.State != "up" || r.Listen != "ok" || r.SSH != "testhost" {
			t.Fatalf("row = %+v", r)
		}
	}
	if !rows[0].Since.Equal(rows[1].Since) {
		t.Fatal("tunnels with the same target should share one master")
	}
	if rows[0].LocalPort > rows[1].LocalPort {
		t.Fatal("rows not sorted by port")
	}
}

func TestReloadAddRemoveKeep(t *testing.T) {
	e := newEnv(t)
	echo := testsshd.EchoServer(t)
	a, b, c := testsshd.FreePort(t), testsshd.FreePort(t), testsshd.FreePort(t)
	e.write(t, tun{a, echo}, tun{b, echo})
	d, _ := e.run(t)
	keep := dialEcho(t, a, "hold")
	defer keep.Close()
	dialEcho(t, b, "b").Close()

	e.write(t, tun{a, echo}, tun{c, echo})
	if err := d.Reload(); err != nil {
		t.Fatal(err)
	}
	if !refuses(b) {
		t.Fatal("removed tunnel still listening")
	}
	dialEcho(t, c, "c").Close()
	roundTrip(t, keep, "still")
	if len(d.Status()) != 2 {
		t.Fatalf("rows = %d", len(d.Status()))
	}
}

func TestReloadChangedRemoteRestarts(t *testing.T) {
	e := newEnv(t)
	echo1, echo2 := testsshd.EchoServer(t), testsshd.EchoServer(t)
	a := testsshd.FreePort(t)
	e.write(t, tun{a, echo1})
	d, _ := e.run(t)
	dialEcho(t, a, "1").Close()
	e.write(t, tun{a, echo2})
	if err := d.Reload(); err != nil {
		t.Fatal(err)
	}
	dialEcho(t, a, "2").Close()
	if got := d.Status()[0].Remote; got != echo2 {
		t.Fatalf("remote = %s", got)
	}
}

func TestReloadInvalidKeepsOld(t *testing.T) {
	e := newEnv(t)
	echo := testsshd.EchoServer(t)
	a := testsshd.FreePort(t)
	e.write(t, tun{a, echo})
	d, _ := e.run(t)
	dialEcho(t, a, "x").Close()
	os.WriteFile(e.cfgPath, []byte("tunnels: [ {local_port: oops"), 0o600)
	if err := d.Reload(); err == nil {
		t.Fatal("expected error")
	}
	dialEcho(t, a, "y").Close()
	if len(d.Status()) != 1 {
		t.Fatal("status changed")
	}
}

func TestEventResetsMasters(t *testing.T) {
	e := newEnv(t)
	echo := testsshd.EchoServer(t)
	a := testsshd.FreePort(t)
	e.write(t, tun{a, echo})
	d, events := e.run(t)
	dialEcho(t, a, "x").Close()
	events <- netwatch.Event{Kind: netwatch.Wake}
	deadline := time.Now().Add(2 * time.Second)
	for d.Status()[0].State != "idle" {
		if time.Now().After(deadline) {
			t.Fatalf("state = %s", d.Status()[0].State)
		}
		time.Sleep(20 * time.Millisecond)
	}
	dialEcho(t, a, "y").Close()
	if st := d.Status()[0].State; st != "up" {
		t.Fatalf("state = %s", st)
	}
}

func TestNewRejectsInvalidConfig(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.yaml")
	os.WriteFile(p, []byte("tunnels: [{local_port: 1}]"), 0o600)
	if _, err := New(p, nil); err == nil {
		t.Fatal("expected error")
	}
}
