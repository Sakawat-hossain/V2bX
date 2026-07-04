<div align="center">

<img src=".github/assets/banner.png" alt="V2bX" width="820">

### 一个 Go 二进制文件，十二种协议，为真实生产环境而生。

面向 **XBoard**、**V2Board** 以及任何实现 UniProxy 节点 API 的面板的全新（clean-room）节点后端。它按间隔从面板拉取节点配置与用户列表、启动各协议监听、回报流量与在线设备，并在 DPI、限速和主动探测下依然稳定可用。

<br>

[![CI](https://img.shields.io/github/actions/workflow/status/Sakawat-hossain/V2bX/ci.yml?branch=main&style=flat-square&label=CI&labelColor=0B0E14&color=0AB2F9)](https://github.com/Sakawat-hossain/V2bX/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/Sakawat-hossain/V2bX?style=flat-square&label=release&labelColor=0B0E14&color=7C3AED)](https://github.com/Sakawat-hossain/V2bX/releases)
[![Go](https://img.shields.io/badge/Go-1.25-0AB2F9?style=flat-square&labelColor=0B0E14&logo=go&logoColor=white)](go.mod)
[![Protocols](https://img.shields.io/badge/protocols-12-7C3AED?style=flat-square&labelColor=0B0E14)](docs/PROTOCOLS.md)
[![License](https://img.shields.io/badge/license-MIT-A16FEE?style=flat-square&labelColor=0B0E14)](LICENSE)

[English](README.md) · **简体中文**

</div>

---

## 支持的协议

每个节点类型都独立运行，可按节点单独启用/停用。下表按**每种协议如何保护链路**分组——这一维度决定了节点需要什么（证书、加密方式，或什么都不需要）以及它在网络上呈现的样子。

| 节点类型 | 链路 | 需要证书 | 亮点 |
|-----------|------|:----------:|------|
| **Shadowsocks** | 自加密 TCP（AEAD） | — | 7 种加密方式，含 Shadowsocks-2022 blake3 |
| **VMess** | 自加密 TCP（AEAD） | — | 单端口多用户 |
| **VLess** | 自加密 TCP | 可选 | XTLS/Vision flow · **Reality** · **WebSocket/CDN** |
| **Trojan** | TLS 承载 TCP | 自动 | SHA-224 认证 · **诱饵回落** |
| **Naive** | TLS 承载 HTTP/2 CONNECT | 自动 | 每用户 HTTP Basic |
| **AnyTLS** | TLS 承载会话 | 自动 | 填充方案，SHA-256 认证 |
| **Hysteria** | QUIC / UDP | 自动 | Brutal 带宽 · **端口跳跃** |
| **Hysteria2** | QUIC / UDP | 自动 | Brutal · **Salamander 混淆** · 端口跳跃 |
| **TUIC** | QUIC / UDP | 自动 | UUID + 密码 · 端口跳跃 |
| **SOCKS5** | 明文 | — | 可选用户名/密码认证 |
| **HTTP** | 明文 | — | CONNECT + 转发，可选认证 |
| **Mieru** | 混淆传输 | — | TCP 或 UDP 传输 |

对于任何 TLS/QUIC 节点，如果你不提供证书，程序会自动生成自签名证书。完整协议说明见 **[docs/PROTOCOLS.md](docs/PROTOCOLS.md)**。

新增协议无需改动面板同步或 CLI 层——每个后端只需实现一个小接口并自行注册：

```go
type ProtocolServer interface {
    Start(cfg NodeConfig) error
    Stop() error
    Stats() UsageStats
    Name() string
}
```

## 为生产环境打造

- **不会漏算流量的计费** —— 用量以“自面板上次确认以来的增量”上报；上报失败会完整重试，绝不丢弃。
- **用户热更新** —— 增删订阅用户时**就地更新**节点（在编解码库支持的前提下），而不是每次同步都断开所有活动连接。
- **设备 / IP 限制** —— 节点通过 `/alive` 上报每个用户的来源 IP，由面板在整个节点集群范围内统一执行限制。
- **单用户限速** —— 每个用户共享一个令牌桶，因此开更多连接也无法叠加带宽。
- **高效同步** —— 使用带 ETag/`304` 的条件 `GET`，配置/用户未变化时只传空响应体。
- **监控指标** —— 可选的 Prometheus `/metrics` 端点（节点数、在线用户、流量、面板 push/sync 健康度）。
- **安全阀** —— 可选的每节点连接数上限、复用的转发缓冲区，以及**加固的 systemd 单元**（丢弃多余能力、只读文件系统、系统调用过滤）。

## 为受限网络环境打造

- **Reality**（VLESS）—— 借用真实网站的 TLS 握手；任何非授权连接都会被透明代理到那个真实网站，因此主动探测看到的是真实网站而非代理。配置不完整时**拒绝启动**（fail closed）。
- **Brutal 拥塞控制**（Hysteria/Hysteria2）—— `up_mbps`/`down_mbps` 设定速率并启用 Brutal；Brutal 忽略丢包，因此在通过注入丢包来限速的链路上仍能保持吞吐。
- **Salamander 混淆**（Hysteria2）—— 隐藏 QUIC 握手，避免被 DPI 分类器识别。
- **端口跳跃**（Hysteria/Hysteria2/TUIC）—— 程序自动安装 `iptables` 规则，把一段 UDP 端口范围重定向到节点端口，客户端在该范围内散射发送，从而规避按流限速和单端口封锁。
- **Trojan 诱饵回落** —— 将未认证连接转发到真实后端，而不是直接重置。
- **VLESS-WebSocket** —— 置于 CDN（如 Cloudflare）之后，其 IP 难以被封锁。

> ⚠️ **抗审查功能务必先灰度（canary）部署。** 错误的 Reality/CDN 配置会形成稳定指纹，可能导致共用该指纹的所有 IP 同时被封——先在一两个节点上线并观察，再全量铺开。详见 [docs/PROTOCOLS.md](docs/PROTOCOLS.md)。

## 面板兼容性

V2bX 使用 V2board 系列通用的 UniProxy HTTP API：

| 调用 | 用途 |
|------|------|
| `GET  {config_path}` | 节点配置——协议、端口、加密方式、TLS |
| `GET  {user_path}` | 该节点当前的订阅用户列表 |
| `POST {push_path}` | 每用户流量用量 |
| `POST {alive_path}` | 当前在线用户 / 设备 IP |

基础 URL、API 密钥和这四个路径都是**由配置驱动**的，因此同一个二进制无需改代码即可对接 XBoard、V2Board 或任意兼容分支。若面板短暂不可达，同步会以指数退避重试，每个节点则继续以最后已知有效配置对外服务。

## 快速安装

```bash
curl -fsSL https://raw.githubusercontent.com/Sakawat-hossain/V2bX/main/install.sh -o install.sh
sudo bash install.sh install
```

它会把二进制安装到 `/usr/local/bin/v2bx`、安装加固后的 systemd 单元，并询问是否运行**交互式配置向导**——回答几个问题（面板地址、API 密钥、节点类型），它就会写入 `/etc/v2bx/config.json` 并启动服务。无需手动编辑 JSON 即可上手。

不想记命令？直接运行 `sudo v2bx` 即可打开菜单。

## 命令

不带参数运行 `v2bx` 会打开交互式菜单，也可以直接使用任意命令：

| 命令 | 作用 |
|---------|------|
| `v2bx` | 打开交互式菜单 |
| `v2bx generate` · `add` · `del` · `edit` | 生成 / 编辑配置（可加 `-c PATH`） |
| `v2bx server [-c PATH]` | 前台运行节点（systemd 实际执行的命令） |
| `v2bx start` · `stop` · `restart` · `status` | 管理 systemd 服务 |
| `v2bx enable` · `disable` | 开关开机自启 |
| `v2bx reload` | 立即强制与面板重新同步（SIGHUP） |
| `v2bx log` | 跟随查看服务日志 |
| `v2bx update` | 原地更新到最新版本 |
| `v2bx x25519` | 生成 X25519 密钥对（Reality 用） |
| `v2bx bbr` · `firewall` | 启用 BBR · 放行入站端口 |
| `v2bx uninstall` · `version` | 卸载服务 · 查看版本 |

## Docker

每次发布都会向 GHCR 推送多架构镜像（`linux/amd64`、`linux/arm64`）。

```bash
mkdir config
docker run --rm -it -v "$PWD/config:/etc/v2bx" \
  ghcr.io/sakawat-hossain/v2bx:latest generate   # 一次性生成配置
docker compose up -d                             # 参见 docker-compose.yml
```

## 配置

单个 JSON 文件（默认 `/etc/v2bx/config.json`）。[`config.example.json`](config.example.json) 提供了每种节点类型的完整示例；[docs/CONFIG.md](docs/CONFIG.md) 是逐字段参考。

```jsonc
{
  "log":     { "level": "info", "output": "stdout" },
  "panel":   { "api_host": "https://panel.example.com", "api_key": "…", "sync_interval_seconds": 60 },
  "metrics": { "listen": "127.0.0.1:9095" },
  "nodes": [
    { "node_id": 1, "node_type": "shadowsocks", "enabled": true, "listen_ip": "0.0.0.0" }
  ]
}
```

## 从源码构建

```bash
go build -o v2bx ./cmd/v2bx      # 需要 Go 1.25+
go test ./...
```

推送 `vX.Y.Z` 标签会触发 CI 交叉编译 `linux/amd64`、`linux/arm64`、`linux/armv7`，将压缩包发布到 Releases，并推送多架构 Docker 镜像。

## 参与贡献

欢迎提交新协议和修复——详见 **[CONTRIBUTING.md](CONTRIBUTING.md)**，尤其是在 `ProtocolServer` 接口之后新增协议的说明。本项目为全新独立实现：原创设计，不移植其他节点后端的逻辑；以 Go module 形式依赖协议 SDK 则是正常的工程做法。

## 许可证

[MIT](LICENSE) · 基于优秀的 [sing](https://github.com/sagernet/sing)、[sing-quic](https://github.com/sagernet/sing-quic)、[reality](https://github.com/sagernet/reality)、[mieru](https://github.com/enfein/mieru) 和 [sing-anytls](https://github.com/anytls/sing-anytls) 库构建。
