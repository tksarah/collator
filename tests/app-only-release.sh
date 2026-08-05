#!/bin/sh
set -eu
repo="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd -P)"
copy="$repo/scripts/copy-runtime-config.sh"
activate="$repo/scripts/activate-controller-release.sh"
sh -n "$copy" "$activate"
grep -q 'migration_database_url.*not allowed' "$copy"
grep -q 'docker compose up -d --no-deps --force-recreate --wait' "$activate"
grep -q 'Runtime secret changed in app-only release' "$activate"
grep -q 'Reward monitoring created a remediation action' "$activate"
grep -q 'docker compose stop controller' "$activate"
grep -Fq 'docker compose run --rm --no-deps controller -acknowledge-pruned-reward-gap "$cursor_before"' "$activate"
if grep -Fq 'docker compose run --rm --no-deps controller /usr/local/bin/controller' "$activate"; then
  echo 'Controller one-shot activation duplicates the image ENTRYPOINT.' >&2
  exit 1
fi
grep -q 'reward.history_gap.acknowledge' "$activate"
grep -Fq "SELECT (details->>'from_block')||'|'||(details->>'through_block')" "$activate"
if grep -Eq 'dbmigrate|rotate-admin|docker compose down|docker compose stop (api|auth-broker|caddy|prometheus|postgres|backup)|bootstrap-host' "$activate"; then
  echo 'App-only activation contains a forbidden migration, non-controller stop, or host bootstrap command.' >&2
  exit 1
fi
printf 'App-only controller release safety fixture passed.\n'
