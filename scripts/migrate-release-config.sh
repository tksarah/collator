#!/bin/sh
set -eu

source_release="${1:?source release directory required}"
target_release="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
test -f "$source_release/.env" || { echo "Source release does not contain .env: $source_release" >&2; exit 1; }
test -r "$source_release/.env" || { echo "Source configuration is protected; rerun this script with sudo." >&2; exit 1; }
for file in database_url postgres_password gemini_api_key smtp_password bootstrap_token encryption_key; do
  test -e "$source_release/secrets/$file" || { echo "Source secret is missing: $file" >&2; exit 1; }
  test -r "$source_release/secrets/$file" || { echo "Source secrets are protected; rerun this script with sudo." >&2; exit 1; }
done
test ! -e "$target_release/.env" || { echo "Target release is already configured." >&2; exit 1; }

umask 077
mkdir -p "$target_release/secrets"
install -m 0600 "$source_release/.env" "$target_release/.env"
for file in database_url postgres_password gemini_api_key smtp_password bootstrap_token encryption_key; do
  install -m 0600 "$source_release/secrets/$file" "$target_release/secrets/$file"
done

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
