package proxy

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"ssh-tunnel-daemon/internal/config"
	"ssh-tunnel-daemon/internal/sshmux"
	"ssh-tunnel-daemon/internal/testsshd"
)

type fixture struct {
	s    *testsshd.Server
	m    *sshmux.Master
	l    *Listener
	port int
}

func setup(t *testing.T, remote string) *fixture {
	t.Helper()
	s := testsshd.Start(t)
	m := sshmux.New(sshmux.Options{
		SSHBinary:      "/usr/bin/ssh",
		SSHConfigFile:  s.ClientConfig,
		SocketDir:      testsshd.ShortTempDir(t),
		Target:         "testhost",
		ConnectTimeout: 5 * time.Second,
	})
	t.Cleanup(m.Close)
	port := testsshd.FreePort(t)
	l := NewListener(config.Tunnel{LocalPort: port, SSH: "testhost", Remote: remote}, m, nil)
	l.Start()
	t.Cleanup(l.Stop)
	waitListen(t, l, "ok")
	return &fixture{s: s, m: m, l: l, port: port}
}

func waitListen(t *testing.T, l *Listener, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if strings.HasPrefix(l.Status().Listen, want) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("Listen = %q, want prefix %q", l.Status().Listen, want)
}

func echoRoundTrip(t *testing.T, addr string, payload []byte) {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(15 * time.Second))
	go c.Write(payload)
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(c, got); err != nil {
		t.Fatalf("read via %s: %v", addr, err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("payload mismatch via %s", addr)
	}
}

func v4(port int) string { return fmt.Sprintf("127.0.0.1:%d", port) }
func v6(port int) string { return fmt.Sprintf("[::1]:%d", port) }

func waitConns(t *testing.T, l *Listener, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if l.Status().Conns == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("Conns = %d, want %d", l.Status().Conns, want)
}

func TestRelayEcho(t *testing.T) {
	f := setup(t, testsshd.EchoServer(t))
	payload := make([]byte, 1<<20)
	rand.Read(payload)
	echoRoundTrip(t, v4(f.port), payload)
	echoRoundTrip(t, v6(f.port), payload)
	waitConns(t, f.l, 0)
}

func TestReconnectAfterMasterKilled(t *testing.T) {
	f := setup(t, testsshd.EchoServer(t))
	echoRoundTrip(t, v4(f.port), []byte("first"))
	syscall.Kill(f.m.PID(), syscall.SIGKILL)
	start := time.Now()
	echoRoundTrip(t, v4(f.port), []byte("second"))
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("reconnect took %v", d)
	}
	if f.m.Spawns() != 2 {
		t.Fatalf("spawns = %d", f.m.Spawns())
	}
}

func TestStaleMasterRetriedOnce(t *testing.T) {
	f := setup(t, testsshd.EchoServer(t))
	echoRoundTrip(t, v4(f.port), []byte("first"))
	f.m.FreezeStateForTest(true)
	syscall.Kill(f.m.PID(), syscall.SIGKILL)
	for f.m.Alive() {
		time.Sleep(20 * time.Millisecond)
	}
	if f.m.Status().State != sshmux.StateUp {
		t.Fatal("expected frozen state up")
	}
	// The client speaks first (like Redis); those bytes must be replayed on the new channel.
	echoRoundTrip(t, v4(f.port), []byte("ping"))
	if f.m.Spawns() != 2 {
		t.Fatalf("spawns = %d", f.m.Spawns())
	}
}

func TestRemoteRefusedKeepsMaster(t *testing.T) {
	closed := testsshd.FreePort(t)
	f := setup(t, v4(closed))
	c, err := net.Dial("tcp", v4(f.port))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := c.Read(make([]byte, 1)); !errors.Is(err, io.EOF) && !errors.Is(err, syscall.ECONNRESET) {
		t.Fatalf("read err = %v, want EOF", err)
	}
	if st := f.m.Status().State; st != sshmux.StateUp {
		t.Fatalf("master state = %s", st)
	}
	if f.m.Spawns() != 1 {
		t.Fatalf("spawns = %d", f.m.Spawns())
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && f.l.Status().LastError == "" {
		time.Sleep(20 * time.Millisecond)
	}
	if e := f.l.Status().LastError; !strings.Contains(e, "refused") && !strings.Contains(e, "open failed") {
		t.Fatalf("LastError = %q", e)
	}
}

func TestBindFailedRetries(t *testing.T) {
	old := bindRetry
	bindRetry = 200 * time.Millisecond
	t.Cleanup(func() { bindRetry = old })

	s := testsshd.Start(t)
	echo := testsshd.EchoServer(t)
	m := sshmux.New(sshmux.Options{SSHBinary: "/usr/bin/ssh", SSHConfigFile: s.ClientConfig,
		SocketDir: testsshd.ShortTempDir(t), Target: "testhost", ConnectTimeout: 5 * time.Second})
	t.Cleanup(m.Close)
	occupier, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := occupier.Addr().(*net.TCPAddr).Port
	l := NewListener(config.Tunnel{LocalPort: port, SSH: "testhost", Remote: echo}, m, nil)
	l.Start()
	t.Cleanup(l.Stop)
	waitListen(t, l, "bind failed:")
	occupier.Close()
	waitListen(t, l, "ok")
	echoRoundTrip(t, v4(port), []byte("ping"))
}

func TestIPv6UnavailableIsWarning(t *testing.T) {
	old := listen
	listen = func(network, addr string) (net.Listener, error) {
		if network == "tcp6" {
			return nil, errors.New("no ipv6")
		}
		return old(network, addr)
	}
	t.Cleanup(func() { listen = old })
	f := setup(t, testsshd.EchoServer(t))
	echoRoundTrip(t, v4(f.port), []byte("ping"))
}

func TestStopClosesActiveConns(t *testing.T) {
	f := setup(t, testsshd.EchoServer(t))
	c, err := net.Dial("tcp", v4(f.port))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Write([]byte("x"))
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	c.Read(make([]byte, 1))
	waitConns(t, f.l, 1)
	f.l.Stop()
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.Read(make([]byte, 1)); err == nil {
		t.Fatal("expected connection to close")
	} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatal("connection not closed by Stop")
	}
	if _, err := net.DialTimeout("tcp", v4(f.port), time.Second); err == nil {
		t.Fatal("port still accepting after Stop")
	}
}

