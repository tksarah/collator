#!/bin/sh
set -eu
umask 077
if [ "$(id -u)" -ne 0 ]; then echo "Run activation with sudo." >&2; exit 1; fi
remote_root="${1:?remote root required}"
version="${2:?version required}"
release="$remote_root/releases/$version"
cd "$release"
previous=""
if [ -L "$remote_root/current" ]; then previous="$(readlink -f "$remote_root/current")"; fi

for file in .env secrets/migration_database_url secrets/api_database_url secrets/auth_database_url secrets/controller_database_url secrets/backup_password secrets/postgres_password secrets/action_broker_key secrets/gemini_api_key secrets/bootstrap_token secrets/encryption_key; do
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
chmod 0700 "$remote_root/backups"
available_kb="$(df -Pk "$remote_root" | awk 'NR==2 {print $4}')"
case "$available_kb" in *[!0-9]*|'') echo "Could not determine free disk space" >&2; exit 1;; esac
if [ "$available_kb" -lt 2097152 ]; then echo "At least 2 GiB of free disk space is required" >&2; exit 1; fi
printf 'Activation timestamp: %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
if command -v timedatectl >/dev/null 2>&1; then
  printf 'NTP synchronized: %s\n' "$(timedatectl show --property=NTPSynchronized --value 2>/dev/null || printf unknown)"
fi
docker info >/dev/null
docker compose config >/dev/null
docker compose --profile tools build
# Prove the one-shot migration container can read every protected secret before
# any service is stopped. This specifically covers the host's root:GID 0640
# production permissions, which developer workstations may not reproduce.
docker compose --profile tools run --rm --no-deps --entrypoint /bin/sh dbmigrate -ec '
  for file in \
    /run/secrets/migration_database_url \
    /run/secrets/api_database_url \
    /run/secrets/auth_database_url \
    /run/secrets/controller_database_url \
    /run/secrets/backup_password \
    /run/secrets/postgres_password
  do
    test -r "$file"
  done
'
docker compose ps

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
credentials_rotated=0
cleanup() {
  status=$?
  trap - EXIT INT TERM HUP
  if [ "$activated" -ne 1 ]; then
    if [ "$credentials_rotated" -eq 0 ]; then
      rollback
    else
      printf 'Activation failed after database credential rotation; automatic rollback is disabled. Fix the new release forward.\n' >&2
    fi
  fi
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM HUP

if [ -z "$previous" ]; then
  docker compose up -d postgres
fi
until docker compose exec -T postgres pg_isready -U guardian -d guardian >/dev/null 2>&1; do sleep 2; done
active_users_before="$(docker compose exec -T postgres psql -U guardian -d guardian -Atc "SELECT count(*) FROM users WHERE active=true" 2>/dev/null || printf 0)"
case "$active_users_before" in *[!0-9]*|'') echo "Could not read active user count" >&2; exit 1;; esac
if [ -n "$previous" ]; then
  pending_actions="$(docker compose exec -T postgres psql -U guardian -d guardian -Atc "SELECT count(*) FROM remediation_actions WHERE status IN ('pending','running')")"
  case "$pending_actions" in *[!0-9]*|'') echo "Could not read pending action count" >&2; exit 1;; esac
  if [ "$pending_actions" -ne 0 ]; then echo "Refusing activation while actions are pending or running" >&2; exit 1; fi
fi
stamp="$(date -u +%Y%m%dT%H%M%SZ)"
database_dump="$remote_root/backups/pre-deploy-$stamp.dump"
globals_dump="$remote_root/backups/pre-deploy-$stamp-globals.sql"
docker compose exec -T postgres pg_dump -U guardian -d guardian -Fc > "$database_dump"
docker compose exec -T postgres pg_dumpall -U guardian --globals-only > "$globals_dump"
test -s "$database_dump"
test -s "$globals_dump"
chmod 0600 "$database_dump" "$globals_dump"
docker compose exec -T postgres pg_restore --list < "$database_dump" >/dev/null

docker compose stop caddy api auth-broker controller prometheus backup >/dev/null 2>&1 || true
docker compose up -d postgres
until docker compose exec -T postgres pg_isready -U guardian -d guardian >/dev/null 2>&1; do sleep 2; done
docker compose --profile tools run --rm dbmigrate -mode=dry-run
docker compose --profile tools run --rm dbmigrate -mode=apply
docker compose --profile tools run --rm dbmigrate -mode=verify
docker compose --profile tools run --rm dbmigrate -mode=rotate-admin
credentials_rotated=1

# Runtime database passwords now match only this release. Recreate every
# non-database service so unchanged images cannot retain bind-mounted secrets
# from the previous release directory. PostgreSQL keeps its existing container
# and persistent volume; its password file is only used during initialization.
if ! docker compose up -d --no-deps --force-recreate --wait --wait-timeout 180 \
  api controller auth-broker backup prometheus caddy; then
  docker compose logs --tail=120 api controller prometheus caddy
  exit 1
fi
if ! docker compose exec -T api /usr/local/bin/api -healthcheck || ! docker compose exec -T auth-broker /usr/local/bin/auth-broker -healthcheck; then
  docker compose logs --tail=120 api auth-broker controller
  exit 1
fi
if ! docker compose exec -T api /bin/sh -c 'test -e /run/secrets/api_database_url && test ! -e /run/secrets/auth_database_url && test ! -e /run/secrets/controller_database_url && test ! -e /run/secrets/postgres_password && test ! -e /run/secrets/encryption_key && test ! -e /run/secrets/action_broker_key && test ! -e /run/shiden-guardian/action-broker/controller.sock'; then
  echo "API privilege separation check failed" >&2
  exit 1
fi
runtime_users="$(docker compose exec -T postgres psql -U guardian -d guardian -Atc "SELECT string_agg(DISTINCT usename, ',') FROM pg_stat_activity WHERE datname='guardian'")"
for runtime_user in guardian_api guardian_auth guardian_controller; do
  printf '%s' "$runtime_users" | grep -Eq "(^|,)${runtime_user}(,|$)" || { echo "Runtime database role is not connected: $runtime_user" >&2; exit 1; }
done
active_users_after="$(docker compose exec -T postgres psql -U guardian -d guardian -Atc "SELECT count(*) FROM users WHERE active=true")"
if [ "$active_users_after" != "$active_users_before" ]; then echo "Active user state changed during migration" >&2; exit 1; fi
test ! -e secrets/database_url

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

unauthorized_status="$(curl --resolve "$domain:443:127.0.0.1" -sS --max-time 5 -o /dev/null -w '%{http_code}' "https://$domain/api/v1/overview")"
if [ "$unauthorized_status" != 401 ]; then echo "Unauthenticated API returned HTTP $unauthorized_status, expected 401" >&2; exit 1; fi

ln -sfn "$release" "$remote_root/current"
rm -f -- secrets/migration_database_url
activated=1
trap - EXIT INT TERM HUP
docker compose ps
printf 'Activated and verified release %s\n' "$version"
