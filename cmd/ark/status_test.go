package main

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"time"

	"ark/internal/daemon"
)

func TestRenderTable(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	rows := []daemon.TunnelStatus{
		{Name: "prod-mysql", LocalPort: 4333, SSH: "ubuntu@203.0.113.180", Remote: "localhost:4333",
			Listen: "ok", State: "up", Conns: 2, Since: now.Add(-5 * time.Minute)},
		{Name: "prod-redis", LocalPort: 6379, SSH: "prod-redis", Remote: "10.0.1.5:6379",
			Listen: "ok", State: "error", Since: now.Add(-30 * time.Second),
			LastError: "ssh: connect to host 10.0.0.1 port 22: Operation timed out after waiting a very long time indeed"},
	}
	var buf bytes.Buffer
	renderTable(&buf, rows, now)
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("lines = %d:\n%s", len(lines), buf.String())
	}
	want := []string{"NAME", "PORT", "SSH", "REMOTE", "LISTEN", "STATE", "CONNS", "SINCE", "LAST", "ERROR"}
	if got := strings.Fields(lines[0]); !reflect.DeepEqual(got, want) {
		t.Fatalf("header = %v", got)
	}
	if !strings.Contains(lines[1], "5m ago") || !strings.Contains(lines[2], "30s ago") {
		t.Fatalf("since not relative:\n%s", buf.String())
	}
	if !strings.HasSuffix(lines[1], "-") {
		t.Fatalf("empty error should render as -: %q", lines[1])
	}
	if !strings.Contains(lines[2], "…") || strings.Contains(lines[2], "indeed") {
		t.Fatalf("error not truncated: %q", lines[2])
	}
	// columns are aligned: "STATE" starts at the same offset as "up"
	if strings.Index(lines[0], "STATE") != strings.Index(lines[1], "up ") {
		t.Fatalf("not aligned:\n%s", buf.String())
	}
}

func TestAge(t *testing.T) {
	now := time.Now()
	cases := map[time.Duration]string{
		10 * time.Second: "10s ago",
		3 * time.Minute:  "3m ago",
		2 * time.Hour:    "2h ago",
		50 * time.Hour:   "2d ago",
	}
	for d, want := range cases {
		if got := age(now.Add(-d), now); got != want {
			t.Errorf("age(%v) = %q, want %q", d, got, want)
		}
	}
	if got := age(time.Time{}, now); got != "-" {
		t.Errorf("zero = %q", got)
	}
}
