#!/usr/bin/env bash
# install.sh — installer/updater/uninstaller for the V2bX node agent.
# Downloads the release binary matching this host's OS/arch from
# github.com/Sakawat-hossain/V2bX releases, installs a systemd unit and a
# starter config, and registers the "v2bx" CLI wrapper.
set -euo pipefail

REPO="Sakawat-hossain/V2bX"
INSTALL_DIR="/usr/local/bin"
CONFIG_DIR="/etc/v2bx"
UNIT_PATH="/etc/systemd/system/v2bx.service"
BIN_NAME="v2bx"

log()  { printf '\033[1;32m[v2bx]\033[0m %s\n' "$1"; }
err()  { printf '\033[1;31m[v2bx]\033[0m %s\n' "$1" >&2; }
fail() { err "$1"; exit 1; }

require_root() {
	if [ "$(id -u)" -ne 0 ]; then
		fail "this script must be run as root (try: sudo bash install.sh)"
	fi
}

detect_platform() {
	local os arch
	os="$(uname -s | tr '[:upper:]' '[:lower:]')"
	case "$os" in
		linux) ;;
		*) fail "unsupported OS: $os (only linux is supported)" ;;
	esac

	arch="$(uname -m)"
	case "$arch" in
		x86_64|amd64)  ARCH="amd64" ;;
		aarch64|arm64) ARCH="arm64" ;;
		armv7l)        ARCH="armv7" ;;
		*) fail "unsupported architecture: $arch" ;;
	esac
	OS="$os"
}

latest_version() {
	curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
		| grep '"tag_name"' | head -n1 | cut -d '"' -f4
}

download_binary() {
	local version="$1"
	local asset="v2bx-${OS}-${ARCH}.tar.gz"
	local url="https://github.com/${REPO}/releases/download/${version}/${asset}"
	local tmp
	tmp="$(mktemp -d)"

	log "downloading ${asset} (${version})"
	curl -fsSL "$url" -o "${tmp}/${asset}" || fail "download failed: ${url}"
	tar -xzf "${tmp}/${asset}" -C "${tmp}"

	install -m 0755 "${tmp}/${BIN_NAME}" "${INSTALL_DIR}/${BIN_NAME}"
	rm -rf "${tmp}"
	log "installed ${INSTALL_DIR}/${BIN_NAME}"
}

install_config() {
	mkdir -p "${CONFIG_DIR}"
	if [ -f "${CONFIG_DIR}/config.json" ]; then
		log "existing config found at ${CONFIG_DIR}/config.json, leaving it untouched"
		return
	fi
	if [ -f "config.example.json" ]; then
		cp config.example.json "${CONFIG_DIR}/config.json"
	else
		curl -fsSL "https://raw.githubusercontent.com/${REPO}/main/config.example.json" \
			-o "${CONFIG_DIR}/config.json"
	fi
	log "wrote starter config to ${CONFIG_DIR}/config.json — edit panel.api_host and panel.api_key before starting"
}

install_systemd_unit() {
	if [ -f "systemd/v2bx.service" ]; then
		cp "systemd/v2bx.service" "${UNIT_PATH}"
	else
		curl -fsSL "https://raw.githubusercontent.com/${REPO}/main/systemd/v2bx.service" \
			-o "${UNIT_PATH}"
	fi
	systemctl daemon-reload
	log "installed systemd unit at ${UNIT_PATH}"
}

do_install() {
	require_root
	detect_platform
	local version="${1:-$(latest_version)}"
	[ -n "$version" ] || fail "could not determine latest release version"
	download_binary "$version"
	install_config
	install_systemd_unit
	log "install complete. Edit ${CONFIG_DIR}/config.json, then run: systemctl enable --now v2bx"
}

do_update() {
	require_root
	detect_platform
	local version="${1:-$(latest_version)}"
	[ -n "$version" ] || fail "could not determine latest release version"
	systemctl stop v2bx 2>/dev/null || true
	download_binary "$version"
	systemctl start v2bx 2>/dev/null || true
	log "update complete (${version})"
}

do_uninstall() {
	require_root
	systemctl disable --now v2bx 2>/dev/null || true
	rm -f "${UNIT_PATH}"
	rm -f "${INSTALL_DIR}/${BIN_NAME}"
	systemctl daemon-reload
	log "binary and systemd unit removed. Config left at ${CONFIG_DIR} — remove manually if no longer needed."
}

case "${1:-install}" in
	install)   do_install "${2:-}" ;;
	update)    do_update "${2:-}" ;;
	uninstall) do_uninstall ;;
	*) fail "usage: $0 [install|update|uninstall] [version]" ;;
esac
