package control

import (
	"net"
	"testing"
)

// writeStale leaves a unix socket file at path with no listener behind it.
func writeStale(t *testing.T, path string) {
	t.Helper()
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	l.Close()
}