func TestLongLivedConnectionOutlivesEnsureBound(t *testing.T) {
	old := ensureTimeout
	ensureTimeout = time.Second
	t.Cleanup(func() { ensureTimeout = old })
	f := setup(t, testsshd.EchoServer(t))
	c, err := net.Dial("tcp", v4(f.port))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for i := 0; i < 3; i++ {
		c.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := c.Write([]byte("ping")); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
		buf := make([]byte, 4)
		if _, err := io.ReadFull(c, buf); err != nil {
			t.Fatalf("round trip %d after %ds: %v", i, i, err)
		}
		time.Sleep(time.Second)
	}
}

func TestPoolOnDeadMasterAllRecover(t *testing.T) {
	f := setup(t, testsshd.EchoServer(t))
	echoRoundTrip(t, v4(f.port), []byte("first"))
	f.m.FreezeStateForTest(true)
	syscall.Kill(f.m.PID(), syscall.SIGKILL)
	for f.m.Alive() {
		time.Sleep(20 * time.Millisecond)
	}
	errs := make(chan error, 10)
	for i := 0; i < 10; i++ {
		go func(i int) {
			c, err := net.Dial("tcp", v4(f.port))
			if err != nil {
				errs <- err
				return
			}
			defer c.Close()
			c.SetDeadline(time.Now().Add(15 * time.Second))
			msg := fmt.Sprintf("conn-%d", i)
			if _, err := io.WriteString(c, msg); err != nil {
				errs <- err
				return
			}
			buf := make([]byte, len(msg))
			if _, err := io.ReadFull(c, buf); err != nil || string(buf) != msg {
				errs <- fmt.Errorf("conn %d: got %q err %v", i, buf, err)
				return
			}
			errs <- nil
		}(i)
	}
	for i := 0; i < 10; i++ {
		if err := <-errs; err != nil {
			t.Error(err)
		}
	}
	if e := f.l.Status().LastError; e != "" {
		t.Logf("LastError = %q", e)
	}
}

// occupyWithSSH starts `ssh -N -L 127.0.0.1:port:<remote> testhost` and waits until it listens.
func occupyWithSSH(t *testing.T, s *testsshd.Server, port int, remote string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("/usr/bin/ssh", "-F", s.ClientConfig, "-N", "-o", "ExitOnForwardFailure=yes",
		"-L", fmt.Sprintf("127.0.0.1:%d:%s", port, remote), "testhost")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.Dial("tcp", v4(port)); err == nil {
			c.Close()
			return cmd
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("ssh -L did not start listening")
	return nil
}

func newListener(t *testing.T, s *testsshd.Server, port int, remote string) (*Listener, *sshmux.Master) {
	t.Helper()
	m := sshmux.New(sshmux.Options{SSHBinary: "/usr/bin/ssh", SSHConfigFile: s.ClientConfig,
		SocketDir: testsshd.ShortTempDir(t), Target: "testhost", ConnectTimeout: 5 * time.Second})
	t.Cleanup(m.Close)
	l := NewListener(config.Tunnel{LocalPort: port, SSH: "testhost", Remote: remote}, m, nil)
	l.Start()
	t.Cleanup(l.Stop)
	return l, m
}

func TestTakeoverSameForward(t *testing.T) {
	s := testsshd.Start(t)
	echo := testsshd.EchoServer(t)
	port := testsshd.FreePort(t)
	old := occupyWithSSH(t, s, port, echo)
	exited := make(chan struct{})
	go func() { old.Wait(); close(exited) }()

	l, m := newListener(t, s, port, echo)
	waitListen(t, l, "ok")
	select {
	case <-exited:
	case <-time.After(3 * time.Second):
		t.Fatal("old ssh -L still running")
	}
	echoRoundTrip(t, v4(port), []byte("ours"))
	if m.Spawns() != 1 {
		t.Fatalf("connection not served by the daemon: spawns = %d", m.Spawns())
	}
}

func TestNoTakeoverDifferentForward(t *testing.T) {
	s := testsshd.Start(t)
	echo, other := testsshd.EchoServer(t), testsshd.EchoServer(t)
	port := testsshd.FreePort(t)
	old := occupyWithSSH(t, s, port, other)

	l, _ := newListener(t, s, port, echo)
	waitListen(t, l, "bind failed:")
	time.Sleep(1500 * time.Millisecond)
	if err := old.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("different-forward ssh was killed: %v", err)
	}
	want := fmt.Sprintf("held by ssh pid %d", old.Process.Pid)
	if got := l.Status().Listen; !strings.Contains(got, want) {
		t.Fatalf("Listen = %q, want it to mention %q", got, want)
	}
}

func TestNoTakeoverNonSSH(t *testing.T) {
	s := testsshd.Start(t)
	occupier, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupier.Close()
	port := occupier.Addr().(*net.TCPAddr).Port
	l, _ := newListener(t, s, port, testsshd.EchoServer(t))
	waitListen(t, l, "bind failed:")
	if got, want := l.Status().Listen, fmt.Sprintf("pid %d", os.Getpid()); !strings.Contains(got, want) {
		t.Fatalf("Listen = %q, want it to mention %q", got, want)
	}
}
