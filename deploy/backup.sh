#!/bin/sh
set -eu
export PGPASSWORD="$(cat "$PGPASSWORD_FILE")"
while true; do
  stamp="$(date -u +%Y%m%dT%H%M%SZ)"
  pg_dump --format=custom --file="/backups/guardian-$stamp.dump"
  find /backups -type f -name 'guardian-*.dump' -mtime +7 -delete
  sleep 86400
done
