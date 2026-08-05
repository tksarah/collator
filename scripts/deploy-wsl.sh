#!/usr/bin/env bash
set -Eeuo pipefail

deploy_mode="full"
acknowledge_pruned_gap=0
while (( $# > 0 )); do
  case "$1" in
    --controller-only) deploy_mode="controller-only" ;;
    --acknowledge-pruned-gap) acknowledge_pruned_gap=1 ;;
    *) printf 'Usage: %s [--controller-only [--acknowledge-pruned-gap]]\n' "$0" >&2; exit 64 ;;
  esac
  shift
done
if (( acknowledge_pruned_gap == 1 )) && [[ "$deploy_mode" != "controller-only" ]]; then
  printf 'ERROR: --acknowledge-pruned-gap requires --controller-only.\n' >&2
  exit 64
fi

ssh_target="${SHIDEN_SSH_TARGET:-shiden-collator}"
remote_root="${SHIDEN_REMOTE_ROOT:-/home/tk/shiden-guardian}"
repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
agent_env_path="${SHIDEN_AGENT_ENV:-$HOME/.ssh/shiden_guardian_agent.env}"

if [[ ! -f "$agent_env_path" ]]; then
  printf 'ERROR: Run scripts/setup-ssh-wsl.sh before deployment.\n' >&2
  exit 1
fi
set +u
# shellcheck disable=SC1090
source "$agent_env_path"
set -u
if ! ssh-add -l >/dev/null 2>&1; then
  printf 'ERROR: The WSL SSH agent is unavailable. Re-run scripts/setup-ssh-wsl.sh.\n' >&2
  exit 1
fi
if ! ssh -o BatchMode=yes -o ConnectTimeout=10 "$ssh_target" true; then
  printf 'ERROR: WSL public-key authentication failed.\n' >&2
  exit 1
fi

version="$(date -u +%Y%m%dT%H%M%SZ)"
release_dir="$repo/release"
archive="$release_dir/shiden-guardian-$version.tar.gz"
checksum_file="$archive.sha256"
mkdir -p "$release_dir"

printf 'Read-only SSH preflight...\n'
set +e
ssh -o BatchMode=yes -o StrictHostKeyChecking=yes "$ssh_target" 'sh -s' <"$repo/scripts/preflight.sh"
preflight_status=$?
set -e
if (( preflight_status > 2 )); then
  printf 'ERROR: SSH access or remote preflight failed.\n' >&2
  exit 1
fi

printf 'Local locked builds and tests in WSL...\n'
cd "$repo"
ENV_FILE=.env.example docker compose --env-file .env.example config --quiet
docker buildx build --platform linux/amd64 --target api --file deploy/Dockerfile.backend .
agent_dir="$release_dir/agent-$version"
mkdir -p "$agent_dir"
docker buildx build --platform linux/amd64 --target agent-artifact \
  --output "type=local,dest=$agent_dir" --file deploy/Dockerfile.backend .
agent_binary="$agent_dir/shiden-guardian-agent"
if [[ ! -f "$agent_binary" ]]; then
  printf 'ERROR: Agent artifact was not produced.\n' >&2
  exit 1
fi
agent_checksum_file="$agent_dir/shiden-guardian-agent.sha256"
agent_checksum="$(sha256sum "$agent_binary" | awk '{ print $1 }')"
printf '%s  shiden-guardian-agent\n' "$agent_checksum" >"$agent_checksum_file"
docker buildx build --platform linux/amd64 --file deploy/Dockerfile.web \
  --build-arg NEXT_PUBLIC_SITE_URL=https://guardian.invalid .
tar \
  --exclude=.git \
  --exclude=node_modules \
  --exclude=.next \
  --exclude=out \
  --exclude=.pnpm-store \
  --exclude=.npm-cache \
  --exclude=.gocache \
  --exclude=.wrangler \
  --exclude=.openai \
  --exclude=build \
  --exclude=db \
  --exclude=drizzle \
  --exclude=examples \
  --exclude=worker \
  --exclude=work \
  --exclude=app/_sites-preview \
  --exclude=next-env.d.ts \
  --exclude=.env \
  --exclude=secrets \
  --exclude=backups \
  --exclude=release \
  -czf "$archive" .
checksum="$(sha256sum "$archive" | awk '{ print $1 }')"
printf '%s  source.tar.gz\n' "$checksum" >"$checksum_file"

ssh "$ssh_target" "mkdir -p '$remote_root/releases/$version'"
scp "$archive" "$checksum_file" "$agent_binary" "$agent_checksum_file" \
  "$ssh_target:$remote_root/releases/$version/"
remote_stage="cd '$remote_root/releases/$version' && mv 'shiden-guardian-$version.tar.gz' source.tar.gz && mv 'shiden-guardian-$version.tar.gz.sha256' source.tar.gz.sha256 && sha256sum -c source.tar.gz.sha256 && tar -xzf source.tar.gz && mkdir -p release/bin && mv shiden-guardian-agent shiden-guardian-agent.sha256 release/bin/ && cd release/bin && sha256sum -c shiden-guardian-agent.sha256 && chmod 0755 shiden-guardian-agent && cd ../.. && sh scripts/preflight.sh"
set +e
ssh "$ssh_target" "$remote_stage"
remote_status=$?
set -e
if (( remote_status > 2 )); then
  printf 'ERROR: Remote checksum, extraction, or preflight failed.\n' >&2
  exit 1
fi

# Only a completely verified remote stage authorizes local release pruning.
# The helper accepts a strict release timestamp and never touches remote files.
if ! SHIDEN_RELEASE_DIR="$release_dir" \
  bash "$repo/scripts/prune-local-releases.sh" "$version"; then
  printf 'WARNING: Remote staging succeeded, but local old-release cleanup was refused. No remote release was removed.\n' >&2
fi

printf '\nRelease staged and agent artifact built: %s/releases/%s\n' "$remote_root" "$version"
printf 'Keep your password-authenticated SSH session open. Review and run there:\n'
printf "  cd '%s/releases/%s'\n" "$remote_root" "$version"
if [[ "$deploy_mode" == "controller-only" ]]; then
  printf "  sudo sh scripts/copy-runtime-config.sh '%s/current'\n" "$remote_root"
  if (( acknowledge_pruned_gap == 1 )); then
    printf "  sudo sh scripts/activate-controller-release.sh '%s' '%s' --acknowledge-pruned-gap\n" "$remote_root" "$version"
  else
    printf "  sudo sh scripts/activate-controller-release.sh '%s' '%s'\n" "$remote_root" "$version"
  fi
  printf '\nController-only mode preserves runtime credentials and does not run DB migration or host bootstrap.\n'
  exit 0
fi
if ssh -o BatchMode=yes "$ssh_target" "test -L '$remote_root/current'"; then
  printf "  sudo sh scripts/migrate-release-config.sh '%s/current'\n" "$remote_root"
else
  printf '  sh scripts/configure-release.sh\n'
fi
printf '  less scripts/bootstrap-host.sh deploy/shiden-guardian-agent.service deploy/sudoers-shiden-guardian\n'
printf '  sudo sh scripts/bootstrap-host.sh\n'
printf '\nBecause the operator does not have Docker socket access, keep Compose root-managed.\n'
printf 'Activate from the password-authenticated session after configuration:\n'
printf "  sudo sh '%s/releases/%s/scripts/activate-release.sh' '%s' '%s'\n" \
  "$remote_root" "$version" "$remote_root" "$version"
