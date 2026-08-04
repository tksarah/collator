#!/bin/sh
set -eu
root="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
guardian_root="$(dirname "$(dirname "$root")")"
cd "$root"
umask 077
mkdir -p secrets backups
[ -f .env ] || cp .env.example .env
restore_tty() { stty echo 2>/dev/null || true; }
trap restore_tty EXIT HUP INT TERM

printf 'Dashboard domain: '; read -r domain
printf 'ACME email: '; read -r acme_email
printf 'SMTP transport [local-postfix/external] [local-postfix]: '; read -r smtp_transport
smtp_transport="${smtp_transport:-local-postfix}"
case "$smtp_transport" in
  local-postfix)
    smtp_host=localhost
    smtp_port=25
    smtp_user=
    smtp_password=
    smtp_unix_socket=/run/shiden-guardian/smtp/postfix.sock
    smtp_tls_mode=none
    ;;
  external)
    printf 'SMTP host: '; read -r smtp_host
    printf 'SMTP port [587]: '; read -r smtp_port; smtp_port="${smtp_port:-587}"
    printf 'SMTP user: '; read -r smtp_user
    printf 'SMTP password: '; stty -echo; read -r smtp_password; stty echo; printf '\n'
    smtp_unix_socket=
    smtp_tls_mode=required
    ;;
  *)
    printf 'Unknown SMTP transport: %s\n' "$smtp_transport" >&2
    exit 64
    ;;
esac
default_from="shiden-guardian@$(hostname -f 2>/dev/null || hostname)"
printf 'Notification sender [%s]: ' "$default_from"; read -r smtp_from; smtp_from="${smtp_from:-$default_from}"
printf 'Notification recipient: '; read -r smtp_to
printf 'Gemini API key: '; stty -echo; read -r gemini_key; stty echo; printf '\n'

postgres_password="$(openssl rand -hex 24)"
bootstrap_token="$(openssl rand -hex 24)"
encryption_key="$(openssl rand -base64 32 | tr -d '\n')"
printf '%s' "$postgres_password" > secrets/postgres_password
printf 'postgres://guardian:%s@postgres:5432/guardian?sslmode=disable' "$postgres_password" > secrets/database_url
printf '%s' "$gemini_key" > secrets/gemini_api_key
printf '%s' "$smtp_password" > secrets/smtp_password
printf '%s' "$bootstrap_token" > secrets/bootstrap_token
printf '%s' "$encryption_key" > secrets/encryption_key

observe_gid="$(getent group shiden-guardian-observe | cut -d: -f3)"
control_gid="$(getent group shiden-guardian-control | cut -d: -f3)"
secrets_gid="$(getent group shiden-guardian-secrets | cut -d: -f3)"
sed -i \
  -e "s|^DOMAIN=.*|DOMAIN=$domain|" \
  -e "s|^ACME_EMAIL=.*|ACME_EMAIL=$acme_email|" \
  -e "s|^INSTALLED_AT=.*|INSTALLED_AT=$(date -u +%Y-%m-%dT%H:%M:%SZ)|" \
  -e "s|^OBSERVE_GID=.*|OBSERVE_GID=$observe_gid|" \
  -e "s|^CONTROL_GID=.*|CONTROL_GID=$control_gid|" \
  -e "s|^SECRETS_GID=.*|SECRETS_GID=$secrets_gid|" \
  -e "s|^HOST_UID=.*|HOST_UID=$(id -u)|" \
  -e "s|^HOST_GID=.*|HOST_GID=$(id -g)|" \
  -e "s|^SMTP_HOST=.*|SMTP_HOST=$smtp_host|" \
  -e "s|^SMTP_PORT=.*|SMTP_PORT=$smtp_port|" \
  -e "s|^SMTP_USER=.*|SMTP_USER=$smtp_user|" \
  -e "s|^SMTP_FROM=.*|SMTP_FROM=$smtp_from|" \
  -e "s|^SMTP_TO=.*|SMTP_TO=$smtp_to|" \
  -e "s|^SMTP_UNIX_SOCKET=.*|SMTP_UNIX_SOCKET=$smtp_unix_socket|" \
  -e "s|^SMTP_TLS_MODE=.*|SMTP_TLS_MODE=$smtp_tls_mode|" .env
if grep -q '^BACKUP_DIR=' .env; then
  sed -i -e "s|^BACKUP_DIR=.*|BACKUP_DIR=$guardian_root/backups|" .env
else
  printf '\nBACKUP_DIR=%s/backups\n' "$guardian_root" >> .env
fi
mkdir -p "$guardian_root/backups"
chmod 0600 .env secrets/*
printf '\nConfiguration complete. Save this bootstrap token now:\n%s\n' "$bootstrap_token"
