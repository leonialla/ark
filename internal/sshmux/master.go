// Package sshmux manages one OpenSSH ControlMaster connection per SSH target
// and opens `ssh -W` channels over it.
package sshmux

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

type State string

const (
	StateIdle       State = "idle"
	StateConnecting State = "connecting"
	StateUp         State = "up"
	StateError      State = "error"
)

// failureBackoff is how long a failed connect is reused instead of retried.
var failureBackoff = time.Second

type Options struct {
	SSHBinary      string
	SSHConfigFile  string
	SocketDir      string
	Target         string
	ConnectTimeout time.Duration
	Logger         *slog.Logger
}

type Status struct {
	Target    string
	State     State
	Since     time.Time
	LastError string
}

type Master struct {
	o    Options
	sock string
	log  *slog.Logger

	mu         sync.Mutex
	state      State
	since      time.Time
	lastErr    string
	proc       *masterProc
	connecting chan struct{}
	connectErr error
	failedAt   time.Time
	spawns     int
	frozen     bool
}

type masterProc struct {
	cmd    *exec.Cmd
	stderr *tailBuffer
	exited chan struct{}
	killed bool // set under Master.mu when we stop it on purpose
}

func New(o Options) *Master {
	log := o.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	sum := sha256.Sum256([]byte(o.Target))
	return &Master{
		o:     o,
		sock:  filepath.Join(o.SocketDir, hex.EncodeToString(sum[:])[:16]+".sock"),
		log:   log.With("ssh", o.Target),
		state: StateIdle,
		since: time.Now(),
	}
}

func (m *Master) setState(s State, errMsg string) {
	if s != m.state {
		m.since = time.Now()
	}
	m.state = s
	if errMsg != "" {
		m.lastErr = errMsg
	}
}

// Ensure makes sure the master connection is up, connecting if needed.
// Concurrent callers share a single connection attempt.
func (m *Master) Ensure(ctx context.Context) error {
	m.mu.Lock()
	if m.state == StateUp {
		m.mu.Unlock()
		return nil
	}
	if done := m.connecting; done != nil {
		m.mu.Unlock()
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.state == StateUp {
			return nil
		}
		return m.connectErr
	}
	if !m.failedAt.IsZero() && time.Since(m.failedAt) < failureBackoff {
		err := m.connectErr
		m.mu.Unlock()
		return err
	}
	done := make(chan struct{})
	m.connecting = done
	m.setState(StateConnecting, "")
	m.mu.Unlock()

	m.log.Info("connecting master")
	err := m.connect(ctx)

	m.mu.Lock()
	m.connecting = nil
	m.connectErr = err
	if err != nil {
		m.failedAt = time.Now()
		m.setState(StateError, err.Error())
	} else {
		m.failedAt = time.Time{}
		m.setState(StateUp, "")
	}
	close(done)
	m.mu.Unlock()

	if err != nil {
		m.log.Error("master connect failed", "err", err)
	} else {
		m.log.Info("master up", "pid", m.PID())
	}
	return err
}

func (m *Master) baseArgs() []string {
	var args []string
	if m.o.SSHConfigFile != "" {
		args = append(args, "-F", m.o.SSHConfigFile)
	}
	return append(args, "-S", m.sock)
}

func (m *Master) connect(ctx context.Context) error {
	if err := os.MkdirAll(m.o.SocketDir, 0o700); err != nil {
		return err
	}
	m.clearStaleSocket()

	secs := int(math.Ceil(m.o.ConnectTimeout.Seconds()))
	if secs < 1 {
		secs = 1
	}
	args := append(m.baseArgs(), "-N", "-M",
		"-o", "ControlPersist=no",
		"-o", "BatchMode=yes",
		"-o", fmt.Sprintf("ConnectTimeout=%d", secs),
		"-o", "ServerAliveInterval=10",
		"-o", "ServerAliveCountMax=2",
		"-o", "ExitOnForwardFailure=yes",
		m.o.Target)
	p := &masterProc{cmd: exec.Command(m.o.SSHBinary, args...), stderr: &tailBuffer{}, exited: make(chan struct{})}
	p.cmd.Stderr = p.stderr
	if err := p.cmd.Start(); err != nil {
		return fmt.Errorf("start ssh: %w", err)
	}
	m.mu.Lock()
	m.proc = p
	m.spawns++
	m.mu.Unlock()
	go m.wait(p)

	deadline := time.NewTimer(m.o.ConnectTimeout + time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-p.exited:
			if msg := p.stderr.Summary(); msg != "" {
				return errors.New(msg)
			}
			return fmt.Errorf("ssh exited: %v", p.cmd.ProcessState)
		case <-deadline.C:
			m.stop(p)
			return fmt.Errorf("timed out after %v waiting for ssh master", m.o.ConnectTimeout)
		case <-ctx.Done():
			m.stop(p)
			return ctx.Err()
		case <-tick.C:
			if m.check() {
				return nil
			}
		}
	}
}

