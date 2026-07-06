<div align="center">

# V2bX

### Multi-core node backend (Xray · sing-box · Hysteria2) for XBoard / V2Board

A multi-protocol node agent for **XBoard**, **V2Board**, and anything that speaks the UniProxy node API — it pulls node config and the subscriber list from the panel on an interval, runs the protocol inbounds, and reports traffic and online devices back. It embeds the **Xray-core**, **sing-box**, and **Hysteria2** cores, with native **Reality** and **XTLS-Vision** support.

[![Release](https://img.shields.io/github/v/release/Sakawat-hossain/V2bX?style=flat-square&label=release&labelColor=0B0E14&color=7C3AED)](https://github.com/Sakawat-hossain/V2bX/releases)
[![Go](https://img.shields.io/badge/Go-1.26-0AB2F9?style=flat-square&labelColor=0B0E14&logo=go&logoColor=white)](go.mod)
[![License](https://img.shields.io/badge/license-MPL--2.0-A16FEE?style=flat-square&labelColor=0B0E14)](LICENSE)

📖 **The primary documentation is in Simplified Chinese — see [简体中文（主文档）](README.zh-CN.md).** This English page is a secondary translation.

[简体中文（主文档）](README.zh-CN.md) · **English**

</div>

---

## Features

- **Multiple cores** — `xray` (Xray-core), `sing` (sing-box), and `hysteria2`, selectable per node.
- **Protocols** — Shadowsocks, VLESS (incl. **Reality + XTLS-Vision**), VMess, Trojan, Hysteria2, SOCKS, plus TUIC / AnyTLS where the core supports them.
- **Panel integration** — the XBoard / V2Board UniProxy API: fetch node config and users, report traffic, report online device IPs.
- **Certificates** — `none` / `self` / `http` / `dns` (ACME, optional DNS provider) / `reality`.
- **Deployment** — single binary + systemd, for Linux `amd64 / arm64 / armv7 / armv6 / armv5 / s390x / riscv64`, on Ubuntu / Debian / CentOS / Alpine / Arch.

## Install

One-line script (Linux, as root):

```bash
wget -N https://raw.githubusercontent.com/Sakawat-hossain/V2bX/main/V2bX-script-master/install.sh && bash install.sh
```

> Note: the bundled script pulls the binary from the upstream releases by default. After you publish releases on your own repo, update the download URL in the script accordingly.

Or build from source (see the bottom of this page).

## Configuration

The default config file is `/etc/V2bX/config.json`; see [example/config.json](example/config.json) for a full example. The shape (keys are case-sensitive):

```jsonc
{
  "Log": { "Level": "info", "Output": "" },
  "Cores": [
    {
      "Type": "sing",
      "Log": { "Level": "info", "Timestamp": true },
      "OriginalPath": "/etc/V2bX/sing_origin.json"
    }
  ],
  "Nodes": [
    {
      "Core": "sing",
      "ApiHost": "https://panel.example.com",
      "ApiKey": "panel node communication key / token",
      "NodeID": 1,
      "NodeType": "vless",
      "ListenIP": "0.0.0.0",
      "EnableSniff": true,
      "CertConfig": { "CertMode": "none" }
    }
  ]
}
```

- **`Cores`** — the cores you enable (`sing` / `xray` / `hysteria2`); each node picks one via its `Core` field.
- **`NodeType`** — `shadowsocks` / `vless` / `vmess` / `trojan` / `hysteria2` / `socks`, matching the panel.
- **`CertConfig.CertMode`** — `none` / `self` / `http` / `dns` / `reality`.
- **VLESS-Reality / XTLS-Vision** — configured on the node in the panel (dest, keys, short ID, flow) and handled by the core; the agent reads it from the panel config, so you don't hand-write keys.

## Commands

| Command | Does |
|---------|------|
| `V2bX server -c /etc/V2bX/config.json` | run the agent in the foreground (what systemd runs) |
| `V2bX start` · `stop` · `restart` | manage the systemd service |
| `V2bX log` | view the service log |
| `V2bX x25519` | generate a Reality X25519 key pair |
| `V2bX synctime` | sync system time (Reality is time-sensitive) |
| `V2bX update` · `uninstall` | update / remove |
| `V2bX version` | print version |

## Build from source

```bash
go build -o V2bX      # needs Go 1.26+
```

At runtime it needs the geo data files (`geoip.dat` / `geosite.dat`, and optionally `geoip.db` / `geosite.db`) — these are fetched by the install script or at deploy time and are not shipped in the repo.

## Maintainer

Sakawat Hossain

## License

[MPL-2.0](LICENSE). This project is based on [Shannon-x/V2bX](https://github.com/Shannon-x/V2bX) (a fork of [InazumaV/V2bX](https://github.com/InazumaV/V2bX)) and distributed under the Mozilla Public License 2.0; the upstream attribution and license notices are retained as the license requires.
