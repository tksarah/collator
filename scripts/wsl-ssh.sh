#!/usr/bin/env bash
set -Eeuo pipefail

agent_env_path="${SHIDEN_AGENT_ENV:-$HOME/.ssh/shiden_guardian_agent.env}"
if [[ ! -f "$agent_env_path" ]]; then
  printf 'ERROR: WSL SSH agent environment is missing. Run scripts/setup-ssh-wsl.sh first.\n' >&2
  exit 1
fi

set +u
# shellcheck disable=SC1090
source "$agent_env_path"
set -u
if ! ssh-add -l >/dev/null 2>&1; then
  printf 'ERROR: The saved WSL SSH agent is unavailable. Re-run scripts/setup-ssh-wsl.sh.\n' >&2
  exit 1
fi

exec ssh "$@"

