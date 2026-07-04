# Configuration reference

V2bX reads a single JSON config file (default path `/etc/v2bx/config.json`,
override with `v2bx server -c <path>`). See
[`config.example.json`](../config.example.json) for a fully worked example
covering every supported node type.

The fastest way to a working file is `v2bx generate` — an interactive wizard
that prompts for your panel and node details and writes a valid config. This
page documents every field for when you want to edit it directly.

## Top level

| Field   | Type         | Required | Description |
|---------|--------------|----------|-------------|
| `log`   | object       | no       | See [Log](#log). Defaults to `info`/`stdout`. |
| `panel` | object       | yes      | See [Panel](#panel). |
| `nodes` | array of Node | yes (≥1) | Nodes this agent instance runs. |

## Log

| Field    | Type   | Default  | Description |
|----------|--------|----------|-------------|
| `level`  | string | `info`   | One of `debug`, `info`, `warn`, `error`. |
| `output` | string | `stdout` | `"stdout"` or a file path to append to. |

## Panel

| Field                    | Type   | Required | Default | Description |
|--------------------------|--------|----------|---------|-------------|
| `api_host`                | string | yes      | —       | Base URL of the panel, e.g. `https://panel.example.com`. |
| `api_key`                 | string | yes      | —       | The node communication key/token from the panel's node settings. |
| `sync_interval_seconds`   | int    | no       | 60      | How often to re-fetch config/users from the panel. |
| `config_path`              | string | no       | `/api/v1/server/UniProxy/config` | Override if your panel exposes the UniProxy contract at a different route. |
| `user_path`                | string | no       | `/api/v1/server/UniProxy/user`   | Same, for the user-list endpoint. |
| `push_path`                | string | no       | `/api/v1/server/UniProxy/push`   | Same, for the traffic-push endpoint. |
| `alive_path`               | string | no       | `/api/v1/server/UniProxy/alive`  | Same, for the online-report endpoint. |

These paths are overridable, not because the API surface changes across
panels, but so any fork or reverse proxy that renames routes still works
without a code change.

## Metrics

Optional Prometheus-compatible metrics endpoint.

| Field    | Type   | Default | Description |
|----------|--------|---------|-------------|
| `listen` | string | `""`    | Address to serve `/metrics` on, e.g. `127.0.0.1:9095`. Empty disables it. |

The endpoint is **unauthenticated** — bind it to localhost or a private
interface and scrape over that. It exposes `v2bx_up`, `v2bx_build_info`,
per-node user/online/traffic gauges and counters, and panel push/sync
success/failure counters.

## Node

Each entry in `nodes` is one locally-run listener. Protocol type, listen
port, cipher, and the user list itself all come from the panel on every
sync — the fields below are agent-side overrides layered on top.

| Field       | Type    | Required | Description |
|-------------|---------|----------|-------------|
| `node_id`    | int64   | yes      | Must match the node ID configured in the panel. Unique within this file. |
| `node_type`  | string  | yes      | One of: `shadowsocks`, `vmess`, `vless`, `trojan`, `hysteria`, `hysteria2`, `tuic`, `socks5`, `naive`, `http`, `mieru`, `anytls`. |
| `enabled`    | bool    | no       | Set `false` to keep the entry in the file but not run it. |
| `listen_ip`  | string  | no       | Interface to bind, e.g. `0.0.0.0` or `127.0.0.1`. Defaults to all interfaces. |
| `cert_mode`  | string  | no       | `none`, `http` (HTTP-01), `dns` (DNS-01), or `self` (bring your own cert). Ignored by protocols that don't use TLS. |
| `cert_file`  | string  | when `cert_mode=self` | Path to a PEM certificate. |
| `key_file`   | string  | when `cert_mode=self` | Path to the matching PEM private key. |
| `tfo`        | bool    | no       | Enable TCP Fast Open where supported. |
| `sniffing`   | bool    | no       | Enable destination sniffing (SNI/HTTP Host) where supported. |
| `limits`     | object  | no       | See [Limits](#limits). |

## Limits

Defaults applied when the panel doesn't specify a per-user value.

| Field                       | Type   | Description |
|------------------------------|--------|-------------|
| `default_speed_limit_bytes`  | uint64 | Bytes/sec cap applied to users with no panel-specified limit. `0` = unlimited. |
| `device_limit`                | int    | Max simultaneous devices per user. `0` = unlimited. |
| `ip_limit`                    | int    | Max simultaneous IPs per user. `0` = unlimited. |
| `traffic_reset_day`           | int    | Day-of-month traffic counters reset. `0` = defer to the panel. |

## Panel outages

If the panel is briefly unreachable, sync retries with exponential backoff
(1s, 2s, 4s, ... capped at 60s) and every node keeps running on its
last-known-good config — nodes are never torn down just because a sync
attempt failed.
