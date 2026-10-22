package sshmux

import (
	"net"
	"strconv"
	"strings"
)

// sshDestination turns "[user@]host:port" into the "ssh://[user@]host:port" form,
// since ssh does not parse a port in a plain destination. Aliases, plain hosts,
// bare IPv6 addresses and URIs are returned unchanged.
func sshDestination(target string) string {
	if strings.Contains(target, "://") {
		return target
	}
	hostPort := target
	if i := strings.LastIndex(target, "@"); i >= 0 {
		hostPort = target[i+1:]
	}
	host, port, err := net.SplitHostPort(hostPort)
	if err != nil || host == "" {
		return target
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return target
	}
	return "ssh://" + target
}
