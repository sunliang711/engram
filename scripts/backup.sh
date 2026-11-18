#!/usr/bin/env bash
set -euo pipefail

BACKUP_DIR="${BACKUP_DIR:-./backups}"
PG_DSN="${MEMORY_DATABASE_DSN:-postgres://postgres:postgres@localhost:5432/ai_memory?sslmode=disable}"
RETENTION_DAYS="${RETENTION_DAYS:-14}"
TIMESTAMP="$(date -u +%Y%m%dT%H%M%SZ)"
OUT_FILE="${BACKUP_DIR}/ai_memory_${TIMESTAMP}.dump"

mkdir -p "${BACKUP_DIR}"

pg_dump --format=custom --no-owner --no-privileges --dbname="${PG_DSN}" --file="${OUT_FILE}"
shasum -a 256 "${OUT_FILE}" > "${OUT_FILE}.sha256"

find "${BACKUP_DIR}" -type f -name "ai_memory_*.dump*" -mtime +"${RETENTION_DAYS}" -delete

echo "Backup completed: ${OUT_FILE}"
