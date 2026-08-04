#!/usr/bin/env bash
set -Eeuo pipefail

repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
fixture="$(mktemp -d)"
cleanup() { rm -rf -- "$fixture"; }
trap cleanup EXIT

release_dir="$fixture/release"
current="20260804T173903Z"
old="20260804T164339Z"
mkdir -p "$release_dir/agent-$current" "$release_dir/agent-$old"
printf 'current archive\n' >"$release_dir/shiden-guardian-$current.tar.gz"
printf 'current agent\n' >"$release_dir/agent-$current/shiden-guardian-agent"
printf 'old archive\n' >"$release_dir/shiden-guardian-$old.tar.gz"
printf 'old checksum\n' >"$release_dir/shiden-guardian-$old.tar.gz.sha256"
printf 'old agent\n' >"$release_dir/agent-$old/shiden-guardian-agent"
printf 'unrelated\n' >"$release_dir/operator-notes.txt"

archive_hash="$(sha256sum "$release_dir/shiden-guardian-$current.tar.gz" | awk '{ print $1 }')"
agent_hash="$(sha256sum "$release_dir/agent-$current/shiden-guardian-agent" | awk '{ print $1 }')"
printf '%s  source.tar.gz\n' "$archive_hash" >"$release_dir/shiden-guardian-$current.tar.gz.sha256"
printf '%s  shiden-guardian-agent\n' "$agent_hash" >"$release_dir/agent-$current/shiden-guardian-agent.sha256"

SHIDEN_RELEASE_DIR="$release_dir" bash "$repo/scripts/prune-local-releases.sh" "$current"

test -f "$release_dir/shiden-guardian-$current.tar.gz"
test -f "$release_dir/shiden-guardian-$current.tar.gz.sha256"
test -f "$release_dir/agent-$current/shiden-guardian-agent"
test -f "$release_dir/operator-notes.txt"
test ! -e "$release_dir/shiden-guardian-$old.tar.gz"
test ! -e "$release_dir/shiden-guardian-$old.tar.gz.sha256"
test ! -e "$release_dir/agent-$old"
printf 'Local release retention fixture passed.\n'
