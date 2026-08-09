#!/usr/bin/env bash
set -euo pipefail

if [[ "${1:-}" == "--check" ]]; then
  : "${DB_TYPE:?DB_TYPE is required}"
  : "${RESTORE_FILE:?RESTORE_FILE is required}"
  [[ -f "$RESTORE_FILE" ]] || { echo "RESTORE_FILE does not exist" >&2; exit 1; }
  echo "restore configuration: ok"
  exit 0
fi
if [[ "${1:-}" != "--confirm" ]]; then
  echo "restore is destructive; pass --confirm" >&2
  exit 2
fi

: "${DB_TYPE:?DB_TYPE is required}"
: "${RESTORE_FILE:?RESTORE_FILE is required}"
[[ -f "$RESTORE_FILE" ]] || { echo "RESTORE_FILE does not exist" >&2; exit 1; }
if [[ -f "$RESTORE_FILE.sha256" ]]; then
  shasum -a 256 -c "$RESTORE_FILE.sha256"
fi

case "$DB_TYPE" in
  sqlite)
    : "${SQLITE_PATH:?SQLITE_PATH is required}"
    cp -p "$RESTORE_FILE" "$SQLITE_PATH"
    ;;
  postgres)
    : "${PGHOST:?PGHOST is required}"
    : "${PGUSER:?PGUSER is required}"
    : "${PGDATABASE:?PGDATABASE is required}"
    : "${PGPASSWORD:?PGPASSWORD is required}"
    pg_restore --clean --if-exists --no-owner --dbname "$PGDATABASE" "$RESTORE_FILE"
    ;;
  mysql)
    : "${MYSQL_HOST:?MYSQL_HOST is required}"
    : "${MYSQL_USER:?MYSQL_USER is required}"
    : "${MYSQL_DATABASE:?MYSQL_DATABASE is required}"
    : "${MYSQL_PASSWORD:?MYSQL_PASSWORD is required}"
    MYSQL_PWD="$MYSQL_PASSWORD" mysql --host "$MYSQL_HOST" --user "$MYSQL_USER" "$MYSQL_DATABASE" < "$RESTORE_FILE"
    ;;
  *) echo "unsupported DB_TYPE: $DB_TYPE" >&2; exit 1 ;;
esac
