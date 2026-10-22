// Package daemon wires config, SSH masters, listeners and network events together.
package daemon

import (
	"context"
	"log/slog"
	"sort"
	"sync"
	"time"

	"ssh-tunnel-daemon/internal/config"
	"ssh-tunnel-daemon/internal/netwatch"
	"ssh-tunnel-daemon/internal/proxy"
	"ssh-tunnel-daemon/internal/sshmux"
)

type TunnelStatus struct {
	Name      string    `json:"name"`
	LocalPort int       `json:"local_port"`
	SSH       string    `json:"ssh"`
	Remote    string    `json:"remote"`
	Listen    string    `json:"listen"`
	State     string    `json:"state"`
	Conns     int       `json:"conns"`
	Since     time.Time `json:"since"`
	LastError string    `json:"last_error"`
}

type Daemon struct {
	cfgPath string
	log     *slog.Logger

	mu        sync.Mutex
	cfg       *config.Config
	running   bool
	masters   map[string]*sshmux.Master
	listeners map[int]*proxy.Listener
}

func New(cfgPath string, log *slog.Logger) (*Daemon, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, err
	}
	return &Daemon{
		cfgPath:   cfgPath,
		log:       log,
		cfg:       cfg,
		masters:   map[string]*sshmux.Master{},
		listeners: map[int]*proxy.Listener{},
	}, nil
}

// Run starts all tunnels and resets every master on each network event, until ctx ends.
func (d *Daemon) Run(ctx context.Context, events <-chan netwatch.Event) error {
	d.mu.Lock()
	d.running = true
	d.apply(d.cfg, nil)
	n := len(d.cfg.Tunnels)
	d.mu.Unlock()
	d.log.Info("daemon started", "config", d.cfgPath, "tunnels", n)

	for {
		select {
		case <-ctx.Done():
			d.shutdown()
			return nil
		case ev, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			d.log.Info("network event, resetting masters", "kind", ev.Kind, "detail", ev.Detail)
			d.resetAll(string(ev.Kind))
		}
	}
}

// Reload re-reads the config file. An invalid config leaves everything unchanged.
func (d *Daemon) Reload() error {
	cfg, err := config.Load(d.cfgPath)
	if err != nil {
		d.log.Error("reload failed, keeping current config", "err", err)
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	old := d.cfg
	d.cfg = cfg
	if d.running {
		d.apply(cfg, old)
	}
	d.log.Info("config reloaded", "tunnels", len(cfg.Tunnels))
	return nil
}

func sameGlobals(a, b *config.Config) bool {
	return a.SSHBinary == b.SSHBinary && a.SSHConfigFile == b.SSHConfigFile &&
		a.ConnectTimeout == b.ConnectTimeout && a.SocketDir == b.SocketDir
}

// apply brings listeners and masters in line with cfg. Caller holds d.mu.
func (d *Daemon) apply(cfg, old *config.Config) {
	if old != nil && !sameGlobals(cfg, old) {
		d.log.Info("global options changed, restarting all tunnels")
		for port, l := range d.listeners {
			l.Stop()
			delete(d.listeners, port)
		}
		for target, m := range d.masters {
			m.Close()
			delete(d.masters, target)
		}
	}

	want := map[int]config.Tunnel{}
	for _, t := range cfg.Tunnels {
		want[t.LocalPort] = t
	}
	for port, l := range d.listeners {
		if t, ok := want[port]; !ok || t != l.Tunnel() {
			d.log.Info("stopping tunnel", "tunnel", l.Tunnel().Label(), "port", port)
			l.Stop()
			delete(d.listeners, port)
		}
	}
	for _, t := range cfg.Tunnels {
		if _, ok := d.listeners[t.LocalPort]; ok {
			continue
		}
		m := d.masters[t.SSH]
		if m == nil {
			m = sshmux.New(sshmux.Options{
				SSHBinary:      cfg.SSHBinary,
				SSHConfigFile:  cfg.SSHConfigFile,
				SocketDir:      cfg.SocketDir,
				Target:         t.SSH,
				ConnectTimeout: cfg.ConnectTimeout,
				Logger:         d.log,
			})
			d.masters[t.SSH] = m
		}
		l := proxy.NewListener(t, m, d.log)
		l.Start()
		d.listeners[t.LocalPort] = l
	}

	used := map[string]bool{}
	for _, t := range cfg.Tunnels {
		used[t.SSH] = true
	}
	for target, m := range d.masters {
		if !used[target] {
			m.Close()
			delete(d.masters, target)
		}
	}
}

func (d *Daemon) resetAll(reason string) {
	d.mu.Lock()
	masters := make([]*sshmux.Master, 0, len(d.masters))
	for _, m := range d.masters {
		masters = append(masters, m)
	}
	d.mu.Unlock()
	var wg sync.WaitGroup
	for _, m := range masters {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.Reset(reason)
		}()
	}
	wg.Wait()
}

func (d *Daemon) shutdown() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.running = false
	for port, l := range d.listeners {
		l.Stop()
		delete(d.listeners, port)
	}
	for target, m := range d.masters {
		m.Close()
		delete(d.masters, target)
	}
	d.log.Info("daemon stopped")
}

// Status returns one row per tunnel, sorted by local port.
func (d *Daemon) Status() []TunnelStatus {
	d.mu.Lock()
	ls := make([]*proxy.Listener, 0, len(d.listeners))
	for _, l := range d.listeners {
		ls = append(ls, l)
	}
	masters := make(map[string]*sshmux.Master, len(d.masters))
	for k, v := range d.masters {
		masters[k] = v
	}
	d.mu.Unlock()

	rows := make([]TunnelStatus, 0, len(ls))
	for _, l := range ls {
		t := l.Tunnel()
		lst := l.Status()
		row := TunnelStatus{
			Name: t.Label(), LocalPort: t.LocalPort, SSH: t.SSH, Remote: t.Remote,
			Listen: lst.Listen, Conns: lst.Conns, LastError: lst.LastError,
		}
		if m := masters[t.SSH]; m != nil {
			ms := m.Status()
			row.State, row.Since = string(ms.State), ms.Since
			// A master in error explains the most; otherwise the listener's newest error.
			if ms.State == sshmux.StateError || row.LastError == "" {
				if ms.LastError != "" {
					row.LastError = ms.LastError
				}
			}
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].LocalPort < rows[j].LocalPort })
	return rows
}
