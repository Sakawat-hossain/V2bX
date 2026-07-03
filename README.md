# V2bX

A multi-protocol node agent for V2board-family panels — **XBoard**,
**V2Board**, and any panel implementing the same UniProxy node-communication
API. Written from scratch in Go: original config format, package layout, and
panel client, built on top of well-known protocol SDKs.

## Supported protocols

| Protocol                | Status |
|--------------------------|--------|
| Shadowsocks (AEAD + 2022) | ✅ done |
| SOCKS5                    | ✅ done |
| HTTP                      | ✅ done |
| Trojan                    | ✅ done |
| VMess                     | ✅ done |
| VLess (XTLS/Vision)       | ✅ done |
| Hysteria (v1)             | ✅ done |
| Hysteria2                 | ✅ done |
| TUIC                      | ✅ done |
| Naive (NaiveProxy)        | ✅ done |
| Mieru                     | ✅ done |
| AnyTLS                    | ✅ done |

Shadowsocks covers `aes-128-gcm`, `aes-192-gcm`, `aes-256-gcm`,
`chacha20-ietf-poly1305`, and the three Shadowsocks-2022 blake3 ciphers, with
multi-user single-port support and per-user traffic accounting. See
[docs/PROTOCOLS.md](docs/PROTOCOLS.md) for protocol-specific notes.

Every protocol runs behind the same interface:

```go
type ProtocolServer interface {
    Start(cfg NodeConfig) error
    Stop() error
    Stats() UsageStats
    Name() string
}
```

so new protocols slot in without touching panel-sync or CLI code.

## Panel compatibility

V2bX speaks the UniProxy-shaped HTTP API common to V2board-family panels:

- `GET  {config_path}` — node configuration (protocol, port, cipher, TLS settings)
- `GET  {user_path}`   — the node's current subscriber list
- `POST {push_path}`   — per-user traffic usage
- `POST {alive_path}`  — currently-online users

Base URL, API key, and all four paths are config-driven (see
[docs/CONFIG.md](docs/CONFIG.md)), so the same binary works against XBoard,
V2Board, or any compatible fork without code changes. If the panel is
briefly unreachable, sync retries with exponential backoff and every running
node keeps serving on its last-known-good config.

## Quick install

```bash
curl -fsSL https://raw.githubusercontent.com/Sakawat-hossain/V2bX/main/install.sh -o install.sh
sudo bash install.sh install
```

This installs the binary to `/usr/local/bin/v2bx`, a starter config at
`/etc/v2bx/config.json`, and a systemd unit. Edit the config
(`panel.api_host`, `panel.api_key`, and your `nodes` list), then:

```bash
sudo v2bx enable   # start at boot
sudo v2bx start
sudo v2bx status
sudo v2bx log       # follow the journal
```

Update or remove:

```bash
sudo bash install.sh update
sudo bash install.sh uninstall
```

## CLI

```
v2bx server [-c /etc/v2bx/config.json]   run the agent in the foreground
v2bx start|stop|restart|status           manage the systemd service
v2bx enable|disable                      toggle start-at-boot
v2bx reload                              force an immediate panel resync (SIGHUP)
v2bx log                                 follow the service journal
v2bx version                             print version information
```

## Configuration

See [`config.example.json`](config.example.json) for a fully worked example
covering every node type, and [docs/CONFIG.md](docs/CONFIG.md) for the
complete field reference.

## Building from source

```bash
go build -o v2bx ./cmd/v2bx
go vet ./...
go test ./...
```

Cross-compiling for another target:

```bash
GOOS=linux GOARCH=arm64 go build -o v2bx-linux-arm64 ./cmd/v2bx
```

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md), particularly the section on adding a
new protocol.

## License

[MIT](LICENSE)
