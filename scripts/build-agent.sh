#!/bin/sh
set -eu
root="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
cd "$root"
mkdir -p release/bin
docker build --target agent-artifact -f deploy/Dockerfile.backend -t shiden-guardian-agent-artifact .
container="$(docker create shiden-guardian-agent-artifact /shiden-guardian-agent)"
trap 'docker rm -f "$container" >/dev/null 2>&1 || true' EXIT
docker cp "$container:/shiden-guardian-agent" release/bin/shiden-guardian-agent
chmod 0755 release/bin/shiden-guardian-agent
sha256sum release/bin/shiden-guardian-agent > release/bin/shiden-guardian-agent.sha256
