#!/bin/sh
set -eu
if [ "$(id -u)" -ne 0 ]; then echo "Run activation with sudo." >&2; exit 1; fi
remote_root="${1:?remote root required}"
version="${2:?version required}"
release="$remote_root/releases/$version"
cd "$release"

for file in .env secrets/database_url secrets/postgres_password secrets/gemini_api_key secrets/bootstrap_token secrets/encryption_key; do
  test -s "$file" || { echo "Missing required configuration: $file" >&2; exit 1; }
done
test -e secrets/smtp_password || { echo "Missing required configuration: secrets/smtp_password" >&2; exit 1; }
test -S /run/shiden-guardian/observe/agent.sock
test -S /run/shiden-guardian/control/agent.sock
smtp_socket="$(sed -n 's/^SMTP_UNIX_SOCKET=//p' .env | tail -n 1)"
if [ -n "$smtp_socket" ]; then test -S "$smtp_socket"; fi
secrets_gid="$(sed -n 's/^SECRETS_GID=//p' .env | tail -n 1)"
case "$secrets_gid" in *[!0-9]*|'') echo "Invalid SECRETS_GID" >&2; exit 1;; esac
getent group "$secrets_gid" | grep -q '^shiden-guardian-secrets:' || { echo "SECRETS_GID does not match shiden-guardian-secrets" >&2; exit 1; }
chown root:"$secrets_gid" secrets/*
chmod 0640 secrets/*
mkdir -p "$remote_root/backups"
docker compose config >/dev/null
docker compose build

previous=""
if [ -L "$remote_root/current" ]; then previous="$(readlink -f "$remote_root/current")"; fi
rollback() {
  printf 'Activation failed; restoring the previous release.\n' >&2
  if [ -n "$previous" ] && [ -d "$previous" ]; then
    cd "$previous"
    docker compose up -d --remove-orphans || true
    ln -sfn "$previous" "$remote_root/current"
  else
    cd "$release"
    docker compose down || true
  fi
}
activated=0
cleanup() {
  status=$?
  trap - EXIT INT TERM HUP
  if [ "$activated" -ne 1 ]; then rollback; fi
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM HUP

docker compose up -d postgres
until docker compose exec -T postgres pg_isready -U guardian -d guardian >/dev/null 2>&1; do sleep 2; done
stamp="$(date -u +%Y%m%dT%H%M%SZ)"
docker compose exec -T postgres pg_dump -U guardian -d guardian -Fc > "$remote_root/backups/pre-deploy-$stamp.dump"
docker compose run --rm --no-deps api -migrate-dry-run

if ! docker compose up -d --remove-orphans --wait --wait-timeout 180; then
  docker compose logs --tail=120 api controller prometheus caddy
  exit 1
fi
if ! docker compose exec -T api /usr/local/bin/api -healthcheck; then
  docker compose logs --tail=120 api controller
  exit 1
fi
domain="$(sed -n 's/^DOMAIN=//p' .env | tail -n 1)"
if [ -z "$domain" ]; then
  echo "HTTPS health check failed: DOMAIN is empty" >&2
  exit 1
fi

# Many home routers do not support NAT loopback. Keep the production hostname,
# SNI, and certificate verification, but connect directly to the local Caddy
# listener. Retry while Caddy obtains its public certificate.
https_ready=0
attempt=1
while [ "$attempt" -le 36 ]; do
  if curl --resolve "$domain:443:127.0.0.1" -fsS --max-time 5 \
    "https://$domain/healthz" >/dev/null; then
    https_ready=1
    break
  fi
  attempt=$((attempt + 1))
  sleep 5
done
if [ "$https_ready" -ne 1 ]; then
  echo "HTTPS health check failed for $domain" >&2
  docker compose logs --tail=120 caddy api
  exit 1
fi

ln -sfn "$release" "$remote_root/current"
activated=1
trap - EXIT INT TERM HUP
docker compose ps
printf 'Activated and verified release %s\n' "$version"
