#!/bin/sh
set -u

payload='{"jsonrpc":"2.0","method":"chain_getHeader","params":[],"id":1}'
check_rpc() {
  name="$1"
  url="$2"
  if response="$(curl -fsS --max-time 12 -H 'content-type: application/json' --data "$payload" "$url")" &&
     printf '%s' "$response" | grep -q '"result"'; then
    block="$(printf '%s' "$response" | sed -n 's/.*"number":"\([^"]*\)".*/\1/p')"
    printf 'PASS  %-12s %s  block=%s\n' "$name" "$url" "${block:-unknown}"
  else
    printf 'WARN  %-12s %s\n' "$name" "$url"
  fi
}

check_rpc Dwellir https://shiden-rpc.n.dwellir.com
check_rpc OnFinality https://shiden.api.onfinality.io/public
check_rpc BlastAPI https://shiden.public.blastapi.io
