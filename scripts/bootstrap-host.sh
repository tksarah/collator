#!/bin/sh
set -eu

if [ "$(id -u)" -ne 0 ]; then echo "Run this reviewed script with sudo." >&2; exit 1; fi
root="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
binary="$root/release/bin/shiden-guardian-agent"
apply_journald=0
for arg in "$@"; do
  case "$arg" in
    --apply-journald-limits) apply_journald=1 ;;
    --agent-binary=*) binary="${arg#*=}" ;;
    *) echo "Unknown option: $arg" >&2; exit 64 ;;
  esac
done
if [ ! -f "$binary" ]; then echo "Agent binary not found: $binary" >&2; exit 1; fi
if [ "$(systemctl show astar.service --property=LoadState --value)" != "loaded" ]; then echo "astar.service is not loaded" >&2; exit 1; fi

stamp="$(date -u +%Y%m%dT%H%M%SZ)"
backup_dir="/var/backups/shiden-guardian/$stamp"
install -d -o root -g root -m 0700 "$backup_dir"
for target in \
  /usr/local/bin/shiden-guardian-agent \
  /usr/local/libexec/shiden-guardian-restart \
  /etc/sudoers.d/shiden-guardian \
  /etc/tmpfiles.d/shiden-guardian.conf \
  /etc/systemd/system/shiden-guardian-agent.service \
  /etc/systemd/system/shiden-guardian-smtp-proxy.socket \
  /etc/systemd/system/shiden-guardian-smtp-proxy.service \
  /etc/systemd/journald.conf.d/60-shiden-guardian.conf
do
  if [ -e "$target" ]; then cp -a --parents "$target" "$backup_dir"; fi
done
printf 'Backed up existing Guardian host files to %s\n' "$backup_dir"

if [ "$(systemctl show shiden-guardian-agent.service --property=LoadState --value 2>/dev/null || true)" = "loaded" ]; then
  systemctl stop shiden-guardian-agent.service || true
fi
if [ "$(systemctl show shiden-guardian-smtp-proxy.socket --property=LoadState --value 2>/dev/null || true)" = "loaded" ]; then
  systemctl stop shiden-guardian-smtp-proxy.socket shiden-guardian-smtp-proxy.service || true
fi

getent group shiden-guardian-observe >/dev/null || groupadd --system shiden-guardian-observe
getent group shiden-guardian-control >/dev/null || groupadd --system shiden-guardian-control
getent group shiden-guardian-secrets >/dev/null || groupadd --system shiden-guardian-secrets
getent group shiden-guardian >/dev/null || groupadd --system shiden-guardian
id shiden-guardian >/dev/null 2>&1 || useradd --system --gid shiden-guardian --home-dir /var/lib/shiden-guardian --shell /usr/sbin/nologin shiden-guardian
usermod -a -G systemd-journal,shiden-guardian-observe,shiden-guardian-control shiden-guardian

install -o root -g root -m 0755 "$binary" /usr/local/bin/shiden-guardian-agent
install -d -o root -g root -m 0755 /usr/local/libexec /etc/shiden-guardian /var/lib/shiden-guardian
chown shiden-guardian:shiden-guardian /var/lib/shiden-guardian
chmod 0750 /var/lib/shiden-guardian
install -o root -g root -m 0755 "$root/deploy/shiden-guardian-restart" /usr/local/libexec/shiden-guardian-restart
install -o root -g root -m 0440 "$root/deploy/sudoers-shiden-guardian" /etc/sudoers.d/shiden-guardian
visudo -cf /etc/sudoers.d/shiden-guardian >/dev/null
install -o root -g root -m 0644 "$root/deploy/shiden-guardian-tmpfiles.conf" /etc/tmpfiles.d/shiden-guardian.conf
systemd-tmpfiles --create /etc/tmpfiles.d/shiden-guardian.conf
install -o root -g root -m 0644 "$root/deploy/shiden-guardian-agent.service" /etc/systemd/system/shiden-guardian-agent.service
install -o root -g root -m 0644 "$root/deploy/shiden-guardian-smtp-proxy.socket" /etc/systemd/system/shiden-guardian-smtp-proxy.socket
install -o root -g root -m 0644 "$root/deploy/shiden-guardian-smtp-proxy.service" /etc/systemd/system/shiden-guardian-smtp-proxy.service

if [ "$apply_journald" -eq 1 ]; then
  install -d -o root -g root -m 0755 /etc/systemd/journald.conf.d
  install -o root -g root -m 0644 "$root/deploy/60-shiden-guardian-journald.conf" /etc/systemd/journald.conf.d/60-shiden-guardian.conf
  systemctl restart systemd-journald.service
  printf 'Applied explicit journald limits: 14 days / 1 GiB.\n'
else
  printf 'Journald limits were not changed. Add --apply-journald-limits after reviewing deploy/60-shiden-guardian-journald.conf.\n'
fi

systemctl daemon-reload
systemctl enable --now shiden-guardian-agent.service
systemctl enable --now shiden-guardian-smtp-proxy.socket

ready=0
attempt=0
while [ "$attempt" -lt 15 ]; do
  if [ -S /run/shiden-guardian/observe/agent.sock ] && [ -S /run/shiden-guardian/control/agent.sock ]; then
    ready=1
    break
  fi
  attempt=$((attempt + 1))
  sleep 1
done
if [ "$ready" -ne 1 ] || ! systemctl is-active --quiet shiden-guardian-agent.service || [ ! -S /run/shiden-guardian/smtp/postfix.sock ]; then
  printf 'Host agent failed its startup verification.\n' >&2
  systemctl --no-pager --full status shiden-guardian-agent.service >&2 || true
  journalctl --no-pager --unit shiden-guardian-agent.service --lines 30 >&2 || true
  printf 'Restoring the previous Guardian agent binary and unit.\n' >&2
  if [ -f "$backup_dir/usr/local/bin/shiden-guardian-agent" ]; then
    cp -a "$backup_dir/usr/local/bin/shiden-guardian-agent" /usr/local/bin/shiden-guardian-agent
  fi
  if [ -f "$backup_dir/etc/systemd/system/shiden-guardian-agent.service" ]; then
    cp -a "$backup_dir/etc/systemd/system/shiden-guardian-agent.service" /etc/systemd/system/shiden-guardian-agent.service
  fi
  systemctl daemon-reload
  systemctl restart shiden-guardian-agent.service || true
  exit 1
fi

printf 'Host agent installed without restarting astar.service.\n'
printf 'OBSERVE_GID=%s\n' "$(getent group shiden-guardian-observe | cut -d: -f3)"
printf 'CONTROL_GID=%s\n' "$(getent group shiden-guardian-control | cut -d: -f3)"
printf 'SECRETS_GID=%s\n' "$(getent group shiden-guardian-secrets | cut -d: -f3)"
systemctl --no-pager --full status shiden-guardian-agent.service | sed -n '1,12p'
