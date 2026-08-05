#!/bin/sh
set -eu
umask 077

if [ "$(id -u)" -ne 0 ]; then echo "Run runtime configuration copy with sudo." >&2; exit 1; fi
source_release="$(readlink -f -- "${1:?source release directory required}")"
target_release="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd -P)"
remote_root="$(dirname -- "$(dirname -- "$target_release")")"
case "$target_release" in "$remote_root"/releases/*) ;; *) echo "Target release escaped the release root." >&2; exit 1;; esac
case "$source_release" in "$remote_root"/releases/*) ;; *) echo "Source release escaped the release root." >&2; exit 1;; esac
test -d "$source_release" || { echo "Source release does not exist." >&2; exit 1; }
test "$source_release" != "$target_release" || { echo "Source and target release are identical." >&2; exit 1; }
test -f "$source_release/.env" && test ! -L "$source_release/.env" || { echo "Source release does not contain a regular .env file." >&2; exit 1; }
test ! -e "$target_release/.env" || { echo "Target release is already configured." >&2; exit 1; }
test ! -e "$source_release/secrets/database_url" || { echo "Legacy database_url is not allowed for app-only releases." >&2; exit 1; }
test ! -e "$source_release/secrets/migration_database_url" || { echo "Migration credential is not allowed for app-only releases." >&2; exit 1; }

runtime_secrets="api_database_url auth_database_url controller_database_url backup_password postgres_password action_broker_key gemini_api_key smtp_password bootstrap_token encryption_key"
for file in $runtime_secrets; do
	test -f "$source_release/secrets/$file" && test ! -L "$source_release/secrets/$file" || { echo "Source runtime secret is not a regular file: $file" >&2; exit 1; }
	test -r "$source_release/secrets/$file" || { echo "Source runtime secrets are protected; rerun with sudo." >&2; exit 1; }
done

secrets_gid="$(sed -n 's/^SECRETS_GID=//p' "$source_release/.env" | tail -n 1)"
case "$secrets_gid" in *[!0-9]*|'') echo "Invalid SECRETS_GID" >&2; exit 1;; esac
getent group "$secrets_gid" | grep -q '^shiden-guardian-secrets:' || { echo "SECRETS_GID does not match shiden-guardian-secrets" >&2; exit 1; }

install -o root -g root -m 0600 "$source_release/.env" "$target_release/.env"
install -d -o root -g "$secrets_gid" -m 0750 "$target_release/secrets"
for file in $runtime_secrets; do
  install -o root -g "$secrets_gid" -m 0640 "$source_release/secrets/$file" "$target_release/secrets/$file"
  cmp -s "$source_release/secrets/$file" "$target_release/secrets/$file" || { echo "Runtime secret copy verification failed: $file" >&2; exit 1; }
done
cmp -s "$source_release/.env" "$target_release/.env" || { echo "Runtime environment copy verification failed." >&2; exit 1; }
test ! -e "$target_release/secrets/database_url"
test ! -e "$target_release/secrets/migration_database_url"
printf 'Runtime configuration copied byte-for-byte without displaying secret values.\n'
