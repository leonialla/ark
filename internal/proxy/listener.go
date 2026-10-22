// Package proxy listens on a tunnel's loopback port and relays each
// connection through an ssh channel.
package proxy

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"ssh-tunnel-daemon/internal/config"
	"ssh-tunnel-daemon/internal/portowner"
	"ssh-tunnel-daemon/internal/sshmux"
)

// bindRetry is how often a failed bind is retried.
var bindRetry = 5 * time.Second

var listen = net.Listen

var findOwners = portowner.Find

type ListenerStatus struct {
	Listen      string // "starting", "ok" or "bind failed: <err>"
	Conns       int
	LastError   string
	LastErrorAt time.Time
}

type Listener struct {
	t   config.Tunnel
	m   *sshmux.Master
	log *slog.Logger

	mu        sync.Mutex
	listen    string
	lastErr   string
	lastErrAt time.Time
	sockets   []net.Listener
	conns     map[net.Conn]struct{}
	stopped   bool
	stop      chan struct{}
	// tookOver holds pids we already tried to take the port from, so we try once each.
	tookOver map[int]bool
	wg       sync.WaitGroup
}

func NewListener(t config.Tunnel, m *sshmux.Master, log *slog.Logger) *Listener {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Listener{
		t:        t,
		m:        m,
		log:      log.With("tunnel", t.Label(), "port", t.LocalPort),
		listen:   "starting",
		conns:    map[net.Conn]struct{}{},
		tookOver: map[int]bool{},
		stop:     make(chan struct{}),
	}
}

func (l *Listener) Tunnel() config.Tunnel { return l.t }

// Start binds in the background, retrying until bound or stopped.
func (l *Listener) Start() {
	l.wg.Add(1)
	go func() {
		defer l.wg.Done()
		for {
			err := l.bind()
			if err == nil {
				return
			}
			msg := "bind failed: " + err.Error()
			if errors.Is(err, syscall.EADDRINUSE) {
				holders, took := l.takeover()
				if took {
					continue
				}
				if holders != "" {
					msg += " (held by " + holders + ")"
				}
			}
			l.mu.Lock()
			first := l.listen != msg
			l.listen = msg
			l.mu.Unlock()
			if first {
				l.log.Error("bind failed, will retry", "err", msg, "every", bindRetry)
			}
			select {
			case <-l.stop:
				return
			case <-time.After(bindRetry):
			}
		}
	}()
}

func (l *Listener) bind() error {
	port := strconv.Itoa(l.t.LocalPort)
	s4, err := listen("tcp4", net.JoinHostPort("127.0.0.1", port))
	if err != nil {
		return err
	}
	socks := []net.Listener{s4}
	if s6, err := listen("tcp6", net.JoinHostPort("::1", port)); err != nil {
		l.log.Warn("ipv6 loopback unavailable, listening on 127.0.0.1 only", "err", err)
	} else {
		socks = append(socks, s6)
	}

	l.mu.Lock()
	if l.stopped {
		l.mu.Unlock()
		for _, s := range socks {
			s.Close()
		}
		return nil
	}
	l.sockets = socks
	l.listen = "ok"
	for _, s := range socks {
		l.wg.Add(1)
		go l.accept(s)
	}
	l.mu.Unlock()
	l.log.Info("listening", "remote", l.t.Remote, "ssh", l.t.SSH)
	return nil
}

// takeover stops the port's holder if it is our own user's ssh -L with exactly this
// tunnel's forward, reporting true so the bind is retried at once. Otherwise it
// describes the holders, e.g. "ssh pid 123 (different forward), node pid 9".
func (l *Listener) takeover() (holders string, took bool) {
	owners, err := findOwners(l.t.LocalPort)
	if err != nil {
		return "", false
	}
	var descs []string
	for _, o := range owners {
		if o.UID == os.Getuid() && !l.tookOver[o.PID] && portowner.SameForward(o.Args, l.t, l.m.SSHConfigFile()) {
			l.tookOver[o.PID] = true
			l.log.Warn("port held by an ssh -L with the same forward, taking over", "pid", o.PID, "args", strings.Join(o.Args, " "))
			terminate(o.PID)
			return "", true
		}
		d := o.String()
		if o.Command == "ssh" {
			d += " (different forward)"
		}
		descs = append(descs, d)
	}
	return strings.Join(descs, ", "), false
}

// terminate sends SIGTERM and waits up to 3s, then SIGKILL.
func terminate(pid int) {
	if syscall.Kill(pid, syscall.SIGTERM) != nil {
		return
	}
	for i := 0; i < 30; i++ {
		if syscall.Kill(pid, 0) != nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	syscall.Kill(pid, syscall.SIGKILL)
	time.Sleep(200 * time.Millisecond)
}

func (l *Listener) accept(s net.Listener) {
	defer l.wg.Done()
	for {
		c, err := s.Accept()
		if err != nil {
			select {
			case <-l.stop:
				return
			default:
			}
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			l.log.Error("accept failed", "err", err)
			return
		}
		l.mu.Lock()
		if l.stopped {
			l.mu.Unlock()
			c.Close()
			return
		}
		l.conns[c] = struct{}{}
		l.mu.Unlock()
		l.wg.Add(1)
		go func() {
			defer l.wg.Done()
			l.handle(c)
			l.mu.Lock()
			delete(l.conns, c)
			l.mu.Unlock()
		}()
	}
}

func (l *Listener) recordErr(msg string) {
	l.mu.Lock()
	l.lastErr = msg
	l.lastErrAt = time.Now()
	l.mu.Unlock()
}

// Stop closes the listening sockets and all active connections.
func (l *Listener) Stop() {
	l.mu.Lock()
	if l.stopped {
		l.mu.Unlock()
		return
	}
	l.stopped = true
	close(l.stop)
	for _, s := range l.sockets {
		s.Close()
	}
	for c := range l.conns {
		c.Close()
	}
	l.mu.Unlock()
	l.wg.Wait()
}

func (l *Listener) Status() ListenerStatus {
	l.mu.Lock()
	defer l.mu.Unlock()
	return ListenerStatus{Listen: l.listen, Conns: len(l.conns), LastError: l.lastErr, LastErrorAt: l.lastErrAt}
}

func (l *Listener) String() string {
	return fmt.Sprintf("%s(:%d -> %s via %s)", l.t.Label(), l.t.LocalPort, l.t.Remote, l.t.SSH)
}
