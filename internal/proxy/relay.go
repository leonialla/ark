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
	// Upper bound on waiting for the master; Ensure is itself bounded by connect_timeout.
	ensureTimeout = 2 * time.Minute
)

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
}

func (l *Listener) handle(c net.Conn) {
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), ensureTimeout)
	defer cancel()
	go func() {
		select {
		case <-l.stop:
			cancel()
		case <-ctx.Done():
		}
	}()

	if err := l.m.Ensure(ctx); err != nil {
		l.recordErr("ssh connect failed: " + err.Error())
		l.log.Error("ssh connect failed, closing client", "err", err)
		return
	}
	ch, err := l.m.Dial(ctx, l.t.Remote)
	if err != nil {
		l.recordErr("channel: " + err.Error())
		l.log.Error("start channel failed", "err", err)
		return
	}
	l.log.Debug("connection open", "client", c.RemoteAddr())
	r := &relay{client: c, ch: ch}
	go r.pumpClient()

	for attempt := 0; ; attempt++ {
		started := time.Now()
		n := r.pumpRemote(ch)
		chErr := ch.Wait()
		ch.Close()
		quick := n == 0 && time.Since(started) < earlyExit
		if quick && attempt == 0 && r.canReplay() && !l.m.Alive() {
			l.log.Warn("channel failed on dead master, reconnecting", "err", chErr)
			l.m.Reset("channel failed on dead master")
			if err := l.m.Ensure(ctx); err != nil {
				l.recordErr("ssh connect failed: " + err.Error())
				l.log.Error("ssh reconnect failed, closing client", "err", err)
				return
			}
			if ch, err = l.m.Dial(ctx, l.t.Remote); err != nil {
				l.recordErr("channel: " + err.Error())
				return
			}
			r.swap(ch)
			continue
		}
		if n == 0 && chErr != nil {
			l.recordErr(chErr.Error())
			l.log.Warn("channel failed", "err", chErr)
		} else {
			l.log.Debug("connection closed", "client", c.RemoteAddr(), "bytes_from_remote", n)
		}
		return
	}
}

// pumpRemote copies channel -> client and returns the bytes copied.
func (r *relay) pumpRemote(ch *sshmux.Channel) int64 {
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
				return total
			}
		}
		if err != nil {
			if total > 0 {
				if tc, ok := r.client.(*net.TCPConn); ok {
					tc.CloseWrite()
				}
			}
			return total
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
func (r *relay) swap(ch *sshmux.Channel) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ch = ch
	if len(r.replay) > 0 {
		ch.Write(r.replay)
	}
	if r.clientEOF {
		ch.CloseWrite()
	}
}
