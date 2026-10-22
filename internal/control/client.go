package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"syscall"
	"time"

	"github.com/leonialla/ark/internal/daemon"
)

// ErrNotRunning means nothing is listening on the control socket.
var ErrNotRunning = errors.New("daemon is not running")

func client(sockPath string) *http.Client {
	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", sockPath)
			},
		},
	}
}

func do(sockPath, method, path string) (*http.Response, error) {
	req, err := http.NewRequest(method, "http://daemon"+path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client(sockPath).Do(req)
	if err != nil {
		if errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED) {
			return nil, fmt.Errorf("%w (no control socket at %s)", ErrNotRunning, sockPath)
		}
		return nil, err
	}
	return resp, nil
}

func FetchStatus(sockPath string) ([]daemon.TunnelStatus, error) {
	resp, err := do(sockPath, http.MethodGet, "/status")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status: HTTP %d", resp.StatusCode)
	}
	var rows []daemon.TunnelStatus
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return nil, err
	}
	return rows, nil
}

func Reload(sockPath string) error {
	resp, err := do(sockPath, http.MethodPost, "/reload")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return errors.New(strings.TrimSpace(string(body)))
	}
	return nil
}
