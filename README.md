# ssh-tunnel-daemon

让 SSH 隧道“永远在线”的 macOS 小工具。

线上的数据库、Redis 等服务不对外暴露端口，本地一般用 `ssh -fN -L 4333:localhost:4333 ubuntu@host` 打隧道访问。问题是换网络、合盖睡眠后隧道会悄悄断掉，每次都得手动重连。

ssh-tunnel-daemon 常驻后台，在配置好的本地端口（只监听 `127.0.0.1` 和 `::1`）上等待连接：

- 有连接进来时，如果到对应服务器的 SSH 连接不存在或已断开，就自动建立，再把数据转发过去；
- 同一台服务器只保持一条 SSH 连接（OpenSSH ControlMaster），上面可以同时跑多个客户端连接；
- 检测到睡眠唤醒或主网卡变化（比如换了 Wi-Fi）时，主动丢弃已经失效的 SSH 连接，下一次连接时重建。VPN 连接、断开和虚拟网卡的变化会被忽略。

它直接调用系统自带的 `/usr/bin/ssh`，所以 `~/.ssh/config` 里的 Host 别名、`ProxyJump`、`IdentityFile`、ssh-agent 都和在终端里一样能用。只支持 TCP。

## 安装

需要 Go 1.26+。

```bash
make install
```

这一步会把程序装到 `~/.local/bin/ssh-tunnel-daemon`，然后执行 `ssh-tunnel-daemon install`：

- 如果 `~/.config/ssh-tunnel-daemon/config.yaml` 不存在，只会写入一份示例配置。改好后再执行一次 `ssh-tunnel-daemon install`。
- 如果配置已存在，会安装 launchd 用户代理 `~/Library/LaunchAgents/local.ssh-tunnel-daemon.plist`。之后登录时自动启动，进程退出后自动拉起。

卸载：

```bash
make uninstall
```

卸载会保留配置和日志。

## 配置

`~/.config/ssh-tunnel-daemon/config.yaml`：

```yaml
tunnels:
  - name: prod-mysql
    local_port: 4333            # 本地监听端口
    ssh: ubuntu@203.0.113.180   # user@host[:port]，或 ~/.ssh/config 里的 Host 别名
    remote: localhost:4333      # 在服务器上看到的目标地址

  - name: prod-redis
    local_port: 6379
    ssh: prod-redis
    remote: 10.0.1.5:6379
```

可选的全局项包括 `ssh_binary`（默认 `/usr/bin/ssh`）、`connect_timeout`（默认 `10s`）、`ssh_config_file`（相当于 `ssh -F`），详见 [config.example.yaml](config.example.yaml)。

修改配置后执行下面的命令生效：

```bash
ssh-tunnel-daemon reload
```

reload 只会影响新增、删除或修改过的端口，其余端口上的连接不受影响。如果配置有错，reload 会报错，并继续使用旧配置。

## 查看状态

```bash
ssh-tunnel-daemon status
```

输出示例：

```
NAME        PORT  SSH                   REMOTE          LISTEN  STATE  CONNS  SINCE    LAST ERROR
prod-mysql  4333  ubuntu@203.0.113.180  localhost:4333  ok      up     2      5m ago   -
prod-redis  6379  prod-redis            10.0.1.5:6379   ok      error  0      30s ago  ssh: connect to host … Operation timed out
```

- `STATE` 是到这台服务器的 SSH 连接状态，取值为 `idle`（还没连 / 已主动断开）、`connecting`、`up`、`error`。
- 加 `--json` 可以输出 JSON 格式。

日志位于 `~/Library/Logs/ssh-tunnel-daemon/daemon.log`，自动轮转（10MB × 3 份）。

## 排查

- **连不上，日志里有 `Host key verification failed`**：服务以非交互模式运行 ssh（`BatchMode=yes`），不会弹出确认新主机的提示。先在终端里手动 `ssh <目标>` 一次，把主机指纹加入 `known_hosts`。
- **私钥有密码**：先用 `ssh-add --apple-use-keychain` 把私钥加到 agent 或钥匙串里。服务不会询问密码。
- **端口被以前手动开的 `ssh -L` 占着**：如果这个 `ssh` 进程是你自己的，并且转发规则和配置完全一致（本地端口相同、绑定在回环地址上、远程地址相同、SSH 目标的 user/host/port 相同，也没有用 `-J`、`-o HostName=` 这类改变目标的选项），服务会结束它（先 SIGTERM，3 秒内没退出再 SIGKILL），然后接管端口，并在日志里记一条 warn。比较是逐字的，`localhost` 和 `127.0.0.1` 算作不同。
- **`LISTEN` 显示 `bind failed: ... (held by node pid 812)`**：端口被其他程序占用，服务不会动它，只显示占用者是谁。转发规则不同的 `ssh` 会显示为 `ssh pid 123 (different forward)`。你自己关掉占用的程序后，服务每 5 秒会重试一次，端口释放后自动接管。
- **前台调试**：先 `launchctl bootout gui/$(id -u)/local.ssh-tunnel-daemon` 停掉后台服务，再运行 `ssh-tunnel-daemon run --log - --debug`，日志会直接打印到终端。
