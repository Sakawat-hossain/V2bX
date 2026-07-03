# Protocol notes

Status legend: **done** (implemented and tested), **planned** (registered in
the roadmap, not yet implemented).

## Shadowsocks — done

Supported ciphers (set via the panel, surfaced in agent config as
`NodeConfig.Cipher`):

- `aes-128-gcm`, `aes-192-gcm`, `aes-256-gcm`
- `chacha20-ietf-poly1305`
- `2022-blake3-aes-128-gcm`, `2022-blake3-aes-256-gcm`, `2022-blake3-chacha20-poly1305`

Notes:

- Classic AEAD ciphers derive the per-session key from an arbitrary-length
  password via the standard Shadowsocks HKDF construction — any password
  string works.
- Shadowsocks-2022 ciphers expect the **pre-shared key as base64**, sized to
  the cipher's key length before encoding: 16 bytes for
  `2022-blake3-aes-128-gcm`, 32 bytes for `2022-blake3-aes-256-gcm` and
  `2022-blake3-chacha20-poly1305`. Panels that generate 2022 passwords
  already produce a correctly-sized base64 string; if you're hand-rolling
  one, `openssl rand -base64 16` (or `32`) produces the right shape.
- Multi-user single-port operation is supported for both classic and 2022
  ciphers via the same PSK/password-keyed identity path — each panel user
  gets a distinct password on the same listener.
- UDP is relayed on a best-effort, single-round-trip basis per association.

## VMess — done

Raw TCP transport only (AEAD, `alterId 0`); WebSocket and gRPC transports
are not yet wired up — front the node with a fronting reverse proxy if you
need those. UDP-over-VMess is not yet implemented.

## VLess (XTLS/Vision) — done

Per-user `flow` (`""` or `xtls-rprx-vision`) comes from the panel's user
list (`User.Flow`). If `cert_mode: self` with `cert_file`/`key_file` is set
the listener terminates TLS itself; otherwise it expects TLS to already be
terminated in front of it. UDP-over-VLess is not yet implemented.

## Trojan — done

TLS is mandatory; only `cert_mode: self` with `cert_file`/`key_file` is
currently wired up — ACME (`http`/`dns`) automation is planned. The
password digest (SHA-224, lowercase hex) is compared as an opaque token
against every configured user; on mismatch the connection is dropped
silently rather than returning an error, matching Trojan's design goal of
being indistinguishable from a plain TLS server to anyone without a valid
password.

## Hysteria (v1) — planned

QUIC-based; needs a certificate. Self-signed certs work with clients set to
skip verification, matching the ecosystem convention for this protocol.

## Hysteria2 — planned

Same certificate requirements as Hysteria v1; wire format differs.

## TUIC — planned

QUIC-based; needs a certificate, same as Hysteria/Hysteria2.

## SOCKS5 — done

No TLS. Username/password auth (RFC 1929) is enabled automatically when the
node has a non-empty user list (matched against each user's `uuid`/`password`
fields); with no users configured, the listener accepts anonymous
connections. Only the `CONNECT` command is supported (no `BIND`/`UDP
ASSOCIATE` yet).

## Naive (NaiveProxy) — planned

HTTP/2 CONNECT tunneled over TLS — needs a real or self-signed certificate
to look like an ordinary HTTPS server to passive observers.

## HTTP — done

Plain HTTP proxy, no TLS at this layer (front it with a TLS-terminating
reverse proxy if needed). Supports `CONNECT` tunneling for HTTPS traffic and
direct forwarding for plain HTTP requests. Same optional Basic
Proxy-Authorization behavior as SOCKS5: auth is required only when the node
has users configured.

## Mieru — planned

## AnyTLS — planned

TLS required; config shape will mirror Trojan's cert handling once
implemented.
