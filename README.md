<div align="center">

<img src=".github/assets/banner.png" alt="V2bX" width="820">

### One Go binary. Twelve protocols. Built for the real world.

A clean-room node agent for **XBoard**, **V2Board**, and anything else that speaks the UniProxy node API — it pulls your node config and subscriber list on an interval, runs the listeners, reports traffic and online devices back, and holds up under DPI, throttling, and active probing.

<br>

[![CI](https://img.shields.io/github/actions/workflow/status/Sakawat-hossain/V2bX/ci.yml?branch=main&style=flat-square&label=CI&labelColor=0B0E14&color=0AB2F9)](https://github.com/Sakawat-hossain/V2bX/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/Sakawat-hossain/V2bX?style=flat-square&label=release&labelColor=0B0E14&color=7C3AED)](https://github.com/Sakawat-hossain/V2bX/releases)
[![Go](https://img.shields.io/badge/Go-1.25-0AB2F9?style=flat-square&labelColor=0B0E14&logo=go&logoColor=white)](go.mod)
[![Protocols](https://img.shields.io/badge/protocols-12-7C3AED?style=flat-square&labelColor=0B0E14)](docs/PROTOCOLS.md)
[![License](https://img.shields.io/badge/license-MIT-A16FEE?style=flat-square&labelColor=0B0E14)](LICENSE)

**English** · [简体中文](README.zh-CN.md)

</div>

---

## Protocols

Every node type runs independently and can be toggled per node. They're grouped by **how each one secures the wire** — the axis that decides what a node needs (a cert, a cipher, nothing) and how it looks on the network.

| Node type | Wire | Needs cert | Highlights |
|-----------|------|:----------:|------------|
| **Shadowsocks** | Self-encrypted TCP (AEAD) | — | 7 ciphers incl. Shadowsocks-2022 blake3 |
| **VMess** | Self-encrypted TCP (AEAD) | — | Single-port multi-user |
| **VLess** | Self-encrypted TCP | optional | XTLS/Vision flow · **Reality** · **WebSocket/CDN** |
| **Trojan** | TLS-wrapped TCP | auto | SHA-224 auth · **decoy fallback** |
| **Naive** | TLS-wrapped HTTP/2 CONNECT | auto | HTTP Basic per user |
| **AnyTLS** | TLS-wrapped session | auto | Padding scheme, SHA-256 auth |
| **Hysteria** | QUIC / UDP | auto | Brutal bandwidth · **port hopping** |
| **Hysteria2** | QUIC / UDP | auto | Brutal · **Salamander obfs** · port hopping |
| **TUIC** | QUIC / UDP | auto | UUID + password · port hopping |
| **SOCKS5** | Plaintext | — | Optional user/password auth |
| **HTTP** | Plaintext | — | CONNECT + forward, optional auth |
| **Mieru** | Obfuscated transport | — | TCP or UDP transport |

For any TLS/QUIC node, a self-signed certificate is generated automatically when you don't supply one. Full protocol notes: **[docs/PROTOCOLS.md](docs/PROTOCOLS.md)**.

Adding a protocol never touches the panel-sync or CLI layers — each backend satisfies one small interface and registers itself:

```go
type ProtocolServer interface {
    Start(cfg NodeConfig) error
    Stop() error
    Stats() UsageStats
    Name() string
}
```

## Built for production

- **Traffic accounting that doesn't lose money** — usage is reported as a delta since the panel's last acknowledgement; a failed push is retried in full, never dropped.
- **Hot user reload** — adding or removing a subscriber updates the node *in place* (where the codec supports it) instead of dropping every active connection each sync.
- **Device / IP limits** — the node reports each user's source IPs via `/alive`; the panel enforces the limit across your whole fleet.
- **Per-user speed limits** — a shared token bucket per user, so opening more connections can't multiply their bandwidth.
- **Efficient sync** — conditional `GET` with ETag/`304`, so unchanged config/user pulls transfer an empty body.
- **Metrics** — an optional Prometheus `/metrics` endpoint (nodes, online users, traffic, panel push/sync health).
- **Safety valves** — optional per-node connection cap, pooled relay buffers, and a **hardened systemd unit** (dropped capabilities, read-only filesystem, syscall filter).

## Built for restricted regions

- **Reality** (VLESS) — borrows a real site's TLS handshake; any connection that isn't an authorized client is transparently proxied to that real site, so an active prober finds a genuine website, not a proxy. Fails closed on a partial config.
- **Brutal congestion control** (Hysteria/Hysteria2) — `up_mbps`/`down_mbps` cap the rate and enable Brutal, which ignores packet loss, so throughput holds up on links the network throttles by injecting loss.
- **Salamander obfuscation** (Hysteria2) — hides the QUIC handshake from DPI classifiers.
- **Port hopping** (Hysteria/Hysteria2/TUIC) — the agent installs an `iptables` redirect from a UDP port range to the node port, so clients spray the range to evade per-flow throttling and single-port blocking.
- **Trojan decoy fallback** — forward unauthenticated connections to a real backend instead of resetting them.
- **VLESS-WebSocket** — run behind a CDN (e.g. Cloudflare) whose IPs are hard to block.

> ⚠️ **Deploy anti-censorship features canary-first.** A wrong Reality/CDN config is a stable fingerprint that can get every IP sharing it blocked at once — roll out to one or two nodes and watch them before going fleet-wide. See [docs/PROTOCOLS.md](docs/PROTOCOLS.md).

## Panel compatibility

V2bX speaks the UniProxy HTTP API shared across the V2board family:

| Call | Purpose |
|------|---------|
| `GET  {config_path}` | node config — protocol, port, cipher, TLS |
| `GET  {user_path}` | the node's current subscriber list |
| `POST {push_path}` | per-user traffic usage |
| `POST {alive_path}` | currently-online users / device IPs |

Base URL, API key, and all four paths are **config-driven**, so one binary targets XBoard, V2Board, or any compatible fork without a code change. If the panel goes briefly unreachable, sync retries with exponential backoff and every node keeps serving on its last-known-good config.

## Quick install

```bash
curl -fsSL https://raw.githubusercontent.com/Sakawat-hossain/V2bX/main/install.sh -o install.sh
sudo bash install.sh install
```

That drops the binary at `/usr/local/bin/v2bx`, installs the hardened systemd unit, and offers to run the **interactive config wizard** — answer a few prompts (panel URL, API key, node type) and it writes `/etc/v2bx/config.json` and starts the service. No hand-editing JSON to get going.

Prefer a menu to memorizing commands? Just run `sudo v2bx`.

## Commands

Run `v2bx` with no arguments for an interactive menu, or use any command directly:

| Command | Does |
|---------|------|
| `v2bx` | open the interactive menu |
| `v2bx generate` · `add` · `del` · `edit` | build / edit the config (`-c PATH` optional) |
| `v2bx server [-c PATH]` | run the agent in the foreground (what systemd runs) |
| `v2bx start` · `stop` · `restart` · `status` | manage the systemd service |
| `v2bx enable` · `disable` | toggle start-on-boot |
| `v2bx reload` | force an immediate panel resync (SIGHUP) |
| `v2bx log` | follow the service journal |
| `v2bx update` | update to the latest release in place |
| `v2bx x25519` | generate an X25519 key pair (Reality) |
| `v2bx bbr` · `firewall` | enable BBR · open inbound ports |
| `v2bx uninstall` · `version` | remove the service · print version |

## Docker

A multi-arch image (`linux/amd64`, `linux/arm64`) is published to GHCR on every release.

```bash
mkdir config
docker run --rm -it -v "$PWD/config:/etc/v2bx" \
  ghcr.io/sakawat-hossain/v2bx:latest generate   # writes config, one time
docker compose up -d                             # see docker-compose.yml
```

## Configuration

A single JSON file (default `/etc/v2bx/config.json`). [`config.example.json`](config.example.json) has every node type worked out end to end; [docs/CONFIG.md](docs/CONFIG.md) is the field-by-field reference.

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

## Build from source

```bash
go build -o v2bx ./cmd/v2bx      # needs Go 1.25+
go test ./...
```

Tagging `vX.Y.Z` triggers CI to cross-compile `linux/amd64`, `linux/arm64`, and `linux/armv7`, publish the tarballs to Releases, and push a multi-arch Docker image.

## Contributing

New protocols and fixes are welcome — see **[CONTRIBUTING.md](CONTRIBUTING.md)**, especially the walkthrough for adding a protocol behind the `ProtocolServer` interface. This is a clean-room implementation: original design, no logic ported from other node agents; depending on protocol SDKs as Go modules is fine.

## License

[MIT](LICENSE) · built on the excellent [sing](https://github.com/sagernet/sing), [sing-quic](https://github.com/sagernet/sing-quic), [reality](https://github.com/sagernet/reality), [mieru](https://github.com/enfein/mieru), and [sing-anytls](https://github.com/anytls/sing-anytls) libraries.
