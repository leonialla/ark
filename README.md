# ark

English | [简体中文](README.zh-CN.md)

A small macOS tool that keeps your SSH tunnels always on. The name comes from the ark: however the network drops or changes, it carries your connections safely across to the private services on the other side.

It has two programs:

- `arkd`: the background daemon, run by launchd;
- `ark`: the command-line client, which shows status, reloads the config, and installs or removes `arkd`.

Production databases, Redis and similar services usually don't expose their ports to the internet, so you reach them from your laptop through a tunnel like `ssh -fN -L 4333:localhost:4333 ubuntu@host`. The trouble is that the tunnel quietly dies when you switch networks or close the lid, and you have to reconnect it by hand every time.

`arkd` stays in the background and waits for connections on the configured local ports (it listens only on `127.0.0.1` and `::1`):

- When a connection comes in and the SSH connection to that server is missing or dead, it establishes one, then relays the traffic through it.
- It keeps one SSH connection per server (an OpenSSH ControlMaster), and many client connections share it.
- When no data has moved through a server's tunnels for 30 minutes, it closes that SSH connection to free it up, and reconnects on the next connection (`idle_timeout`).
- When it detects a wake from sleep or a change of the primary network interface (for example, a different Wi-Fi network), it drops the stale SSH connections and rebuilds them on the next connection. VPNs coming up or going down and changes to virtual interfaces are ignored.

It calls the system `/usr/bin/ssh` directly, so Host aliases, `ProxyJump`, `IdentityFile` and ssh-agent from `~/.ssh/config` all work exactly as they do in your terminal. TCP only.

## Installation

Requires macOS on Apple silicon (arm64). Intel Macs are not supported.

Download a prebuilt binary:

```bash
curl -fsSL https://github.com/leonialla/ark/releases/latest/download/ark_darwin_arm64.tar.gz | tar -xz
mkdir -p ~/.local/bin
install -m 0755 ark_darwin_arm64/ark ark_darwin_arm64/arkd ~/.local/bin/
~/.local/bin/ark install
```

If you download the archive in a browser instead, macOS quarantines it; clear that with `xattr -d com.apple.quarantine ~/.local/bin/ark ~/.local/bin/arkd`. Checksums are in `checksums.txt` on each release.

With `go install` (Go 1.26+):

```bash
go install github.com/leonialla/ark/cmd/arkd@latest github.com/leonialla/ark/cmd/ark@latest
ark install
```

Both binaries land in `$(go env GOPATH)/bin` (make sure it is on your `PATH`). `ark install` looks for `arkd` in the same directory as itself.

Or from a clone (Go 1.26+):

```bash
git clone https://github.com/leonialla/ark.git
cd ark
make install
```

This installs `arkd` and `ark` into `~/.local/bin/`, then runs `ark install`.

`ark install` does the following:

- If `~/.config/ark/config.yaml` does not exist, it only writes an example config there. Edit it, then run `ark install` again.
- If the config exists, it installs the launchd user agent `~/Library/LaunchAgents/local.ark.plist`. From then on `arkd` starts at login and is restarted whenever it exits.

To uninstall:

```bash
make uninstall
```

Uninstalling keeps your config and logs.

## Configuration

`~/.config/ark/config.yaml`:

```yaml
tunnels:
  - name: prod-mysql
    local_port: 4333            # local port to listen on
    ssh: ubuntu@203.0.113.180   # user@host[:port], or a Host alias from ~/.ssh/config
    remote: localhost:4333      # target address as seen from the server

  - name: prod-redis
    local_port: 6379
    ssh: prod-redis
    remote: 10.0.1.5:6379
```

Optional global settings include `ssh_binary` (default `/usr/bin/ssh`), `connect_timeout` (default `10s`), `idle_timeout` (default `30m`) and `ssh_config_file` (equivalent to `ssh -F`).

`idle_timeout` is measured on data, in either direction, across all tunnels that use the same server. When it expires the SSH connection is closed, including client connections that are still open but have been silent the whole time (for example idle connections in a database pool); common database and Redis clients reconnect on their own. Set `idle_timeout: 0` to keep SSH connections open indefinitely. See [config.example.yaml](config.example.yaml).

After editing the config, apply it with:

```bash
ark reload
```

A reload only affects ports that were added, removed or changed; connections on the other ports are left alone. If the config is invalid, reload reports the error and keeps running with the old config.

## Status

```bash
ark status
```

Example output:

```
NAME        PORT  SSH                   REMOTE          LISTEN  STATE  CONNS  SINCE    LAST ERROR
prod-mysql  4333  ubuntu@203.0.113.180  localhost:4333  ok      up     2      5m ago   -
prod-redis  6379  prod-redis            10.0.1.5:6379   ok      error  0      30s ago  ssh: connect to host … Operation timed out
```

- `STATE` is the state of the SSH connection to that server: `idle` (not connected yet, dropped after sleep or a network change, or closed by `idle_timeout`), `connecting`, `up` or `error`.
- Add `--json` for JSON output.

Logs are written to `~/Library/Logs/ark/arkd.log` and rotated automatically (10MB × 3 files).

## Troubleshooting

- **Can't connect, and the log says `Host key verification failed`**: ssh runs non-interactively (`BatchMode=yes`), so it never asks you to confirm a new host. Run `ssh <target>` once by hand in a terminal to add the host key to `known_hosts`.
- **Your private key has a passphrase**: add the key to the agent or the keychain first with `ssh-add --apple-use-keychain`. The daemon never asks for passphrases.
- **The port is held by an `ssh -L` you started by hand earlier**: if that `ssh` process is yours and its forward matches the config exactly (same local port bound to a loopback address, same remote address, same SSH target user/host/port, and no target-changing options such as `-J` or `-o HostName=`), `arkd` stops it (SIGTERM, then SIGKILL if it hasn't exited within 3 seconds), takes over the port, and logs a warning. The comparison is literal: `localhost` and `127.0.0.1` count as different.
- **`LISTEN` shows `bind failed: ... (held by node pid 812)`**: another program holds the port. `arkd` leaves it alone and only reports who holds it; an `ssh` with a different forward shows up as `ssh pid 123 (different forward)`. Once you close that program, `arkd` retries every 5 seconds and takes over the port when it is free.
- **Debugging in the foreground**: stop the background service with `launchctl bootout gui/$(id -u)/local.ark`, then run `arkd --log - --debug` to print the log to your terminal.

## License

[MIT](LICENSE)
