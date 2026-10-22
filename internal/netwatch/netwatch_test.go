package netwatch

import (
	"context"
	"os"
	"testing"
	"time"
)

var t0 = time.Unix(1_700_000_000, 0)

func sample(mono, wall time.Duration, iface, ip string) Sample {
	return Sample{Wall: t0.Add(wall), Mono: t0.Add(mono), Iface: iface, IPv4: ip}
}

func kinds(evs []Event) []EventKind {
	var out []EventKind
	for _, e := range evs {
		out = append(out, e.Kind)
	}
	return out
}

func expect(t *testing.T, got []Event, want ...EventKind) {
	t.Helper()
	g := kinds(got)
	if len(g) != len(want) {
		t.Fatalf("events = %v, want %v", g, want)
	}
	for i := range g {
		if g[i] != want[i] {
			t.Fatalf("events = %v, want %v", g, want)
		}
	}
}

func TestWakeDetected(t *testing.T) {
	var d Detector
	expect(t, d.Observe(sample(0, 0, "en0", "192.168.1.5")))
	expect(t, d.Observe(sample(3*time.Second, 3*time.Second, "en0", "192.168.1.5")))
	expect(t, d.Observe(sample(6*time.Second, 43*time.Second, "en0", "192.168.1.5")), Wake)
}

func TestSmallDriftIgnored(t *testing.T) {
	var d Detector
	d.Observe(sample(0, 0, "en0", "192.168.1.5"))
	expect(t, d.Observe(sample(3*time.Second, 13*time.Second, "en0", "192.168.1.5")))
}

func TestPrimaryChange(t *testing.T) {
	var d Detector
	d.Observe(sample(0, 0, "en0", "192.168.1.5"))
	expect(t, d.Observe(sample(3*time.Second, 3*time.Second, "en0", "10.0.0.8")), PrimaryChanged)
	expect(t, d.Observe(sample(6*time.Second, 6*time.Second, "en7", "10.0.0.8")), PrimaryChanged)
	expect(t, d.Observe(sample(9*time.Second, 9*time.Second, "en7", "10.0.0.8")))
}

func TestVirtualIgnored(t *testing.T) {
	var d Detector
	d.Observe(sample(0, 0, "en0", "192.168.1.5"))
	expect(t, d.Observe(sample(3*time.Second, 3*time.Second, "utun4", "10.8.0.2")))
	expect(t, d.Observe(sample(6*time.Second, 6*time.Second, "bridge100", "192.168.64.1")))
	expect(t, d.Observe(sample(9*time.Second, 9*time.Second, "en0", "192.168.1.5")))
}

func TestEmptySampleIgnored(t *testing.T) {
	var d Detector
	d.Observe(sample(0, 0, "en0", "192.168.1.5"))
	expect(t, d.Observe(sample(3*time.Second, 3*time.Second, "", "")))
	expect(t, d.Observe(sample(6*time.Second, 6*time.Second, "en0", "")))
	expect(t, d.Observe(sample(9*time.Second, 9*time.Second, "en0", "172.20.1.3")), PrimaryChanged)
}

func TestWakeAndChangeTogether(t *testing.T) {
	var d Detector
	d.Observe(sample(0, 0, "en0", "192.168.1.5"))
	expect(t, d.Observe(sample(3*time.Second, time.Hour, "en0", "10.1.1.1")), Wake, PrimaryChanged)
}

func TestIsVirtual(t *testing.T) {
	for _, n := range []string{"utun0", "ipsec1", "ppp0", "bridge100", "vmenet0", "gif0", "stf0", "awdl0", "llw0"} {
		if !IsVirtual(n) {
			t.Errorf("IsVirtual(%q) = false", n)
		}
	}
	for _, n := range []string{"en0", "en7"} {
		if IsVirtual(n) {
			t.Errorf("IsVirtual(%q) = true", n)
		}
	}
}

func TestWatchLive(t *testing.T) {
	if os.Getenv("NETWATCH_LIVE") == "" {
		t.Skip("set NETWATCH_LIVE=1")
	}
	s := takeSample(context.Background())
	t.Logf("live sample: iface=%q ipv4=%q", s.Iface, s.IPv4)
	if s.Iface == "" {
		t.Fatal("no primary interface found")
	}
}

func TestParseNetstatDefaultSkipsVPN(t *testing.T) {
	out := `Routing tables

Internet:
Destination        Gateway            Flags               Netif Expire
default            link#25            UCSg                utun6
default            192.168.1.1        UGScIg                en0
198.51.100.66      link#25            UHW3Ig              utun6      1
`
	if got := ParseNetstatDefault(out); got != "en0" {
		t.Fatalf("got %q, want en0", got)
	}
	if got := ParseNetstatDefault("Internet:\ndefault  link#25  UCSg  utun6\n"); got != "" {
		t.Fatalf("vpn-only: got %q", got)
	}
}
