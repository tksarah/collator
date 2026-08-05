#!/bin/sh
set -eu
umask 077

if [ "$(id -u)" -ne 0 ]; then echo "Run controller activation with sudo." >&2; exit 1; fi
remote_root="$(readlink -f -- "${1:?remote root required}")"
version="${2:?version required}"
test "$#" -le 3 || { echo "Usage: $0 REMOTE_ROOT VERSION [--acknowledge-pruned-gap]" >&2; exit 64; }
acknowledge_pruned_gap=0
case "${3:-}" in
  "") ;;
  --acknowledge-pruned-gap) acknowledge_pruned_gap=1 ;;
  *) echo "Usage: $0 REMOTE_ROOT VERSION [--acknowledge-pruned-gap]" >&2; exit 64;;
esac
case "$version" in
  [0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]T[0-9][0-9][0-9][0-9][0-9][0-9]Z) ;;
  *) echo "Release version must have the form YYYYMMDDTHHMMSSZ." >&2; exit 64;;
esac
release="$remote_root/releases/$version"
test -d "$release" || { echo "Target release does not exist." >&2; exit 1; }
previous="$(readlink -f -- "$remote_root/current")"
case "$previous" in "$remote_root"/releases/*) ;; *) echo "Current release escaped the release root." >&2; exit 1;; esac
test "$previous" != "$release" || { echo "Target release is already current." >&2; exit 1; }

runtime_secrets="api_database_url auth_database_url controller_database_url backup_password postgres_password action_broker_key gemini_api_key smtp_password bootstrap_token encryption_key"
for file in .env compose.yaml deploy/Dockerfile.backend; do
	test -f "$previous/$file" && test ! -L "$previous/$file" || { echo "Current runtime definition is not a regular file: $file" >&2; exit 1; }
	test -f "$release/$file" && test ! -L "$release/$file" || { echo "Target runtime definition is not a regular file: $file" >&2; exit 1; }
	cmp -s "$previous/$file" "$release/$file" || { echo "App-only activation refuses runtime definition drift: $file" >&2; exit 1; }
done
for file in $runtime_secrets; do
	test -f "$previous/secrets/$file" && test ! -L "$previous/secrets/$file" || { echo "Current runtime secret is not a regular file: $file" >&2; exit 1; }
	test -f "$release/secrets/$file" && test ! -L "$release/secrets/$file" || { echo "Target runtime secret is not a regular file: $file" >&2; exit 1; }
  cmp -s "$previous/secrets/$file" "$release/secrets/$file" || { echo "Runtime secret changed in app-only release: $file" >&2; exit 1; }
done
cmp -s "$previous/.env" "$release/.env" || { echo "Runtime environment changed in app-only release." >&2; exit 1; }
test ! -e "$release/secrets/database_url"
test ! -e "$release/secrets/migration_database_url"
test -S /run/shiden-guardian/observe/agent.sock
test -S /run/shiden-guardian/control/agent.sock

cd "$previous"
docker info >/dev/null
docker compose config --quiet
docker compose exec -T controller /usr/local/bin/controller -healthcheck
pending_actions="$(docker compose exec -T postgres psql -U guardian -d guardian -Atc "SELECT count(*) FROM remediation_actions WHERE status IN ('pending','running')")"
case "$pending_actions" in *[!0-9]*|'') echo "Could not read pending action count." >&2; exit 1;; esac
test "$pending_actions" -eq 0 || { echo "Refusing controller activation while actions are pending or running." >&2; exit 1; }
reward_actions_before="$(docker compose exec -T postgres psql -U guardian -d guardian -Atc "SELECT count(*) FROM remediation_actions a JOIN incidents i ON i.id=a.incident_id WHERE i.fingerprint LIKE 'reward-%'")"
cursor_before="$(docker compose exec -T postgres psql -U guardian -d guardian -Atc "SELECT last_scanned_block FROM reward_monitor_state WHERE id=1")"
gap_audits_before="$(docker compose exec -T postgres psql -U guardian -d guardian -Atc "SELECT count(*) FROM audit_events WHERE action='reward.history_gap.acknowledge'")"
case "$reward_actions_before:$cursor_before:$gap_audits_before" in *[!0-9:]*) echo "Could not read reward activation baseline." >&2; exit 1;; esac

services="postgres api auth-broker caddy prometheus backup"
service_ids_before=""
for service in $services; do
  service_ids_before="$service_ids_before $service=$(docker compose ps -q "$service")"
done
astar_restarts_before="$(systemctl show astar.service --property=NRestarts --value)"
agent_restarts_before="$(systemctl show shiden-guardian-agent.service --property=NRestarts --value)"
controller_id="$(docker compose ps -q controller)"
test -n "$controller_id" || { echo "Current controller container is missing." >&2; exit 1; }
old_image="$(docker inspect --format '{{.Image}}' "$controller_id")"
test -n "$old_image" || { echo "Could not resolve current controller image." >&2; exit 1; }
rollback_tag="shiden-guardian-controller:rollback-$version"
docker tag "$old_image" "$rollback_tag"

cd "$release"
docker compose build controller
new_image="$(docker image inspect shiden-guardian-controller --format '{{.Id}}')"
test -n "$new_image" && test "$new_image" != "$old_image" || { echo "Controller build did not produce a new image." >&2; exit 1; }

started=0
activated=0
rebase_applied=0
rollback() {
  status=$?
  trap - EXIT INT TERM HUP
  if [ "$started" -eq 1 ] && [ "$activated" -ne 1 ]; then
    printf 'Controller activation failed; restoring the previous image and release.\n' >&2
    docker tag "$rollback_tag" shiden-guardian-controller:latest || true
    cd "$previous"
    docker compose up -d --no-deps --force-recreate --wait --wait-timeout 90 controller || true
    ln -sfn "$previous" "$remote_root/current"
    if [ "$rebase_applied" -eq 1 ]; then
      printf 'The operator-approved history-gap acknowledgement remains recorded; the old controller resumes from the rebased cursor.\n' >&2
    fi
  fi
  exit "$status"
}
trap rollback EXIT
trap 'exit 130' INT
trap 'exit 143' TERM HUP

started=1
cursor_validation_floor="$cursor_before"
if [ "$acknowledge_pruned_gap" -eq 1 ]; then
  cd "$previous"
  docker compose stop controller
  cd "$release"
  # The controller image already declares /usr/local/bin/controller as its
  # ENTRYPOINT. Supplying the binary again would pass it as argv[1] and start
  # the normal long-running controller instead of the one-shot recovery mode.
  docker compose run --rm --no-deps controller -acknowledge-pruned-reward-gap "$cursor_before"
  rebase_applied=1
  cursor_validation_floor="$(docker compose exec -T postgres psql -U guardian -d guardian -Atc "SELECT last_scanned_block FROM reward_monitor_state WHERE id=1")"
  gap_audits_after="$(docker compose exec -T postgres psql -U guardian -d guardian -Atc "SELECT count(*) FROM audit_events WHERE action='reward.history_gap.acknowledge'")"
  historical_gap="$(docker compose exec -T postgres psql -U guardian -d guardian -Atc "SELECT COALESCE(payload->'sources'->>'historical_gap','') FROM reward_monitor_state WHERE id=1")"
  gap_audit_details="$(docker compose exec -T postgres psql -U guardian -d guardian -Atc "SELECT (details->>'from_block')||'|'||(details->>'through_block')||'|'||(details->>'resume_from_block')||'|'||(details->>'history_recovered')||'|'||(details->>'reason') FROM audit_events WHERE action='reward.history_gap.acknowledge' ORDER BY created_at DESC LIMIT 1")"
  case "$cursor_validation_floor:$gap_audits_after" in *[!0-9:]*) echo "Could not verify acknowledged reward gap." >&2; exit 1;; esac
  test "$cursor_validation_floor" -gt "$cursor_before" || { echo "Acknowledged reward cursor did not advance." >&2; exit 1; }
  test "$gap_audits_after" -eq $((gap_audits_before + 1)) || { echo "Reward gap acknowledgement audit was not recorded exactly once." >&2; exit 1; }
  expected_gap="acknowledged_pruned_blocks_$((cursor_before + 1))_${cursor_validation_floor}"
  test "$historical_gap" = "$expected_gap" || { echo "Historical reward gap marker is incorrect." >&2; exit 1; }
  expected_audit="$((cursor_before + 1))|${cursor_validation_floor}|$((cursor_validation_floor + 1))|false|local_state_pruned"
  test "$gap_audit_details" = "$expected_audit" || { echo "Historical reward gap audit details are incorrect." >&2; exit 1; }
fi
docker compose up -d --no-deps --force-recreate --wait --wait-timeout 90 controller
docker compose exec -T controller /usr/local/bin/controller -healthcheck
docker compose exec -T auth-broker /usr/local/bin/auth-broker -healthcheck
docker compose exec -T api /usr/local/bin/api -healthcheck

for item in $service_ids_before; do
  service="${item%%=*}"
  expected="${item#*=}"
  actual="$(docker compose ps -q "$service")"
  test -n "$expected" && test "$actual" = "$expected" || { echo "Non-controller service changed: $service" >&2; exit 1; }
done
test "$(systemctl show astar.service --property=ActiveState --value)" = active
test "$(systemctl show shiden-guardian-agent.service --property=ActiveState --value)" = active
test "$(systemctl show astar.service --property=NRestarts --value)" = "$astar_restarts_before"
test "$(systemctl show shiden-guardian-agent.service --property=NRestarts --value)" = "$agent_restarts_before"
for file in $runtime_secrets; do cmp -s "$previous/secrets/$file" "$release/secrets/$file"; done

domain="$(sed -n 's/^DOMAIN=//p' .env | tail -n 1)"
test -n "$domain" || { echo "DOMAIN is empty." >&2; exit 1; }
curl --resolve "$domain:443:127.0.0.1" -fsS --max-time 8 "https://$domain/healthz" >/dev/null
unauthorized_status="$(curl --resolve "$domain:443:127.0.0.1" -sS --max-time 8 -o /dev/null -w '%{http_code}' "https://$domain/api/v1/overview")"
test "$unauthorized_status" = 401 || { echo "Unauthenticated API returned HTTP $unauthorized_status." >&2; exit 1; }
reward_actions_after="$(docker compose exec -T postgres psql -U guardian -d guardian -Atc "SELECT count(*) FROM remediation_actions a JOIN incidents i ON i.id=a.incident_id WHERE i.fingerprint LIKE 'reward-%'")"
test "$reward_actions_after" = "$reward_actions_before" || { echo "Reward monitoring created a remediation action." >&2; exit 1; }

ln -sfn "$release" "$remote_root/current"
deadline=$(( $(date +%s) + 600 ))
caught_up=0
while [ "$(date +%s)" -le "$deadline" ]; do
  state="$(docker compose exec -T postgres psql -U guardian -d guardian -Atc "SELECT last_scanned_block||'|'||COALESCE((payload->>'finalized_block')::bigint,0)||'|'||CASE WHEN COALESCE(payload->>'gap','')='' THEN '1' ELSE '0' END FROM reward_monitor_state WHERE id=1")"
  old_ifs="$IFS"; IFS='|'; set -- $state; IFS="$old_ifs"
  cursor="${1:-0}"; finalized="${2:-0}"; gap_clear="${3:-0}"
  case "$cursor:$finalized:$gap_clear" in *[!0-9:]*) cursor=0; finalized=0; gap_clear=0;; esac
  if [ "$cursor" -ge "$cursor_validation_floor" ] && [ "$finalized" -ge "$cursor" ] && [ $((finalized-cursor)) -le 16 ] && [ "$gap_clear" -eq 1 ]; then
    caught_up=1
    break
  fi
  sleep 15
done
test "$caught_up" -eq 1 || { echo "Reward cursor did not catch up within 10 minutes." >&2; exit 1; }
reward_actions_final="$(docker compose exec -T postgres psql -U guardian -d guardian -Atc "SELECT count(*) FROM remediation_actions a JOIN incidents i ON i.id=a.incident_id WHERE i.fingerprint LIKE 'reward-%'")"
test "$reward_actions_final" = "$reward_actions_before" || { echo "Reward catch-up created a remediation action." >&2; exit 1; }
if [ "$rebase_applied" -eq 1 ]; then
  test "$(docker compose exec -T postgres psql -U guardian -d guardian -Atc "SELECT count(*) FROM audit_events WHERE action='reward.history_gap.acknowledge'")" = "$gap_audits_after" || { echo "Reward gap acknowledgement audit count changed during catch-up." >&2; exit 1; }
  test "$(docker compose exec -T postgres psql -U guardian -d guardian -Atc "SELECT COALESCE(payload->'sources'->>'historical_gap','') FROM reward_monitor_state WHERE id=1")" = "$expected_gap" || { echo "Historical reward gap marker was lost during catch-up." >&2; exit 1; }
fi

activated=1
trap - EXIT INT TERM HUP
docker compose ps
if [ "$rebase_applied" -eq 1 ]; then
  printf 'Operator-approved pruned reward history was acknowledged in the audit log; no reward events were synthesized.\n'
fi
printf 'Controller-only release %s activated; reward cursor caught up without credential changes.\n' "$version"
