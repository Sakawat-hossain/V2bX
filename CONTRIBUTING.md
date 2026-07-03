# Contributing to V2bX

Thanks for considering a contribution. V2bX is a clean-room Go implementation
of a V2board-family node agent — please keep new code that way: original
design, no ported logic from other node-agent projects. Depending on
well-known SDKs (Xray-core, sing-box, sing-shadowsocks, hysteria, etc.) as Go
modules is fine.

## Getting started

```bash
git clone https://github.com/Sakawat-hossain/V2bX.git
cd V2bX
go build ./...
go test ./...
```

## Adding a new protocol

1. Create `internal/protocol/<name>/`.
2. Implement `protocol.ProtocolServer` (`Start`, `Stop`, `Stats`, `Name`).
3. Register it in an `init()` via `protocol.Register("<name>", ...)`.
4. Blank-import the package from `cmd/v2bx/main.go`.
5. Add a smoke test that starts the server on an ephemeral port, opens a
   connection, and stops it cleanly.
6. Document any protocol-specific config quirks in `docs/PROTOCOLS.md` and
   add an example node entry to `config.example.json`.

## Before opening a PR

- `go vet ./...` and `go test ./...` must pass.
- `gofmt -l .` should print nothing.
- Keep commits focused; one milestone (protocol, CLI feature, doc) per PR
  where practical.

## Reporting issues

Open a GitHub issue with your panel type (XBoard/V2Board/other), the node
type involved, agent version (`v2bx version`), and relevant log output
(`v2bx log` or `journalctl -u v2bx`).
