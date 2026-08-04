#!/bin/sh
set -u

pass=0
warn=0
check() {
  label="$1"; shift
  if "$@" >/dev/null 2>&1; then
    printf 'PASS  %s\n' "$label"; pass=$((pass+1))
  else
    printf 'WARN  %s\n' "$label"; warn=$((warn+1))
  fi
}
rpc_check() {
  curl -fsS --max-time 12 -H 'content-type: application/json' \
    --data '{"jsonrpc":"2.0","method":"chain_getHeader","params":[],"id":1}' "$1" |
    grep -q '"result"'
}

printf 'Shiden Guardian preflight (read-only)\n'
printf 'timestamp:    %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
printf 'hostname:     %s\n' "$(hostname)"
printf 'kernel/arch:  %s\n' "$(uname -srmo)"
printf 'os:           %s\n' "$(. /etc/os-release 2>/dev/null; printf '%s %s' "${NAME:-unknown}" "${VERSION_ID:-unknown}")"
printf 'cpu:          %s cores\n' "$(getconf _NPROCESSORS_ONLN 2>/dev/null || printf '?')"
printf 'memory:       %s\n' "$(awk '/MemTotal/{printf "%.1f GiB",$2/1024/1024}' /proc/meminfo 2>/dev/null)"
printf 'root disk:    %s\n' "$(df -hP / | awk 'NR==2{print $3" used / "$2" ("$5")"}')"
printf 'journald use: %s\n' "$(journalctl --disk-usage 2>/dev/null || printf 'unavailable')"

printf '\nAstar service definition\n'
systemctl cat astar.service --no-pager 2>&1 || true
printf '\nAstar service state\n'
systemctl show astar.service --no-pager \
  --property=LoadState,ActiveState,SubState,NRestarts,ExecMainStartTimestamp,ExecStart,Restart,RestartSec 2>&1
exec_start="$(systemctl show astar.service --property=ExecStart --value 2>/dev/null || true)"
printf '\nExpected identity and ports\n'
printf '%s\n' "$exec_start" | grep -F -- '--chain shiden' || printf 'WARN  --chain shiden not found in ExecStart\n'
printf '%s\n' "$exec_start" | grep -F -- '--name tk_sdn_collator' || printf 'WARN  --name tk_sdn_collator not found in ExecStart\n'
printf '%s\n' "$exec_start" | grep -E -- '--prometheus-port(=| )9615|9615' || printf 'INFO  parachain metrics port is not explicit in ExecStart\n'
printf '%s\n' "$exec_start" | grep -E -- '9616' || printf 'INFO  relay metrics port is not explicit in ExecStart\n'
ss -lntup 2>/dev/null | grep -E ':(80|443|9615|9616|9944|9945|30333|30334)\b' || true
check 'parachain metrics on 127.0.0.1:9615' curl -fsS --max-time 3 http://127.0.0.1:9615/metrics
check 'relay metrics on 127.0.0.1:9616' curl -fsS --max-time 3 http://127.0.0.1:9616/metrics
check 'parachain JSON-RPC on 127.0.0.1:9944' rpc_check http://127.0.0.1:9944
metrics_are_private() {
  ss -lntH | awk '
    $4 ~ /:(9615|9616)$/ &&
    $4 !~ /^127\.0\.0\.1:/ &&
    $4 !~ /^\[::1\]:/ { exposed=1 }
    END { exit exposed }
  '
}
check 'metrics ports are not externally bound' metrics_are_private
rpc_is_private() {
  ss -lntH | awk '
    $4 ~ /:9944$/ &&
    $4 !~ /^127\.0\.0\.1:/ &&
    $4 !~ /^\[::1\]:/ { exposed=1 }
    END { exit exposed }
  '
}
check 'parachain RPC port is not externally bound' rpc_is_private

printf '\nRuntime and port ownership\n'
check 'Docker engine is usable by tk' docker info
check 'Docker Compose v2 is available' docker compose version
if ss -lntH 2>/dev/null | awk '$4 ~ /:80$|:443$/ {found=1} END{exit !found}'; then
  printf 'INFO  port 80/443 already has a listener; inspect ownership above before activation\n'
else
  printf 'PASS  ports 80/443 are currently free\n'; pass=$((pass+1))
fi
if command -v ufw >/dev/null 2>&1; then ufw status 2>&1 || printf 'INFO  ufw status requires elevated read access\n'; else printf 'INFO  ufw is not installed\n'; fi

printf '\nOutbound dependencies\n'
check 'DNS resolution' getent ahosts shiden-rpc.n.dwellir.com
check 'Shiden RPC A' rpc_check https://shiden-rpc.n.dwellir.com
check 'Shiden RPC B' rpc_check https://shiden.api.onfinality.io/public
check 'HTTPS certificate path' curl -fsSI --max-time 12 https://ai.google.dev/
if [ -n "${PREFLIGHT_SMTP_HOST:-}" ]; then
  check 'SMTP TCP reachability' sh -c "timeout 8 bash -c '</dev/tcp/${PREFLIGHT_SMTP_HOST}/${PREFLIGHT_SMTP_PORT:-587}'"
else
  printf 'INFO  SMTP check skipped; set PREFLIGHT_SMTP_HOST and optional PREFLIGHT_SMTP_PORT\n'
fi

printf '\nSummary: %s pass, %s warning(s)\n' "$pass" "$warn"
if [ "$warn" -gt 0 ]; then exit 2; fi
