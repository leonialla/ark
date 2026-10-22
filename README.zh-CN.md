# ark

[English](README.md) | 简体中文

让 SSH 隧道“永远在线”的 macOS 小工具。名字取自方舟（ark）：不管网络怎么断、怎么换，都把你的连接安全送到对岸的内网服务。

它由两个程序组成：

- `arkd`：常驻后台的守护进程，由 launchd 负责运行；
- `ark`：命令行客户端，用来查看状态、重新加载配置，以及安装或卸载 `arkd`。

线上的数据库、Redis 等服务不对外暴露端口，本地一般用 `ssh -fN -L 4333:localhost:4333 ubuntu@host` 打隧道访问。问题是换网络、合盖睡眠后隧道会悄悄断掉，每次都得手动重连。

`arkd` 常驻后台，在配置好的本地端口（只监听 `127.0.0.1` 和 `::1`）上等待连接：

- 有连接进来时，如果到对应服务器的 SSH 连接不存在或已断开，就自动建立，再把数据转发过去；
- 同一台服务器只保持一条 SSH 连接（OpenSSH ControlMaster），上面可以同时跑多个客户端连接；
- 某台服务器的隧道连续 30 分钟没有任何数据时，关闭这条 SSH 连接以释放资源，下一次有连接进来时再重建（`idle_timeout`）。
- 检测到睡眠唤醒或主网卡变化（比如换了 Wi-Fi）时，主动丢弃已经失效的 SSH 连接，下一次连接时重建。VPN 连接、断开和虚拟网卡的变化会被忽略。

它直接调用系统自带的 `/usr/bin/ssh`，所以 `~/.ssh/config` 里的 Host 别名、`ProxyJump`、`IdentityFile`、ssh-agent 都和在终端里一样能用。只支持 TCP。

## 安装

需要 Apple silicon（arm64）的 macOS，不支持 Intel 芯片的 Mac。

下载预编译的二进制：

```bash
curl -fsSL https://github.com/leonialla/ark/releases/latest/download/ark_darwin_arm64.tar.gz | tar -xz
mkdir -p ~/.local/bin
install -m 0755 ark_darwin_arm64/ark ark_darwin_arm64/arkd ~/.local/bin/
~/.local/bin/ark install
```

如果是用浏览器下载的压缩包，macOS 会给它加上隔离标记，可以用 `xattr -d com.apple.quarantine ~/.local/bin/ark ~/.local/bin/arkd` 去掉。每个 Release 里都附有 `checksums.txt` 校验和。

用 `go install` 安装（需要 Go 1.26+）：

```bash
go install github.com/leonialla/ark/cmd/arkd@latest github.com/leonialla/ark/cmd/ark@latest
ark install
```

两个程序会装到 `$(go env GOPATH)/bin`（确认它在你的 `PATH` 里）。`ark install` 会在自己所在的目录里找 `arkd`。

或者从源码安装（需要 Go 1.26+）：

```bash
git clone https://github.com/leonialla/ark.git
cd ark
make install
```

这一步会把 `arkd` 和 `ark` 装到 `~/.local/bin/`，然后执行 `ark install`。

`ark install` 会做这些事：

- 如果 `~/.config/ark/config.yaml` 不存在，只会写入一份示例配置。改好后再执行一次 `ark install`。
- 如果配置已存在，会安装 launchd 用户代理 `~/Library/LaunchAgents/local.ark.plist`。之后登录时自动启动，进程退出后自动拉起。

卸载：

```bash
make uninstall
```

卸载会保留配置和日志。

## 配置

`~/.config/ark/config.yaml`：

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

可选的全局项包括 `ssh_binary`（默认 `/usr/bin/ssh`）、`connect_timeout`（默认 `10s`）、`idle_timeout`（默认 `30m`）、`ssh_config_file`（相当于 `ssh -F`），详见 [config.example.yaml](config.example.yaml)。

`idle_timeout` 按数据判断：同一台服务器上所有隧道、任一方向只要有数据流动，就算有活动。超时后会关闭这条 SSH 连接，其中也包括一直开着但始终没有数据的客户端连接，比如数据库连接池里的空闲连接；常见的数据库和 Redis 客户端会自动重连。设成 `idle_timeout: 0` 则永不回收。

修改配置后执行下面的命令生效：

```bash
ark reload
```

reload 只会影响新增、删除或修改过的端口，其余端口上的连接不受影响。如果配置有错，reload 会报错，并继续使用旧配置。

## 查看状态

```bash
ark status
```

输出示例：

```
NAME        PORT  SSH                   REMOTE          LISTEN  STATE  CONNS  SINCE    LAST ERROR
prod-mysql  4333  ubuntu@203.0.113.180  localhost:4333  ok      up     2      5m ago   -
prod-redis  6379  prod-redis            10.0.1.5:6379   ok      error  0      30s ago  ssh: connect to host … Operation timed out
```

