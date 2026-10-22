package proxy

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"ssh-tunnel-daemon/internal/sshmux"
)

const (
	replayMax = 64 << 10
	// A channel that dies this quickly without sending anything may have hit a dead master.
	earlyExit = 2 * time.Second
)

// ensureTimeout bounds waiting for the master; Ensure is itself bounded by connect_timeout.
var ensureTimeout = 2 * time.Minute

// relay copies between a client connection and an ssh channel. Until the remote
// side sends its first byte, client bytes are kept so the connection can be moved
// to a fresh channel once if the first one died on a dead master.
type relay struct {
	client net.Conn

	mu        sync.Mutex
	ch        *sshmux.Channel
	replay    []byte
	overflow  bool
	gotRemote bool
	clientEOF bool
	// clientDone is set when pumpClient has exited (EOF or error).
	clientDone bool
}

func (l *Listener) handle(c net.Conn) {
	defer c.Close()
	// Channels live as long as the connection; only Stop cancels them.
	chanCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-l.stop:
			cancel()
		case <-chanCtx.Done():
		}
	}()

	ch, ok := l.open(chanCtx)
	if !ok {
		return
	}
	l.log.Debug("connection open", "client", c.RemoteAddr())
	r := &relay{client: c, ch: ch}
	go r.pumpClient()

	for attempt := 0; ; attempt++ {
		started := time.Now()
		n, clientGone := r.pumpRemote(ch)
		if clientGone {
			ch.Close()
		}
		chErr := ch.Wait()
		ch.Close()
		quick := n == 0 && !clientGone && time.Since(started) < earlyExit
		if quick && attempt == 0 && r.canReplay() && l.m.GenDead(ch.Gen) {
			l.log.Warn("channel failed on dead master, reconnecting", "err", chErr)
			l.m.ResetIf(ch.Gen, "channel failed on dead master")
			if ch, ok = l.open(chanCtx); !ok {
				return
			}
			if !r.swap(ch) {
				ch.Close()
				return
			}
			continue
		}
		if n == 0 && chErr != nil && !clientGone {
			l.recordErr(chErr.Error())
			l.log.Warn("channel failed", "err", chErr)
		} else {
			l.log.Debug("connection closed", "client", c.RemoteAddr(), "bytes_from_remote", n)
		}
		return
	}
}

// open ensures the master is up and starts a channel bound to chanCtx.
func (l *Listener) open(chanCtx context.Context) (*sshmux.Channel, bool) {
	ctx, cancel := context.WithTimeout(chanCtx, ensureTimeout)
	err := l.m.Ensure(ctx)
	cancel()
	if err != nil {
		l.recordErr("ssh connect failed: " + err.Error())
		l.log.Error("ssh connect failed, closing client", "err", err)
		return nil, false
	}
	ch, err := l.m.Dial(chanCtx, l.t.Remote)
	if err != nil {
		l.recordErr("channel: " + err.Error())
		l.log.Error("start channel failed", "err", err)
		return nil, false
	}
	return ch, true
}

// pumpRemote copies channel -> client. It returns the bytes copied and whether
// it stopped because writing to the client failed.
func (r *relay) pumpRemote(ch *sshmux.Channel) (int64, bool) {
	buf := make([]byte, 32<<10)
	var total int64
	for {
		n, err := ch.Read(buf)
		if n > 0 {
			if total == 0 {
				r.mu.Lock()
				r.gotRemote = true
				r.replay = nil
				r.mu.Unlock()
			}
			total += int64(n)
			if _, werr := r.client.Write(buf[:n]); werr != nil {
				return total, true
			}
		}
		if err != nil {
			if total > 0 {
				if tc, ok := r.client.(*net.TCPConn); ok {
					tc.CloseWrite()
				}
			}
			return total, false
		}
	}
}

// pumpClient copies client -> current channel until the client closes.
func (r *relay) pumpClient() {
	buf := make([]byte, 32<<10)
	for {
		n, err := r.client.Read(buf)
		if n > 0 {
			r.mu.Lock()
			if !r.gotRemote && !r.overflow {
				if len(r.replay)+n > replayMax {
					r.overflow, r.replay = true, nil
				} else {
					r.replay = append(r.replay, buf[:n]...)
				}
			}
			// A write error means the channel died; the main loop decides what happens next.
			r.ch.Write(buf[:n])
			r.mu.Unlock()
		}
		if err != nil {
			r.mu.Lock()
			r.clientDone = true
			if errors.Is(err, io.EOF) {
				r.clientEOF = true
				r.ch.CloseWrite()
			} else {
				r.ch.Close()
			}
			r.mu.Unlock()
			return
		}
	}
}

func (r *relay) canReplay() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return !r.gotRemote && !r.overflow
}

// swap moves the relay to ch and replays what the client has sent so far.
// It returns false if the client has already gone away with an error.
func (r *relay) swap(ch *sshmux.Channel) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.clientDone && !r.clientEOF {
		return false
	}
	r.ch = ch
	if len(r.replay) > 0 {
		ch.Write(r.replay)
	}
	if r.clientEOF {
		ch.CloseWrite()
	}
	return true
}
