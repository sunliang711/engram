#!/usr/bin/env bash
set -euo pipefail

if [ $# -lt 2 ]; then
  echo "Usage: $0 <dump_file> <target_verify_dsn>" >&2
  exit 1
fi

DUMP_FILE="$1"
VERIFY_DSN="$2"

if [ -f "${DUMP_FILE}.sha256" ]; then
  shasum -a 256 -c "${DUMP_FILE}.sha256"
fi

pg_restore --clean --if-exists --no-owner --no-privileges --dbname="${VERIFY_DSN}" "${DUMP_FILE}"

psql "${VERIFY_DSN}" -v ON_ERROR_STOP=1 -c "
SELECT
  (SELECT count(*) FROM tenants) AS tenants_count,
  (SELECT count(*) FROM projects) AS projects_count,
  (SELECT count(*) FROM memories) AS memories_count,
  (SELECT count(*) FROM memory_revisions) AS revisions_count,
  (SELECT count(*) FROM memory_events) AS events_count;
"

echo "Restore verification passed for ${DUMP_FILE}"
