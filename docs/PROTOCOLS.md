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

## VMess — planned

## VLess (XTLS/Vision) — planned

Flow (`xtls-rprx-vision` or none) is expected to travel as a per-user field
from the panel's user list once implemented.

## Trojan — planned

TLS is mandatory; `cert_mode: self` with `cert_file`/`key_file` is the
straightforward path until ACME (`http`/`dns`) automation lands.

## Hysteria (v1) — planned

QUIC-based; needs a certificate. Self-signed certs work with clients set to
skip verification, matching the ecosystem convention for this protocol.

## Hysteria2 — planned

Same certificate requirements as Hysteria v1; wire format differs.

## TUIC — planned

QUIC-based; needs a certificate, same as Hysteria/Hysteria2.

## SOCKS5 — planned

No TLS. Straightforward relay once implemented.

## Naive (NaiveProxy) — planned

HTTP/2 CONNECT tunneled over TLS — needs a real or self-signed certificate
to look like an ordinary HTTPS server to passive observers.

## HTTP — planned

Plain HTTP CONNECT proxy, no TLS.

## Mieru — planned

## AnyTLS — planned

TLS required; config shape will mirror Trojan's cert handling once
implemented.
