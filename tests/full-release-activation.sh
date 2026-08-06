#!/usr/bin/env bash
set -Eeuo pipefail

repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
activate="$repo/scripts/activate-release.sh"

grep -Fq 'docker compose up -d --no-deps --force-recreate --wait --wait-timeout 180' "$activate"
grep -Fq 'api controller auth-broker backup prometheus caddy' "$activate"

runtime_start="$(sed -n '/^if ! docker compose up -d --no-deps --force-recreate --wait --wait-timeout 180/,/; then$/p' "$activate")"
printf '%s\n' "$runtime_start" | grep -Fq 'api controller auth-broker backup prometheus caddy'
if printf '%s\n' "$runtime_start" | grep -Eq '(^|[[:space:]])postgres([[:space:]]|;|$)'; then
  printf 'ERROR: Full activation must not force-recreate PostgreSQL.\n' >&2
  exit 1
fi

printf 'Full release activation recreation policy passed.\n'
