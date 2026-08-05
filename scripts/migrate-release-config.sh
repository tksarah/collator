#!/bin/sh
set -eu

source_release="${1:?source release directory required}"
target_release="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
test -f "$source_release/.env" || { echo "Source release does not contain .env: $source_release" >&2; exit 1; }
test -r "$source_release/.env" || { echo "Source configuration is protected; rerun this script with sudo." >&2; exit 1; }
for file in postgres_password gemini_api_key smtp_password bootstrap_token encryption_key; do
  test -e "$source_release/secrets/$file" || { echo "Source secret is missing: $file" >&2; exit 1; }
  test -r "$source_release/secrets/$file" || { echo "Source secrets are protected; rerun this script with sudo." >&2; exit 1; }
done
test ! -e "$target_release/.env" || { echo "Target release is already configured." >&2; exit 1; }

umask 077
mkdir -p "$target_release/secrets"
install -m 0600 "$source_release/.env" "$target_release/.env"
for file in gemini_api_key smtp_password bootstrap_token encryption_key; do
  install -m 0600 "$source_release/secrets/$file" "$target_release/secrets/$file"
done

old_postgres_password="$(cat "$source_release/secrets/postgres_password")"
postgres_password="$(openssl rand -hex 24)"
api_password="$(openssl rand -hex 24)"
auth_password="$(openssl rand -hex 24)"
controller_password="$(openssl rand -hex 24)"
backup_password="$(openssl rand -hex 24)"
action_broker_key="$(openssl rand -base64 32 | tr -d '\n')"
printf 'postgres://guardian:%s@postgres:5432/guardian?sslmode=disable' "$old_postgres_password" > "$target_release/secrets/migration_database_url"
printf '%s' "$postgres_password" > "$target_release/secrets/postgres_password"
printf 'postgres://guardian_api:%s@postgres:5432/guardian?sslmode=disable' "$api_password" > "$target_release/secrets/api_database_url"
printf 'postgres://guardian_auth:%s@postgres:5432/guardian?sslmode=disable' "$auth_password" > "$target_release/secrets/auth_database_url"
printf 'postgres://guardian_controller:%s@postgres:5432/guardian?sslmode=disable' "$controller_password" > "$target_release/secrets/controller_database_url"
printf '%s' "$backup_password" > "$target_release/secrets/backup_password"
printf '%s' "$action_broker_key" > "$target_release/secrets/action_broker_key"

set_value() {
  key="$1"
  value="$2"
  if grep -q "^${key}=" "$target_release/.env"; then
    sed -i "s|^${key}=.*|${key}=${value}|" "$target_release/.env"
  else
    printf '%s=%s\n' "$key" "$value" >>"$target_release/.env"
  fi
}
set_value OBSERVE_GID "$(getent group shiden-guardian-observe | cut -d: -f3)"
set_value CONTROL_GID "$(getent group shiden-guardian-control | cut -d: -f3)"
set_value SECRETS_GID "$(getent group shiden-guardian-secrets | cut -d: -f3)"
set_value COLLATOR_REWARD_ADDRESS "WGYDjFY3JSijqBMkKEv7qfWU6XaRnmzigQG7B6G1zh7jBzN"
set_value REWARD_WARNING_MINUTES "15"
set_value REWARD_CRITICAL_MINUTES "30"
chmod 0600 "$target_release/.env" "$target_release"/secrets/*
printf 'Configuration migrated without displaying secret values.\n'
