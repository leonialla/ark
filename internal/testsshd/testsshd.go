// Package testsshd runs a throwaway, unprivileged sshd on 127.0.0.1 for
// integration tests, together with an ssh client config that reaches it.
package testsshd

import (
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"testing"
	"time"
)

const sshdPath = "/usr/sbin/sshd"

// Server is a running sshd. ClientConfig is an ssh_config file (for ssh -F)
// defining the Host aliases "testhost", "jumped" (ProxyJump via testhost),
// "badport" (nothing listening), "forwarding" (like testhost, plus a
// LocalForward on ForwardPort and ForkAfterAuthentication yes), and a
// "127.0.0.1" block so host:port destinations reach the server.
type Server struct {
	Port         int
	ClientConfig string
	ForwardPort  int

	dir     string
	sshdCfg string
	cmd     *exec.Cmd
}

func Start(t testing.TB) *Server {
	t.Helper()
	if _, err := os.Stat(sshdPath); err != nil {
		t.Skip("sshd not available")
	}
	dir := t.TempDir()
	for _, k := range []string{"host_key", "client_key"} {
		out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", filepath.Join(dir, k)).CombinedOutput()
		if err != nil {
			t.Fatalf("ssh-keygen: %v: %s", err, out)
		}
	}
	pub, err := os.ReadFile(filepath.Join(dir, "client_key.pub"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "authorized_keys"), pub, 0o600); err != nil {
		t.Fatal(err)
	}
	u, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Port: FreePort(t), ForwardPort: FreePort(t), dir: dir, sshdCfg: filepath.Join(dir, "sshd_config"), ClientConfig: filepath.Join(dir, "ssh_config")}

	sshdCfg := fmt.Sprintf(`Port %d
ListenAddress 127.0.0.1
HostKey %s/host_key
PidFile %s/sshd.pid
AuthorizedKeysFile %s/authorized_keys
UsePAM no
StrictModes no
PasswordAuthentication no
KbdInteractiveAuthentication no
PubkeyAuthentication yes
AllowTcpForwarding yes
LogLevel ERROR
`, s.Port, dir, dir, dir)
	common := fmt.Sprintf(`    HostName 127.0.0.1
    User %s
    IdentityFile %s/client_key
    IdentitiesOnly yes
    IdentityAgent none
    StrictHostKeyChecking no
    UserKnownHostsFile /dev/null
    LogLevel ERROR
`, u.Username, dir)
	clientCfg := fmt.Sprintf("Host testhost\n    Port %d\n%s\nHost jumped\n    Port %d\n    ProxyJump testhost\n%s\nHost badport\n    Port %d\n%s"+
		"\nHost forwarding\n    Port %d\n    LocalForward 127.0.0.1:%d 127.0.0.1:1\n    ForkAfterAuthentication yes\n%s\nHost 127.0.0.1\n%s",
		s.Port, common, s.Port, common, FreePort(t), common, s.Port, s.ForwardPort, common, common)
	if err := os.WriteFile(s.sshdCfg, []byte(sshdCfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.ClientConfig, []byte(clientCfg), 0o600); err != nil {
		t.Fatal(err)
	}
	s.start(t)
	t.Cleanup(s.Stop)
	return s
}

func (s *Server) start(t testing.TB) {
	t.Helper()
	s.cmd = exec.Command(sshdPath, "-D", "-e", "-f", s.sshdCfg)
	s.cmd.Stderr = io.Discard // macOS sshd prints BSM audit noise when unprivileged
	if err := s.cmd.Start(); err != nil {
		t.Fatalf("start sshd: %v", err)
	}
	addr := fmt.Sprintf("127.0.0.1:%d", s.Port)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.Dial("tcp", addr); err == nil {
			c.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("sshd did not start listening on %s", addr)
}

// Stop kills sshd (connections it already accepted are served by child processes and also end).
func (s *Server) Stop() {
	if s.cmd == nil || s.cmd.Process == nil {
		return
	}
	s.cmd.Process.Kill()
	s.cmd.Wait()
	s.cmd = nil
}

// Restart stops sshd and starts it again on the same port.
func (s *Server) Restart(t testing.TB) {
	t.Helper()
	s.Stop()
	s.start(t)
}

// FreePort returns a TCP port that was free on 127.0.0.1 a moment ago.
func FreePort(t testing.TB) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// EchoServer starts a TCP echo server on 127.0.0.1 and returns its address.
func EchoServer(t testing.TB) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				io.Copy(c, c)
			}()
		}
	}()
	return l.Addr().String()
}

// ShortTempDir returns a temp dir with a short path, for unix sockets (macOS limit is 104 bytes).
func ShortTempDir(t testing.TB) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "stdd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}
