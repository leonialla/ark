// Package portowner finds which process holds a local TCP port and decides
// whether it is an ssh -L forward identical to a configured tunnel.
package portowner

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"ark/internal/config"
)

type Owner struct {
	PID     int
	Command string // short process name as lsof reports it
	UID     int
	Args    []string // full argv, nil if unreadable
}

func (o Owner) String() string { return fmt.Sprintf("%s pid %d", o.Command, o.PID) }

// Find returns the processes listening on TCP port, with their argv.
func Find(port int) ([]Owner, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/usr/sbin/lsof", "-nP", fmt.Sprintf("-iTCP:%d", port), "-sTCP:LISTEN", "-Fpcu").Output()
	// lsof exits 1 when nothing matches.
	if err != nil && len(out) == 0 {
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			return nil, nil
		}
		return nil, err
	}
	owners := parseLsof(string(out))
	for i := range owners {
		owners[i].Args, _ = procArgs(owners[i].PID)
	}
	return owners, nil
}

func parseLsof(out string) []Owner {
	var owners []Owner
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		v := line[1:]
		switch line[0] {
		case 'p':
			pid, _ := strconv.Atoi(v)
			owners = append(owners, Owner{PID: pid})
		case 'c':
			if len(owners) > 0 {
				owners[len(owners)-1].Command = v
			}
		case 'u':
			if len(owners) > 0 {
				owners[len(owners)-1].UID, _ = strconv.Atoi(v)
			}
		}
	}
	return owners
}

// procArgs reads a process's argv via the kern.procargs2 sysctl.
func procArgs(pid int) ([]string, error) {
	buf, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return nil, err
	}
	if len(buf) < 4 {
		return nil, fmt.Errorf("short procargs")
	}
	argc := int(binary.LittleEndian.Uint32(buf[:4]))
	rest := buf[4:]
	// Skip the executable path and the NUL padding after it.
	i := bytes.IndexByte(rest, 0)
	if i < 0 {
		return nil, fmt.Errorf("malformed procargs")
	}
	rest = rest[i:]
	for len(rest) > 0 && rest[0] == 0 {
		rest = rest[1:]
	}
	args := make([]string, 0, argc)
	for len(args) < argc && len(rest) > 0 {
		j := bytes.IndexByte(rest, 0)
		if j < 0 {
			j = len(rest)
		}
		args = append(args, string(rest[:j]))
		if j == len(rest) {
			break
		}
		rest = rest[j+1:]
	}
	return args, nil
}

// ssh options that take an argument.
const sshArgOpts = "BbcDEeFIiJLlmOoPpQRSWw"

// -o keys that change where the connection goes; their presence means we cannot vouch for it.
var redirectingOptions = map[string]bool{"hostname": true, "port": true, "user": true, "proxyjump": true, "proxycommand": true}

// SameForward reports whether args (an ssh argv) forwards t.LocalPort on loopback to
// exactly t.Remote via exactly t.SSH. sshConfigFile is the daemon's -F file ("" for none);
// a different -F could give the alias another meaning. Anything unclear is "no".
func SameForward(args []string, t config.Tunnel, sshConfigFile string) bool {
	if len(args) == 0 || filepath.Base(args[0]) != "ssh" {
		return false
	}
	var forwards []string
	var dest, loginUser, port, cfgFile string
	for i := 1; i < len(args); i++ {
		a := args[i]
		if len(a) < 2 || a[0] != '-' {
			if dest != "" {
				break // remote command follows
			}
			dest = a
			continue
		}
		for j := 1; j < len(a); j++ {
			c := a[j]
			if strings.IndexByte(sshArgOpts, c) < 0 {
				continue
			}
			val := a[j+1:]
			if val == "" && i+1 < len(args) {
				i++
				val = args[i]
			}
			switch c {
			case 'L':
				forwards = append(forwards, val)
			case 'l':
				loginUser = val
			case 'p':
				port = val
			case 'F':
				cfgFile = val
			case 'J':
				return false
			case 'o':
				key, _, _ := strings.Cut(strings.TrimSpace(val), "=")
				key, _, _ = strings.Cut(key, " ")
				if redirectingOptions[strings.ToLower(key)] {
					return false
				}
			}
			break
		}
	}
	if cfgFile != sshConfigFile || dest == "" {
		return false
	}

	wantUser, wantHost, wantPort := splitTarget(t.SSH)
	user, host, destPort := splitTarget(dest)
	if user == "" {
		user = loginUser
	}
	if destPort == "22" && port != "" {
		destPort = port
	}
	if user != wantUser || !strings.EqualFold(host, wantHost) || destPort != wantPort {
		return false
	}

	rHost, rPort, err := net.SplitHostPort(t.Remote)
	if err != nil {
		return false
	}
	for _, f := range forwards {
		parts := splitColons(f)
		bind := ""
		if len(parts) == 4 {
			bind, parts = trimBrackets(parts[0]), parts[1:]
		}
		if len(parts) != 3 {
			continue
		}
		if bind != "" && bind != "localhost" && bind != "127.0.0.1" && bind != "::1" {
			continue
		}
		if parts[0] == strconv.Itoa(t.LocalPort) && strings.EqualFold(trimBrackets(parts[1]), rHost) && parts[2] == rPort {
			return true
		}
	}
	return false
}

// splitTarget parses [ssh://][user@]host[:port]; port defaults to "22".
func splitTarget(s string) (user, host, port string) {
	s = strings.TrimPrefix(s, "ssh://")
	if i := strings.LastIndex(s, "@"); i >= 0 {
		user, s = s[:i], s[i+1:]
	}
	host, port = s, "22"
	if h, p, err := net.SplitHostPort(s); err == nil {
		if _, err := strconv.Atoi(p); err == nil {
			host, port = h, p
		}
	}
	return user, trimBrackets(host), port
}

// splitColons splits an -L spec on ':' outside [brackets].
func splitColons(s string) []string {
	var parts []string
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '[':
			depth++
		case ']':
			depth--
		case ':':
			if depth == 0 {
				parts = append(parts, s[start:i])
				start = i + 1
			}
		}
	}
	return append(parts, s[start:])
}

func trimBrackets(s string) string { return strings.TrimSuffix(strings.TrimPrefix(s, "["), "]") }
