package sshmux

import (
	"context"
	"io"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"ssh-tunnel-daemon/internal/testsshd"
)

func newMaster(t *testing.T, s *testsshd.Server, target string) *Master {
	t.Helper()
	m := New(Options{
		SSHBinary:      "/usr/bin/ssh",
		SSHConfigFile:  s.ClientConfig,
		SocketDir:      testsshd.ShortTempDir(t),
		Target:         target,
		ConnectTimeout: 5 * time.Second,
	})
	t.Cleanup(m.Close)
	return m
}

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	return c
}

func roundTrip(t *testing.T, m *Master, remote, msg string) {
	t.Helper()
	ch, err := m.Dial(ctx(t), remote)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer ch.Close()
	if _, err := io.WriteString(ch, msg); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(ch, buf); err != nil {
		t.Fatalf("read: %v (stderr %q)", err, ch.Stderr())
	}
	if string(buf) != msg {
		t.Fatalf("got %q want %q", buf, msg)
	}
}

func waitState(t *testing.T, m *Master, want State, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if m.Status().State == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("state = %s, want %s", m.Status().State, want)
}

func TestEnsureAndDialEcho(t *testing.T) {
	s := testsshd.Start(t)
	echo := testsshd.EchoServer(t)
	m := newMaster(t, s, "testhost")
	if err := m.Ensure(ctx(t)); err != nil {
		t.Fatal(err)
	}
	if st := m.Status(); st.State != StateUp || st.Target != "testhost" {
		t.Fatalf("status = %+v", st)
	}
	roundTrip(t, m, echo, "ping")
	if m.PID() == 0 || m.Spawns() != 1 {
		t.Fatalf("pid=%d spawns=%d", m.PID(), m.Spawns())
	}
}

func TestEnsureViaProxyJump(t *testing.T) {
	s := testsshd.Start(t)
	echo := testsshd.EchoServer(t)
	m := newMaster(t, s, "jumped")
	if err := m.Ensure(ctx(t)); err != nil {
		t.Fatal(err)
	}
	roundTrip(t, m, echo, "ping")
}

func TestEnsureSingleFlight(t *testing.T) {
	s := testsshd.Start(t)
	echo := testsshd.EchoServer(t)
	m := newMaster(t, s, "testhost")
	var wg sync.WaitGroup
	errs := make(chan error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- m.Ensure(ctx(t))
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if m.Spawns() != 1 {
		t.Fatalf("spawns = %d", m.Spawns())
	}
	roundTrip(t, m, echo, "ping")
}

func TestEnsureFailsFastWithStderr(t *testing.T) {
	s := testsshd.Start(t)
	m := newMaster(t, s, "badport")
	m.o.ConnectTimeout = 2 * time.Second
	start := time.Now()
	err := m.Ensure(ctx(t))
	if err == nil {
		t.Fatal("expected error")
	}
	if d := time.Since(start); d > 4*time.Second {
		t.Fatalf("took %v", d)
	}
	st := m.Status()
	if st.State != StateError || !strings.Contains(st.LastError, "Connection refused") {
		t.Fatalf("status = %+v", st)
	}
}

func TestResetThenEnsure(t *testing.T) {
	s := testsshd.Start(t)
	echo := testsshd.EchoServer(t)
	m := newMaster(t, s, "testhost")
	if err := m.Ensure(ctx(t)); err != nil {
		t.Fatal(err)
	}
	m.Reset("test")
	if st := m.Status(); st.State != StateIdle {
		t.Fatalf("state = %s", st.State)
	}
	if _, err := os.Stat(m.sock); !os.IsNotExist(err) {
		t.Fatalf("socket still present: %v", err)
	}
	if err := m.Ensure(ctx(t)); err != nil {
		t.Fatal(err)
	}
	roundTrip(t, m, echo, "again")
	if m.Spawns() != 2 {
		t.Fatalf("spawns = %d", m.Spawns())
	}
}

func TestMasterDiesUnexpectedly(t *testing.T) {
	s := testsshd.Start(t)
	m := newMaster(t, s, "testhost")
	if err := m.Ensure(ctx(t)); err != nil {
		t.Fatal(err)
	}
	syscall.Kill(m.PID(), syscall.SIGKILL)
	waitState(t, m, StateError, 2*time.Second)
	if m.Alive() {
		t.Fatal("Alive() = true after kill")
	}
}

func TestStaleSocketCleared(t *testing.T) {
	s := testsshd.Start(t)
	m := newMaster(t, s, "testhost")
	os.MkdirAll(m.o.SocketDir, 0o700)
	if err := os.WriteFile(m.sock, []byte("junk"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.Ensure(ctx(t)); err != nil {
		t.Fatal(err)
	}
}

func TestDialWithoutMasterDoesNotConnectDirectly(t *testing.T) {
	s := testsshd.Start(t)
	echo := testsshd.EchoServer(t)
	m := newMaster(t, s, "testhost")
	ch, err := m.Dial(ctx(t), echo)
	if err != nil {
		t.Fatal(err)
	}
	defer ch.Close()
	io.WriteString(ch, "ping")
	if n, _ := io.ReadFull(ch, make([]byte, 4)); n != 0 {
		t.Fatal("channel relayed data without a master")
	}
	if ch.Wait() == nil {
		t.Fatal("expected channel error")
	}
}

func TestFailureBackoff(t *testing.T) {
	s := testsshd.Start(t)
	m := newMaster(t, s, "badport")
	err1 := m.Ensure(ctx(t))
	if err1 == nil {
		t.Fatal("expected error")
	}
	err2 := m.Ensure(ctx(t))
	if err2 == nil || err2.Error() != err1.Error() || m.Spawns() != 1 {
		t.Fatalf("err2=%v spawns=%d", err2, m.Spawns())
	}
	time.Sleep(failureBackoff)
	m.Ensure(ctx(t))
	if m.Spawns() != 2 {
		t.Fatalf("spawns after backoff = %d", m.Spawns())
	}
}
