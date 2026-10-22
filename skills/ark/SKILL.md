---
name: ark
description: Use when a user on macOS wants to reach a private remote service (database, Redis, internal HTTP API) through an SSH tunnel managed by ark, add, change or remove an ark port mapping, or find out why a local port forwarded by ark refuses connections or shows idle, error or bind failed in `ark status`.
---

# ark: SSH tunnels on local ports

## Overview

`arkd` (a launchd agent) listens on `127.0.0.1` and `::1` for each configured port and opens the SSH connection on demand through the system `/usr/bin/ssh`. `ark` is the client. One SSH connection per server is shared by all its ports. Configuration is one YAML file; the CLI has no "add tunnel" command.

| Command | Purpose |
|---|---|
| `ark status` / `ark status --json` | One row per tunnel (see States below) |
| `ark reload` | Re-read the config. Only added, removed or changed ports restart; an invalid config is rejected and the old one keeps running |
| `ark version` | Versions of `ark` and the running `arkd`; says when they differ |
| `ark install` | (Re)start `arkd` under launchd; needed after replacing the binaries |

Config: `~/.config/ark/config.yaml`. Log: `~/Library/Logs/ark/arkd.log`.

## Adding a mapping

1. **Read the config and `ark status` first.** Reuse an existing entry if the service is already mapped.
2. **Choose the SSH target** (`ssh:`). It is passed to ssh, so `~/.ssh/config` applies: a Host alias, `user@host`, or `user@host:port`. ark has no ProxyJump setting; a jump host must be configured on an alias in `~/.ssh/config`. Confirm the alias exists with `grep -niE '^\s*(Host|Include)\b' ~/.ssh/config` (follow any `Include`). `ssh -G <alias>` shows the resolved settings without connecting, but it never fails: for an unknown alias it just echoes the name as `hostname` with your local `user`. If the alias is missing, ask the user for HostName, User and key instead of guessing.
3. **Choose `remote:`**: the `host:port` as seen *from the SSH server*, e.g. `localhost:5432` for a database on the server itself, or `10.20.0.15:6379` for a host behind it.
4. **Choose `local_port:`**: it must be free, so check with `lsof -nP -iTCP:<port> -sTCP:LISTEN`. Prefer the service's usual port; if something already uses it (often a local Docker container), pick a nearby one such as 6380 or 15432 and tell the user, so they don't talk to the local service by mistake.
5. **Append the entry** to the user's config yourself (that is the intended way to configure ark), keeping the file's indentation:

   ```yaml
   tunnels:
     - name: staging-redis      # optional label shown in status
       local_port: 6380
       ssh: bastion-staging     # alias, user@host or user@host:port
       remote: 10.20.0.15:6379
   ```

6. **Run `ark reload`, then `ark status`.** The new row should show `LISTEN ok` and `STATE idle`; idle is normal, because the SSH connection opens on the first client connection.
7. **Verify with a real connection**: `nc -z 127.0.0.1 6380` always works; the service's own client (`redis-cli -p 6380 PING`, `psql -h 127.0.0.1 -p 15432`) may not be installed. Then `ark status` again: `STATE up`.

## States in `ark status`

| Column | Value | Meaning |
|---|---|---|
| LISTEN | `ok` | Port bound on loopback |
| | `bind failed: … (held by <cmd> pid N)` | Another process owns the port. arkd retries every 5s; it only takes the port over from the user's own `ssh -L` with an identical forward. Never kill the holder without asking. |
| STATE | `idle` | No SSH connection yet, or closed on purpose: after sleep, a network change, or `idle_timeout` (default 30m with no data). Connects on next use. Not an error. |
| | `connecting` / `up` | Opening / open |
| | `error` | Last attempt failed; `LAST ERROR` has ssh's message. Retried on the next client connection |
| CONNS | number | Client connections open through this port right now |

## Common failures

| Symptom | Cause and fix |
|---|---|
| `connection refused` on a port | No tunnel on that port, or `LISTEN` is not ok. Compare with the PORT column: apps often use the server-side port from `remote:` (e.g. 5432) instead of `local_port` (e.g. 6432) |
| `Host key verification failed` | ssh runs non-interactively (`BatchMode=yes`). The user must run `ssh <target>` once in a terminal to accept the host key. Check `ssh-keygen -F <host>` before the first connection to a new server |
| `Permission denied (publickey)` | Key not usable non-interactively: the user adds it with `ssh-add --apple-use-keychain`, or the alias needs `IdentityFile` |
| `Could not resolve hostname` | Alias missing from `~/.ssh/config`, or a typo in `ssh:` |
| `reload failed: …` | Config invalid (duplicate `local_port`, `remote` without a port, …). Fix the file; the old config is still running |
| `ark version` shows different versions | Binaries were replaced but `arkd` was not restarted: `ark install` |

Never edit `~/.ssh/config`, `known_hosts`, keys or the launchd plist without the user's consent.