// wait reaps the master process and records unexpected exits.
func (m *Master) wait(p *masterProc) {
	p.cmd.Wait()
	m.mu.Lock()
	unexpected := false
	if m.proc == p {
		if !m.frozen {
			m.proc = nil
		}
		if !p.killed && !m.frozen && m.state == StateUp {
			msg := "master exited"
			if s := p.stderr.Summary(); s != "" {
				msg += ": " + s
			}
			m.setState(StateError, msg)
			unexpected = true
		}
	}
	m.mu.Unlock()
	close(p.exited)
	if unexpected {
		m.log.Warn("master exited unexpectedly", "err", m.Status().LastError)
	}
}

func (m *Master) check() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	args := append(m.baseArgs(), "-O", "check", m.o.Target)
	return exec.CommandContext(ctx, m.o.SSHBinary, args...).Run() == nil
}

func (m *Master) clearStaleSocket() {
	if _, err := os.Lstat(m.sock); err != nil {
		return
	}
	if m.check() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		args := append(m.baseArgs(), "-O", "exit", m.o.Target)
		exec.CommandContext(ctx, m.o.SSHBinary, args...).Run()
		cancel()
	}
	os.Remove(m.sock)
}

// stop asks the process to exit via its control socket, then kills it.
func (m *Master) stop(p *masterProc) {
	m.mu.Lock()
	p.killed = true
	m.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	args := append(m.baseArgs(), "-O", "exit", m.o.Target)
	exec.CommandContext(ctx, m.o.SSHBinary, args...).Run()
	cancel()
	select {
	case <-p.exited:
	case <-time.After(time.Second):
		p.cmd.Process.Kill()
		<-p.exited
	}
	os.Remove(m.sock)
}

// Dial opens a channel to remote (host:port as seen from the server) over the master.
// It does not connect the master; call Ensure first.
func (m *Master) Dial(ctx context.Context, remote string) (*Channel, error) {
	args := append(m.baseArgs(),
		"-o", "ControlMaster=no",
		"-o", "BatchMode=yes",
		// If the master is gone, fail instead of silently opening a direct connection.
		"-o", "ProxyCommand=false",
		"-W", remote, m.o.Target)
	return startChannel(exec.CommandContext(ctx, m.o.SSHBinary, args...))
}

// Reset tears down the master connection; the next Ensure reconnects.
func (m *Master) Reset(reason string) {
	m.mu.Lock()
	p := m.proc
	m.proc = nil
	m.failedAt = time.Time{}
	m.setState(StateIdle, "")
	m.mu.Unlock()
	if p == nil {
		return
	}
	m.log.Info("resetting master", "reason", reason)
	m.stop(p)
}

// Alive reports whether the master process is running and answers on its control socket.
func (m *Master) Alive() bool {
	m.mu.Lock()
	p := m.proc
	m.mu.Unlock()
	if p == nil {
		return false
	}
	select {
	case <-p.exited:
		return false
	default:
	}
	return m.check()
}

func (m *Master) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return Status{Target: m.o.Target, State: m.state, Since: m.since, LastError: m.lastErr}
}

// PID of the running master process, or 0.
func (m *Master) PID() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.proc == nil || m.proc.cmd.Process == nil {
		return 0
	}
	return m.proc.cmd.Process.Pid
}

// Spawns is the number of master processes started so far.
func (m *Master) Spawns() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.spawns
}

// FreezeStateForTest makes process exits leave the state untouched, so a dead
// master keeps reporting "up". Test hook only.
func (m *Master) FreezeStateForTest(frozen bool) {
	m.mu.Lock()
	m.frozen = frozen
	m.mu.Unlock()
}

// Close stops the master and removes its control socket.
func (m *Master) Close() {
	m.Reset("closing")
	os.Remove(m.sock)
}
