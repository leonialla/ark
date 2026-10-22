package netwatch

import (
	"context"
	"net"
	"os/exec"
	"time"
)

// takeSample reads the current clocks and the physical IPv4 default-route
// interface. With a VPN up there are several default routes; the VPN's is skipped.
func takeSample(ctx context.Context) Sample {
	now := time.Now()
	s := Sample{Wall: now.Round(0), Mono: now}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/usr/sbin/netstat", "-rn", "-f", "inet").Output()
	if err != nil {
		return s
	}
	iface := ParseNetstatDefault(string(out))
	if iface == "" {
		return s
	}
	s.Iface = iface
	if ifi, err := net.InterfaceByName(iface); err == nil {
		if addrs, err := ifi.Addrs(); err == nil {
			for _, a := range addrs {
				if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil {
					s.IPv4 = ipn.IP.String()
					break
				}
			}
		}
	}
	return s
}
