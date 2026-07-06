<div align="center">

# V2bX

### 多核心机场节点后端（Xray · sing-box · Hysteria2），对接 XBoard / V2Board

面向 **XBoard**、**V2Board** 以及任何实现 UniProxy 节点 API 的面板的多协议节点代理：按间隔从面板拉取节点配置与用户列表、启动各协议入站、回报流量与在线设备。内置 **Xray-core**、**sing-box**、**Hysteria2** 三套核心，原生支持 **Reality** 与 **XTLS-Vision**。

[![Release](https://img.shields.io/github/v/release/Sakawat-hossain/V2bX?style=flat-square&label=release&labelColor=0B0E14&color=7C3AED)](https://github.com/Sakawat-hossain/V2bX/releases)
[![Go](https://img.shields.io/badge/Go-1.26-0AB2F9?style=flat-square&labelColor=0B0E14&logo=go&logoColor=white)](go.mod)
[![License](https://img.shields.io/badge/license-MPL--2.0-A16FEE?style=flat-square&labelColor=0B0E14)](LICENSE)

**简体中文（主文档）** · [English](README.md)

</div>

---

## 特性

- **多核心**：`xray`（Xray-core）、`sing`（sing-box）、`hysteria2`，可按节点独立选择。
- **协议**：Shadowsocks、VLESS（含 **Reality + XTLS-Vision**）、VMess、Trojan、Hysteria2、SOCKS，以及各核心支持的 TUIC / AnyTLS 等。
- **面板对接**：XBoard / V2Board 的 UniProxy API —— 拉取节点配置与用户、上报流量、上报在线设备 IP。
- **证书**：`none` / `self` / `http` / `dns`（ACME，可选 DNS Provider）/ `reality`。
- **部署**：单二进制 + systemd，支持 Linux `amd64 / arm64 / armv7 / armv6 / armv5 / s390x / riscv64`，可跑于 Ubuntu / Debian / CentOS / Alpine / Arch。

## 安装

一键脚本（Linux，需 root）：

```bash
wget -N https://raw.githubusercontent.com/Sakawat-hossain/V2bX/main/V2bX-script-master/install.sh && bash install.sh
```

> 注意：随附脚本默认从上游 Release 拉取二进制。发布你自己仓库的 Release 后，请相应修改脚本中的下载地址。

也可从源码构建（见文末）。

## 配置

默认配置文件 `/etc/V2bX/config.json`；完整示例见 [example/config.json](example/config.json)。核心结构如下（键名区分大小写）：

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
      "ApiKey": "面板节点通信密钥 / 令牌",
      "NodeID": 1,
      "NodeType": "vless",
      "ListenIP": "0.0.0.0",
      "EnableSniff": true,
      "CertConfig": { "CertMode": "none" }
    }
  ]
}
```

- **`Cores`**：启用的核心列表（`sing` / `xray` / `hysteria2`），每个节点用 `Core` 字段指定用哪一个。
- **`NodeType`**：`shadowsocks` / `vless` / `vmess` / `trojan` / `hysteria2` / `socks` 等，须与面板一致。
- **`CertConfig.CertMode`**：`none` / `self` / `http` / `dns` / `reality`。
- **VLESS-Reality / XTLS-Vision**：在面板的节点上配置（dest、密钥、Short ID、flow），由核心自动处理；本代理会从面板下发的配置中读取，无需手写密钥。

## 命令

| 命令 | 作用 |
|------|------|
| `V2bX server -c /etc/V2bX/config.json` | 前台运行节点（systemd 实际执行的命令） |
| `V2bX start` · `stop` · `restart` | 管理 systemd 服务 |
| `V2bX log` | 查看服务日志 |
| `V2bX x25519` | 生成 Reality X25519 密钥对 |
| `V2bX synctime` | 校准系统时间（Reality 对时间敏感） |
| `V2bX update` · `uninstall` | 更新 / 卸载 |
| `V2bX version` | 查看版本 |

## 从源码构建

```bash
go build -o V2bX      # 需要 Go 1.26+
```

运行时需要 `geoip.dat` / `geosite.dat`（以及可选的 `geoip.db` / `geosite.db`）等地理数据文件——这些由安装脚本或部署时下载，不随仓库分发。

## 维护者

Sakawat Hossain

## 许可证

[MPL-2.0](LICENSE)。本项目基于 [Shannon-x/V2bX](https://github.com/Shannon-x/V2bX)（[InazumaV/V2bX](https://github.com/InazumaV/V2bX) 的分支）构建，依据 Mozilla Public License 2.0 分发；按许可证要求保留上游署名与许可证声明。
