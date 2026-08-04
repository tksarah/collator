#!/usr/bin/env bash
set -Eeuo pipefail

host_name="${SHIDEN_HOST:-192.168.2.194}"
ssh_user="${SHIDEN_USER:-tk}"
ssh_alias="${SHIDEN_ALIAS:-shiden-collator}"
allowed_from="${SHIDEN_KEY_FROM:-192.168.2.0/24}"
expected_fingerprint="${SHIDEN_HOST_FINGERPRINT:-SHA256:fkbdkdvFESNUXqSzus7FFxWSt/jEs+Itx6YkC9ZMN4U}"

ssh_dir="$HOME/.ssh"
key_path="$ssh_dir/shiden_collator_deploy_ed25519"
config_path="$ssh_dir/config"
known_hosts_path="$ssh_dir/known_hosts"
agent_env_path="$ssh_dir/shiden_guardian_agent.env"

umask 077
mkdir -p "$ssh_dir"
chmod 700 "$ssh_dir"

tmp_dir="$(mktemp -d)"
cleanup() {
  rm -rf -- "$tmp_dir"
}
trap cleanup EXIT

scan_file="$tmp_dir/host-key"
scan_error="$tmp_dir/host-key.error"
printf 'Reading the SSH Ed25519 host key and verifying its fingerprint...\n'
if ! ssh-keyscan -T 5 -t ed25519 "$host_name" >"$scan_file" 2>"$scan_error" ||
   ! grep -Eq '^\S+[[:space:]]+ssh-ed25519[[:space:]]+' "$scan_file"; then
  printf 'ssh-keyscan could not negotiate KEX; using an isolated SSH handshake fallback.\n'
  : >"$scan_file"
  fallback_known_hosts="$tmp_dir/fallback-known-hosts"
  : >"$fallback_known_hosts"
  ssh \
    -o KexAlgorithms=curve25519-sha256 \
    -o HostKeyAlgorithms=ssh-ed25519 \
    -o BatchMode=yes \
    -o PasswordAuthentication=no \
    -o PubkeyAuthentication=no \
    -o ConnectTimeout=5 \
    -o StrictHostKeyChecking=accept-new \
    -o HashKnownHosts=no \
    -o UserKnownHostsFile="$fallback_known_hosts" \
    "$ssh_user@$host_name" exit >"$tmp_dir/fallback.out" 2>"$tmp_dir/fallback.error" || true
  grep -E '^\S+[[:space:]]+ssh-ed25519[[:space:]]+' "$fallback_known_hosts" >"$scan_file" || true
fi

if [[ ! -s "$scan_file" ]]; then
  printf 'ERROR: The host key could not be read; known_hosts was not modified.\n' >&2
  sed -n '1,5p' "$scan_error" >&2 || true
  exit 1
fi

actual_fingerprint="$(ssh-keygen -E sha256 -lf "$scan_file" | awk 'NR == 1 { print $2 }')"
if [[ "$actual_fingerprint" != "$expected_fingerprint" ]]; then
  printf 'ERROR: Host fingerprint mismatch.\nExpected: %s\nActual:   %s\n' \
    "$expected_fingerprint" "$actual_fingerprint" >&2
  exit 1
fi
printf 'Verified host fingerprint: %s\n' "$actual_fingerprint"

existing_keys="$tmp_dir/existing-host-keys"
if [[ -f "$known_hosts_path" ]]; then
  ssh-keygen -F "$host_name" -f "$known_hosts_path" 2>/dev/null |
    grep -v '^#' >"$existing_keys" || true
fi
if [[ -s "$existing_keys" ]]; then
  if ! ssh-keygen -E sha256 -lf "$existing_keys" | grep -Fq "$expected_fingerprint"; then
    printf 'ERROR: known_hosts already contains a different key for %s. No automatic replacement was made.\n' "$host_name" >&2
    exit 1
  fi
else
  cat "$scan_file" >>"$known_hosts_path"
fi
chmod 600 "$known_hosts_path"

if [[ ! -f "$key_path" ]]; then
  printf 'Create a dedicated WSL key. Enter a strong passphrase when prompted.\n'
  ssh-keygen -t ed25519 -a 100 -f "$key_path" -C shiden-guardian-deploy-wsl
fi

touch "$config_path"
chmod 600 "$config_path"
if ! grep -Eq "^[[:space:]]*Host[[:space:]]+$ssh_alias[[:space:]]*$" "$config_path"; then
  cat >>"$config_path" <<EOF

# BEGIN shiden-guardian
Host $ssh_alias
    HostName $host_name
    User $ssh_user
    IdentityFile $key_path
    IdentitiesOnly yes
    StrictHostKeyChecking yes
    UserKnownHostsFile $known_hosts_path
# END shiden-guardian
EOF
fi

agent_status=2
if [[ -f "$agent_env_path" ]]; then
  set +u
  # shellcheck disable=SC1090
  source "$agent_env_path"
  set -u
  ssh-add -l >/dev/null 2>&1 && agent_status=0 || agent_status=$?
fi
if [[ "$agent_status" -eq 2 ]]; then
  eval "$(ssh-agent -s)" >/dev/null
  {
    printf 'export SSH_AUTH_SOCK=%q\n' "$SSH_AUTH_SOCK"
    printf 'export SSH_AGENT_PID=%q\n' "$SSH_AGENT_PID"
  } >"$agent_env_path"
  chmod 600 "$agent_env_path"
fi

key_fingerprint="$(ssh-keygen -E sha256 -lf "$key_path.pub" | awk 'NR == 1 { print $2 }')"
if ! ssh-add -l 2>/dev/null | grep -Fq "$key_fingerprint"; then
  ssh-add "$key_path"
fi

if ssh -o BatchMode=yes -o ConnectTimeout=10 "$ssh_alias" true 2>/dev/null; then
  printf 'WSL public-key authentication is ready.\n'
  ssh -o BatchMode=yes "$ssh_alias" hostname
  exit 0
fi

public_key_line="from=\"$allowed_from\",restrict $(<"$key_path.pub")"
printf '\nThe WSL key must now be registered on the node.\n'
printf 'Client key fingerprint: %s\n' "$key_fingerprint"
printf 'Only the public key and your SSH password will be used during this interactive step.\n'
read -r -p 'Register it now using password authentication? [y/N] ' reply
if [[ ! "$reply" =~ ^[Yy]$ ]]; then
  printf 'Registration was not performed. Re-run this script when ready.\n'
  exit 2
fi

printf '%s\n' "$public_key_line" |
  ssh \
    -o PubkeyAuthentication=no \
    -o PreferredAuthentications=keyboard-interactive,password \
    -o ConnectTimeout=10 \
    "$ssh_user@$host_name" \
    'set -eu; umask 077; mkdir -p "$HOME/.ssh"; touch "$HOME/.ssh/authorized_keys"; chmod 700 "$HOME/.ssh"; chmod 600 "$HOME/.ssh/authorized_keys"; IFS= read -r key_line; grep -qxF -- "$key_line" "$HOME/.ssh/authorized_keys" || printf "%s\n" "$key_line" >>"$HOME/.ssh/authorized_keys"'

printf 'Testing WSL public-key authentication...\n'
ssh -o BatchMode=yes -o ConnectTimeout=10 "$ssh_alias" hostname
printf 'WSL SSH setup completed. Agent environment: %s\n' "$agent_env_path"

