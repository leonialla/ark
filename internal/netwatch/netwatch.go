// Package netwatch detects wake from sleep and changes of the primary
// network interface, the two events after which SSH connections are dead.
package netwatch

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

type EventKind string

const (
	Wake           EventKind = "wake"
	PrimaryChanged EventKind = "primary-changed"
)

type Event struct {
	Kind   EventKind
	Detail string
}

// Sample is one observation. Wall must carry no monotonic reading (t.Round(0));
// Mono must (time.Now()), so their elapsed times differ by the time spent asleep.
type Sample struct {
	Wall  time.Time
	Mono  time.Time
	Iface string
	IPv4  string
}

// wakeGap is how far wall-clock time may run ahead of monotonic time before we call it a wake.
const wakeGap = 30 * time.Second

var virtualPrefixes = []string{"utun", "ipsec", "ppp", "bridge", "vmenet", "gif", "stf", "awdl", "llw"}

// IsVirtual reports whether iface is a VPN, VM or other virtual interface we ignore.
func IsVirtual(iface string) bool {
	for _, p := range virtualPrefixes {
		if strings.HasPrefix(iface, p) {
			return true
		}
	}
	return false
}

// Detector turns successive samples into events. The zero value is ready to use.
type Detector struct {
	prev         *Sample
	iface, ip    string
	knownPrimary bool
}

func (d *Detector) Observe(s Sample) []Event {
	var evs []Event
	if d.prev != nil {
		wall := s.Wall.Sub(d.prev.Wall)
		mono := s.Mono.Sub(d.prev.Mono)
		if wall-mono > wakeGap {
			evs = append(evs, Event{Kind: Wake, Detail: fmt.Sprintf("slept ~%s", (wall - mono).Round(time.Second))})
		}
	}
	d.prev = &s

	if s.Iface == "" || s.IPv4 == "" || IsVirtual(s.Iface) {
		return evs
	}
	if d.knownPrimary && (s.Iface != d.iface || s.IPv4 != d.ip) {
		evs = append(evs, Event{Kind: PrimaryChanged, Detail: fmt.Sprintf("%s/%s -> %s/%s", d.iface, d.ip, s.Iface, s.IPv4)})
	}
	d.iface, d.ip, d.knownPrimary = s.Iface, s.IPv4, true
	return evs
}

// ParseNetstatDefault returns the interface of the first IPv4 default route
// in `netstat -rn -f inet` output that is not a virtual (VPN/VM) interface.
func ParseNetstatDefault(out string) string {
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) >= 4 && f[0] == "default" && !IsVirtual(f[3]) {
			return f[3]
		}
	}
	return ""
}

// Watch samples every interval and emits wake / primary-change events until ctx ends.
func Watch(ctx context.Context, interval time.Duration, log *slog.Logger) <-chan Event {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	out := make(chan Event, 8)
	go func() {
		defer close(out)
		var d Detector
		d.Observe(takeSample(ctx))
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			for _, e := range d.Observe(takeSample(ctx)) {
				log.Info("network event", "kind", e.Kind, "detail", e.Detail)
				select {
				case out <- e:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out
}
