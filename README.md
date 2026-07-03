<div align="center">

<img src=".github/assets/banner.png" alt="V2bX" width="820">

### One Go binary. Twelve protocols. Any V2board-family panel.

A clean-room node agent for **XBoard**, **V2Board**, and anything else that speaks the UniProxy node API — it pulls your node config and subscriber list on an interval, runs the listeners, and reports traffic back.

<br>

[![CI](https://img.shields.io/github/actions/workflow/status/Sakawat-hossain/V2bX/ci.yml?branch=main&style=flat-square&label=CI&labelColor=0B0E14&color=0AB2F9)](https://github.com/Sakawat-hossain/V2bX/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/Sakawat-hossain/V2bX?style=flat-square&label=release&labelColor=0B0E14&color=7C3AED)](https://github.com/Sakawat-hossain/V2bX/releases)
[![Go](https://img.shields.io/badge/Go-1.25-0AB2F9?style=flat-square&labelColor=0B0E14&logo=go&logoColor=white)](go.mod)
[![Protocols](https://img.shields.io/badge/protocols-12-7C3AED?style=flat-square&labelColor=0B0E14)](docs/PROTOCOLS.md)
[![License](https://img.shields.io/badge/license-MIT-A16FEE?style=flat-square&labelColor=0B0E14)](LICENSE)

</div>

---

## Protocols

Every node type runs independently and can be toggled per node. They're grouped below by **how each one secures the wire** — that's the axis that decides what a node needs (a cert, a cipher, nothing) and how it looks on the network.

| Node type | Wire | Needs cert | Per-node knobs |
|-----------|------|:----------:|----------------|
| **Shadowsocks** | Self-encrypted TCP (AEAD) | — | 7 ciphers incl. Shadowsocks-2022 blake3 |
| **VMess** | Self-encrypted TCP (AEAD) | — | Single-port multi-user |
| **VLess** | Self-encrypted TCP | optional | XTLS/Vision flow per user |
| **Trojan** | TLS-wrapped TCP | required | SHA-224 password auth |
| **Naive** | TLS-wrapped HTTP/2 CONNECT | required | HTTP Basic per user |
| **AnyTLS** | TLS-wrapped session | required | Padding scheme, SHA-256 auth |
| **Hysteria** | QUIC / UDP | required | v1 wire format |
| **Hysteria2** | QUIC / UDP | required | v2 wire format |
| **TUIC** | QUIC / UDP | required | UUID + password |
| **SOCKS5** | Plaintext | — | Optional user/password auth |
| **HTTP** | Plaintext | — | CONNECT + forward, optional auth |
| **Mieru** | Obfuscated transport | — | TCP or UDP transport |

Shadowsocks covers `aes-128-gcm`, `aes-192-gcm`, `aes-256-gcm`, `chacha20-ietf-poly1305`, and the three `2022-blake3-*` ciphers. Full protocol-specific notes live in **[docs/PROTOCOLS.md](docs/PROTOCOLS.md)**.

Adding a protocol never touches the panel-sync or CLI layers — each backend satisfies one small interface and registers itself:

```go
type ProtocolServer interface {
    Start(cfg NodeConfig) error
    Stop() error
    Stats() UsageStats
    Name() string
}
```

## Panel compatibility

V2bX speaks the UniProxy HTTP API shared across the V2board family:

| Call | Purpose |
|------|---------|
| `GET  {config_path}` | node config — protocol, port, cipher, TLS |
| `GET  {user_path}` | the node's current subscriber list |
| `POST {push_path}` | per-user traffic usage |
| `POST {alive_path}` | currently-online users |

Base URL, API key, and all four paths are **config-driven**, so one binary targets XBoard, V2Board, or any compatible fork without a code change. If the panel goes briefly unreachable, sync retries with exponential backoff and every node keeps serving on its last-known-good config — a hiccup upstream never drops your users.

## Quick install

```bash
curl -fsSL https://raw.githubusercontent.com/Sakawat-hossain/V2bX/main/install.sh -o install.sh
sudo bash install.sh install
```

That drops the binary at `/usr/local/bin/v2bx`, a starter config at `/etc/v2bx/config.json`, and a systemd unit. Edit `panel.api_host`, `panel.api_key`, and your `nodes` list, then:

```bash
sudo v2bx enable   # start on boot
sudo v2bx start
sudo v2bx status
sudo v2bx log      # follow the journal
```

Update or remove any time:

```bash
sudo bash install.sh update
sudo bash install.sh uninstall
```

## Commands

| Command | Does |
|---------|------|
| `v2bx server [-c PATH]` | run the agent in the foreground (what systemd runs) |
| `v2bx start` · `stop` · `restart` | manage the systemd service |
| `v2bx status` | show service status |
| `v2bx enable` · `disable` | toggle start-on-boot |
| `v2bx reload` | force an immediate panel resync (SIGHUP) |
| `v2bx log` | follow the service journal |
| `v2bx version` | print version |

## Configuration

A single JSON file (default `/etc/v2bx/config.json`). [`config.example.json`](config.example.json) has every node type worked out end to end; [docs/CONFIG.md](docs/CONFIG.md) is the field-by-field reference.

```jsonc
{
  "log":   { "level": "info", "output": "stdout" },
  "panel": { "api_host": "https://panel.example.com", "api_key": "…", "sync_interval_seconds": 60 },
  "nodes": [
    { "node_id": 1, "node_type": "shadowsocks", "enabled": true, "listen_ip": "0.0.0.0" }
  ]
}
```

## Build from source

```bash
go build -o v2bx ./cmd/v2bx      # needs Go 1.25+
go test ./...
```

Cross-compile for a release target:

```bash
GOOS=linux GOARCH=arm64 go build -o v2bx-linux-arm64 ./cmd/v2bx
```

Tagging `vX.Y.Z` triggers CI to cross-compile `linux/amd64`, `linux/arm64`, and `linux/armv7` and publish the tarballs to Releases.

## Contributing

New protocols and fixes are welcome — see **[CONTRIBUTING.md](CONTRIBUTING.md)**, especially the walkthrough for adding a protocol behind the `ProtocolServer` interface. This is a clean-room implementation: original design, no logic ported from other node agents; depending on protocol SDKs as Go modules is fine.

## License

[MIT](LICENSE) · built on the excellent [sing](https://github.com/sagernet/sing), [sing-quic](https://github.com/sagernet/sing-quic), [mieru](https://github.com/enfein/mieru), and [sing-anytls](https://github.com/anytls/sing-anytls) protocol libraries.
