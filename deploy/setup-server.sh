#!/bin/sh
# Installs or updates GameBoard on a Debian VM. Run as root from the directory
# holding the uploaded files. Safe to re-run: deploy.ps1 runs it on every deploy.
set -eu
cd "$(dirname "$0")"

if ! command -v caddy >/dev/null || ! command -v sqlite3 >/dev/null; then
	apt-get update -q
	DEBIAN_FRONTEND=noninteractive apt-get install -y -q caddy sqlite3
fi

id gameboard >/dev/null 2>&1 || useradd --system --home-dir /var/lib/gameboard --shell /usr/sbin/nologin gameboard
install -d -o gameboard -g gameboard -m 750 /var/lib/gameboard /var/lib/gameboard/backups
install -d -m 755 /opt/gameboard
install -d -m 750 -g gameboard /etc/gameboard
[ -f /etc/gameboard/env ] || install -m 640 -g gameboard /dev/null /etc/gameboard/env

install -m 755 gameboard /opt/gameboard/gameboard
install -m 644 gameboard.service gameboard-backup.service gameboard-backup.timer /etc/systemd/system/
install -m 644 Caddyfile /etc/caddy/Caddyfile
install -m 755 gameboard-admin /usr/local/bin/gameboard-admin

systemctl daemon-reload
systemctl enable --now gameboard-backup.timer
systemctl enable gameboard.service
systemctl restart gameboard.service
systemctl enable caddy
systemctl reload-or-restart caddy

sleep 1
systemctl is-active gameboard.service caddy
