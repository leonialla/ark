package main

import (
	"bytes"
	"errors"
	"fmt"
	"testing"

	"github.com/leonialla/ark/internal/control"
)

func TestRenderVersion(t *testing.T) {
	cases := []struct {
		name      string
		daemon    string
		daemonErr error
		want      string
	}{
		{"same version", "v0.2.0", nil, "ark   v0.2.0\narkd  v0.2.0\n"},
		{"daemon not running", "", fmt.Errorf("%w (no control socket)", control.ErrNotRunning), "ark   v0.2.0\narkd  not running\n"},
		{"daemon unreachable", "", errors.New("boom"), "ark   v0.2.0\narkd  unknown (boom)\n"},
		{"daemon predates version support", "", fmt.Errorf("%w: HTTP 404", control.ErrNoVersion), "ark   v0.2.0\narkd  older than this ark (no version info)\n\narkd is running a different version; run `ark install` to restart it with this one.\n"},
		{"daemon older", "v0.1.0", nil, "ark   v0.2.0\narkd  v0.1.0\n\narkd is running a different version; run `ark install` to restart it with this one.\n"},
	}
	for _, c := range cases {
		var buf bytes.Buffer
		renderVersion(&buf, "v0.2.0", c.daemon, c.daemonErr)
		if got := buf.String(); got != c.want {
			t.Errorf("%s:\ngot  %q\nwant %q", c.name, got, c.want)
		}
	}
}
