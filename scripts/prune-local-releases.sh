#!/usr/bin/env bash
set -Eeuo pipefail

version="${1:-}"
if [[ ! "$version" =~ ^[0-9]{8}T[0-9]{6}Z$ ]]; then
  printf 'ERROR: Release version must have the form YYYYMMDDTHHMMSSZ.\n' >&2
  exit 64
fi

repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
requested_dir="${SHIDEN_RELEASE_DIR:-$repo/release}"
if [[ ! -d "$requested_dir" ]]; then
  printf 'ERROR: Local release directory does not exist: %s\n' "$requested_dir" >&2
  exit 1
fi
release_dir="$(cd "$requested_dir" && pwd -P)"
if [[ "$release_dir" == "/" || "$(basename "$release_dir")" != "release" ]]; then
  printf 'ERROR: Refusing to prune a directory not named release: %s\n' "$release_dir" >&2
  exit 1
fi

current_archive="$release_dir/shiden-guardian-$version.tar.gz"
current_checksum="$current_archive.sha256"
current_agent_dir="$release_dir/agent-$version"
current_agent="$current_agent_dir/shiden-guardian-agent"
current_agent_checksum="$current_agent.sha256"
for required in \
  "$current_archive" "$current_checksum" "$current_agent_dir" \
  "$current_agent" "$current_agent_checksum"
do
  if [[ ! -e "$required" ]]; then
    printf 'ERROR: Current release is incomplete; local pruning was skipped: %s\n' "$required" >&2
    exit 1
  fi
done

verify_checksum() {
  local payload="$1" checksum_file="$2" expected actual
  expected="$(awk 'NR == 1 { print tolower($1) }' "$checksum_file")"
  if [[ ! "$expected" =~ ^[0-9a-f]{64}$ ]]; then
    printf 'ERROR: Invalid checksum file: %s\n' "$checksum_file" >&2
    return 1
  fi
  actual="$(sha256sum "$payload" | awk '{ print tolower($1) }')"
  if [[ "$actual" != "$expected" ]]; then
    printf 'ERROR: Checksum mismatch; local pruning was skipped: %s\n' "$payload" >&2
    return 1
  fi
}

verify_checksum "$current_archive" "$current_checksum"
verify_checksum "$current_agent" "$current_agent_checksum"

shopt -s nullglob
candidates=(
  "$release_dir"/agent-[0-9]*T[0-9]*Z
  "$release_dir"/shiden-guardian-[0-9]*T[0-9]*Z.tar.gz
  "$release_dir"/shiden-guardian-[0-9]*T[0-9]*Z.tar.gz.sha256
)
for candidate in "${candidates[@]}"; do
  name="$(basename "$candidate")"
  case "$name" in
    "agent-$version"|"shiden-guardian-$version.tar.gz"|"shiden-guardian-$version.tar.gz.sha256")
      continue
      ;;
  esac
  if [[ ! "$name" =~ ^agent-[0-9]{8}T[0-9]{6}Z$ && \
        ! "$name" =~ ^shiden-guardian-[0-9]{8}T[0-9]{6}Z\.tar\.gz(\.sha256)?$ ]]; then
    continue
  fi
  parent="$(cd "$(dirname "$candidate")" && pwd -P)"
  if [[ "$parent" != "$release_dir" ]]; then
    printf 'ERROR: Candidate escaped the release directory: %s\n' "$candidate" >&2
    exit 1
  fi
  if [[ -d "$candidate" && ! -L "$candidate" ]]; then
    rm -rf -- "$candidate"
  else
    rm -f -- "$candidate"
  fi
  printf 'Pruned old local release item: %s\n' "$name"
done

printf 'Local release retention verified: %s\n' "$version"
