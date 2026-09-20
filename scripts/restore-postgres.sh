#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo "usage: $0 <database-url> <backup-file>" >&2
  exit 2
fi

database_url="$1"
backup_file="$2"
pg_restore --clean --if-exists --no-owner --no-privileges --dbname "$database_url" "$backup_file"
psql "$database_url" -v ON_ERROR_STOP=1 -c "SELECT version FROM schema_migrations ORDER BY version;"
echo "restore verified: $database_url"
