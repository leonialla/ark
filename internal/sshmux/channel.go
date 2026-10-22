package sshmux

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Channel is one `ssh -W` stdio forward multiplexed over a master connection.
// Reads come from the remote end, writes go to it.
type Channel struct {
	cmd    *exec.Cmd
	stdin  *os.File
	stdout *os.File
	stderr *tailBuffer
	done   chan struct{}
	err    error

	closeOnce sync.Once
}

func startChannel(cmd *exec.Cmd) (*Channel, error) {
	inR, inW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		inR.Close()
		inW.Close()
		return nil, err
	}
	c := &Channel{cmd: cmd, stdin: inW, stdout: outR, stderr: &tailBuffer{}, done: make(chan struct{})}
	// Plain *os.File pipes so that cmd.Wait does not close our ends while we still read.
	cmd.Stdin, cmd.Stdout, cmd.Stderr = inR, outW, c.stderr
	err = cmd.Start()
	inR.Close()
	outW.Close()
	if err != nil {
		inW.Close()
		outR.Close()
		return nil, err
	}
	go func() {
		if err := cmd.Wait(); err != nil {
			if msg := c.stderr.Summary(); msg != "" {
				err = fmt.Errorf("%w: %s", err, msg)
			}
			c.err = err
		}
		close(c.done)
	}()
	return c, nil
}

func (c *Channel) Read(p []byte) (int, error)  { return c.stdout.Read(p) }
func (c *Channel) Write(p []byte) (int, error) { return c.stdin.Write(p) }

// CloseWrite signals EOF to the remote end.
func (c *Channel) CloseWrite() error { return c.stdin.Close() }

// Close ends the channel, killing the ssh process if it does not exit within 1s.
func (c *Channel) Close() error {
	c.closeOnce.Do(func() {
		c.stdin.Close()
		select {
		case <-c.done:
		case <-time.After(time.Second):
			c.cmd.Process.Kill()
			<-c.done
		}
		c.stdout.Close()
	})
	return nil
}

// Done is closed when the ssh process has exited.
func (c *Channel) Done() <-chan struct{} { return c.done }

// Wait blocks until the ssh process exits and returns its error, including stderr.
func (c *Channel) Wait() error {
	<-c.done
	return c.err
}

// Stderr is the tail of the ssh process's stderr.
func (c *Channel) Stderr() string { return c.stderr.Summary() }

// tailBuffer keeps the last 4KB written to it.
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
}

const tailMax = 4096

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	if len(b.buf) > tailMax {
		b.buf = b.buf[len(b.buf)-tailMax:]
	}
	return len(p), nil
}

// Summary returns the last few non-empty lines joined with "; ".
func (b *tailBuffer) Summary() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var lines []string
	for _, l := range strings.Split(string(b.buf), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) > 3 {
		lines = lines[len(lines)-3:]
	}
	return strings.Join(lines, "; ")
}
