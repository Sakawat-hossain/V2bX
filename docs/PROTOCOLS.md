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

### VLESS-Reality

Add a `reality` block to a `vless` node to use the Reality transport, which
defeats **active probing**: the node borrows a real site's TLS handshake, and
any connection that isn't an authorized Reality client is transparently
proxied to that real site — so a censor probing the IP finds a genuine
website, not a proxy.

```jsonc
"reality": {
  "dest": "www.microsoft.com:443",       // real site to impersonate (must be reachable)
  "server_names": ["www.microsoft.com"], // SNIs to accept (valid for dest)
  "private_key": "<base64url x25519>",    // from `v2bx x25519`; give clients the public key
  "short_ids": ["", "0123456789abcdef"]  // hex, 0-16 chars; "" allows the empty id
}
```

Generate the keypair with `v2bx x25519` — put the **private** key here and the
**public** key in the panel/client. When `reality` is set the node ignores
`cert_mode`/cert files (Reality does its own handshake) and requires `dest`,
`server_names`, and `private_key` — a partial config is **rejected** rather
than served as a detectable handshake.

**Deploy carefully.** A wrong Reality config is a stable fingerprint that gets
every IP sharing it blocked at once. Pick a `dest` that is popular, allows TLS
1.3 + HTTP/2, and is *not* behind the same CDN as many other proxies; make
`server_names` match it. **Canary on one or two IPs first** and watch them
before rolling out fleet-wide.

## Trojan — done

TLS is mandatory (a self-signed cert is auto-generated when no
`cert_file`/`key_file` is given). The password digest (SHA-224, lowercase
hex) is compared as an opaque token against every configured user.

**Decoy fallback.** By default an unauthenticated connection is dropped. A
TLS server that resets everything without a valid password is itself a mild
tell to an active prober, so set `fallback` to a decoy backend (`host:port`,
e.g. a local web server serving a real page): probes and browsers are then
transparently forwarded there — including the bytes already read — and see a
genuine website instead of a proxy that hangs up. This is the
poor-man's version of Reality; for the strongest anti-probing, prefer
[VLESS-Reality](#vless-reality).

## Hysteria (v1) — done

QUIC-based; requires `cert_file`/`key_file` (self-signed is fine — clients
typically skip verification for this protocol, matching ecosystem
convention). Server-side bandwidth is fixed at 1 Gbps send/receive
internally since the config schema doesn't yet expose a per-node bandwidth
knob; actual throughput is still governed by the client's own congestion
control and any `limits.default_speed_limit_bytes` enforcement layered on
top in the future.

## Hysteria2 — done

Same certificate requirements as Hysteria v1; wire format differs and there
is no bandwidth negotiation step, so no equivalent BPS setting is needed.

## TUIC — done

QUIC-based; requires `cert_file`/`key_file` like Hysteria/Hysteria2. Each
user needs both a UUID and a password (TUIC authenticates on the pair).

## SOCKS5 — done

No TLS. Username/password auth (RFC 1929) is enabled automatically when the
node has a non-empty user list (matched against each user's `uuid`/`password`
fields); with no users configured, the listener accepts anonymous
connections. Only the `CONNECT` command is supported (no `BIND`/`UDP
ASSOCIATE` yet).

## Naive (NaiveProxy) — done

HTTP/2 `CONNECT` tunneled over TLS — requires `cert_file`/`key_file` (a real
cert makes it blend in as an ordinary HTTPS site; self-signed works if the
client is configured to trust it). Each user needs a UUID (used as the HTTP
Basic username) and password; requests without valid `Proxy-Authorization`
get a `407`. The optional length-padding scheme naive clients can negotiate
is **not** implemented — plain h2 CONNECT relay only, which interoperates
with naive clients that don't require padding.

## HTTP — done

Plain HTTP proxy, no TLS at this layer (front it with a TLS-terminating
reverse proxy if needed). Supports `CONNECT` tunneling for HTTPS traffic and
direct forwarding for plain HTTP requests. Same optional Basic
Proxy-Authorization behavior as SOCKS5: auth is required only when the node
has users configured.

## Mieru — done

Backed by the `enfein/mieru/v3` embedding API. Each user's UUID is used as
the mieru username and the panel-issued password as the mieru password;
mieru enforces its own password-strength rules, so weak passwords may be
rejected at start. Transport defaults to TCP — set the node's
`Extra["transport"]` to `"UDP"` to bind a UDP mieru transport instead. Only
TCP `CONNECT` is relayed; UDP-associate requests are declined for now. No
TLS certificate is needed (mieru has its own obfuscated transport).

## AnyTLS — done

Backed by the `anytls/sing-anytls` session library. Requires
`cert_file`/`key_file` — the listener terminates TLS and hands the plaintext
stream to the AnyTLS session layer, which authenticates on SHA-256 of the
user's password. The default AnyTLS padding scheme is used. The panel UUID
(or the numeric user ID if no UUID) is the display name used for stats
attribution.
