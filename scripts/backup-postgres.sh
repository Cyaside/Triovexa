#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo "usage: $0 <database-url> <backup-file>" >&2
  exit 2
fi

database_url="$1"
backup_file="$2"
mkdir -p "$(dirname "$backup_file")"
pg_dump --format=custom --no-owner --no-privileges --file "$backup_file" "$database_url"
pg_restore --list "$backup_file" >/dev/null
echo "backup verified: $backup_file"
