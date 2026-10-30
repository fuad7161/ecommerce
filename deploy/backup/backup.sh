#!/bin/sh
# Runs inside the backup container: dumps the database once a day at BACKUP_HOUR (Dhaka time)
# and deletes dumps older than BACKUP_KEEP_DAYS.
set -eu
echo "backup service started: daily at ${BACKUP_HOUR}:00, keeping ${BACKUP_KEEP_DAYS} days"
while true; do
  now_h=$(date +%H | sed 's/^0//'); now_m=$(date +%M | sed 's/^0//')
  if [ "${now_h:-0}" -eq "$BACKUP_HOUR" ] && [ "${now_m:-0}" -lt 5 ]; then
    f="/backups/db-$(date +%Y%m%d-%H%M%S).sql.gz"
    if pg_dump --no-owner | gzip > "$f.tmp"; then
      mv "$f.tmp" "$f"; echo "backup written: $f ($(du -h "$f" | cut -f1))"
    else
      rm -f "$f.tmp"; echo "backup FAILED" >&2
    fi
    find /backups -name 'db-*.sql.gz' -mtime +"$BACKUP_KEEP_DAYS" -delete
    sleep 600
  fi
  sleep 60
done