- `STATE` 是到这台服务器的 SSH 连接状态，取值为 `idle`（还没连、睡眠唤醒或换网络后已断开，或被 `idle_timeout` 回收）、`connecting`、`up`、`error`。
- 加 `--json` 可以输出 JSON 格式。

`ark version` 会显示 `ark` 和正在运行的 `arkd` 的版本。两者不一致时（比如升级后还没重启），会提示你执行 `ark install`。`arkd --version` 显示守护进程自己的版本。

日志位于 `~/Library/Logs/ark/arkd.log`，自动轮转（10MB × 3 份）。

## 排查

- **连不上，日志里有 `Host key verification failed`**：服务以非交互模式运行 ssh（`BatchMode=yes`），不会弹出确认新主机的提示。先在终端里手动 `ssh <目标>` 一次，把主机指纹加入 `known_hosts`。
- **私钥有密码**：先用 `ssh-add --apple-use-keychain` 把私钥加到 agent 或钥匙串里。服务不会询问密码。
- **端口被以前手动开的 `ssh -L` 占着**：如果这个 `ssh` 进程是你自己的，并且转发规则和配置完全一致（本地端口相同、绑定在回环地址上、远程地址相同、SSH 目标的 user/host/port 相同，也没有用 `-J`、`-o HostName=` 这类改变目标的选项），服务会结束它（先 SIGTERM，3 秒内没退出再 SIGKILL），然后接管端口，并在日志里记一条 warn。比较是逐字的，`localhost` 和 `127.0.0.1` 算作不同。
- **`LISTEN` 显示 `bind failed: ... (held by node pid 812)`**：端口被其他程序占用，服务不会动它，只显示占用者是谁。转发规则不同的 `ssh` 会显示为 `ssh pid 123 (different forward)`。你自己关掉占用的程序后，服务每 5 秒会重试一次，端口释放后自动接管。
- **前台调试**：先 `launchctl bootout gui/$(id -u)/local.ark` 停掉后台服务，再运行 `arkd --log - --debug`，日志会直接打印到终端。

## AI agent 技能

[`skills/ark/SKILL.md`](skills/ark/SKILL.md) 用来指导 coding agent（Claude Code、Codex、Gemini CLI 以及其他支持 [Agent Skills](https://agentskills.io) 的工具）配置和检查 ark 隧道：选择空闲的本地端口、SSH 目标和远程地址，重新加载配置，看懂 `ark status`，以及排查常见问题。为 Claude Code 安装：

```bash
mkdir -p ~/.claude/skills/ark
curl -fsSL https://raw.githubusercontent.com/leonialla/ark/main/skills/ark/SKILL.md -o ~/.claude/skills/ark/SKILL.md
```

其他 agent 请放到 `~/.agents/skills/ark/`。装好后，可以直接对 agent 说“用 ark 把 bastion-staging 后面的 staging Redis 映射到本地端口”。

## 与同类工具对比

以下为 2026 年 10 月的情况：

| 工具 | 做法 | 和 ark 的区别 |
|---|---|---|
| [autossh](https://www.harding.motd.ca/autossh/) | `ssh -L` 进程退出后自动重启它。最新版本 1.4g（2019 年）。 | ark 有客户端连进来时才建立连接，空闲时自动回收；睡眠唤醒或换网络后立刻丢弃失效连接，不用等心跳超时；同一台服务器的所有端口共用一条 SSH 连接。 |
| [mole](https://github.com/davrodpin/mole) | 跨平台命令行工具，保持隧道在线并自动重连。最新版本 v2.0.0（2021 年）。 | 和 autossh 一样是常驻连接，不处理睡眠和网络切换。 |
| [Core Tunnel](https://apps.apple.com/app/id1354318707) | 做工精致的 macOS 菜单栏应用，能读取 `ssh_config`，睡眠唤醒和网络中断后自动恢复隧道。免费下载，高级版按年付费。 | 闭源、通过图形界面操作。ark 免费、可以写进脚本，用一个 YAML 文件配置，按需连接而不是一直在线。 |
| [Secure Pipes](https://formulae.brew.sh/cask/secure-pipes) | 免费的 macOS 菜单栏应用，支持本地/远程转发和 SOCKS 代理。 | Homebrew 上的版本只为 Intel 编译，在 Apple silicon 上需要 Rosetta 2。 |
| [sshuttle](https://github.com/sshuttle/sshuttle) | 基于 SSH 的“简易 VPN”，转发整个网段。仍在积极维护。 | 解决的是另一类问题：需要 sudo、会修改路由，可能和公司 VPN 冲突。ark 只开放你列出的端口。 |
| 数据库客户端（TablePlus、DataGrip、DBeaver 等） | 每个连接内置 SSH 隧道。 | 只能在该客户端里用；ark 的端口任何程序、命令行和脚本都能用。 |

如果你需要支持 Linux 或 Intel 芯片的 Mac、想要图形界面，或者要访问整个网段而不只是几个端口，其他工具会更合适。

## 许可证

[MIT](LICENSE)
