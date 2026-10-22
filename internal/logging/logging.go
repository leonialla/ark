// Package logging builds the daemon's slog logger, writing to a rotated file.
package logging

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"gopkg.in/natefinch/lumberjack.v2"
)

// DefaultPath is ~/Library/Logs/ssh-tunnel-daemon/daemon.log.
func DefaultPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library/Logs/ssh-tunnel-daemon/daemon.log")
}

type nopCloser struct{}

func (nopCloser) Close() error { return nil }

// New returns a text logger writing to path (10MB x 3 rotation), or to stderr when path is "".
func New(path string, debug bool) (*slog.Logger, io.Closer, error) {
	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}
	opts := &slog.HandlerOptions{Level: level}
	if path == "" {
		return slog.New(slog.NewTextHandler(os.Stderr, opts)), nopCloser{}, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, nil, err
	}
	w := &lumberjack.Logger{Filename: path, MaxSize: 10, MaxBackups: 3}
	return slog.New(slog.NewTextHandler(w, opts)), w, nil
}
