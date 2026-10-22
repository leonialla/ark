package logging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewWritesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "arkd.log")
	log, closer, err := New(path, false)
	if err != nil {
		t.Fatal(err)
	}
	log.Info("hello", "k", "v")
	log.Debug("hidden")
	closer.Close()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if !strings.Contains(s, "hello") || !strings.Contains(s, "k=v") || strings.Contains(s, "hidden") {
		t.Fatalf("log = %q", s)
	}
}

func TestNewDebug(t *testing.T) {
	path := filepath.Join(t.TempDir(), "arkd.log")
	log, closer, err := New(path, true)
	if err != nil {
		t.Fatal(err)
	}
	log.Debug("shown")
	closer.Close()
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "shown") {
		t.Fatalf("log = %q", data)
	}
}
