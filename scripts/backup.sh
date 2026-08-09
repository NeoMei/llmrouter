#!/usr/bin/env bash
set -euo pipefail

mode=${1:-}
if [[ "$mode" != "--check" && -n "$mode" ]]; then
  echo "usage: $0 [--check]" >&2
  exit 2
fi

: "${DB_TYPE:?DB_TYPE is required: sqlite, postgres, or mysql}"
: "${BACKUP_DIR:?BACKUP_DIR is required}"

case "$DB_TYPE" in
  sqlite)
    : "${SQLITE_PATH:?SQLITE_PATH is required}"
    [[ -f "$SQLITE_PATH" ]] || { echo "SQLite database does not exist" >&2; exit 1; }
    ;;
  postgres)
    : "${PGHOST:?PGHOST is required}"
    : "${PGUSER:?PGUSER is required}"
    : "${PGDATABASE:?PGDATABASE is required}"
    : "${PGPASSWORD:?PGPASSWORD is required}"
    command -v pg_dump >/dev/null || { echo "pg_dump is required" >&2; exit 1; }
    ;;
  mysql)
    : "${MYSQL_HOST:?MYSQL_HOST is required}"
    : "${MYSQL_USER:?MYSQL_USER is required}"
    : "${MYSQL_DATABASE:?MYSQL_DATABASE is required}"
    : "${MYSQL_PASSWORD:?MYSQL_PASSWORD is required}"
    command -v mysqldump >/dev/null || { echo "mysqldump is required" >&2; exit 1; }
    ;;
  *) echo "unsupported DB_TYPE: $DB_TYPE" >&2; exit 1 ;;
esac

if [[ "$mode" == "--check" ]]; then
  echo "backup configuration: ok"
  exit 0
fi

mkdir -p "$BACKUP_DIR"
timestamp=$(date -u +%Y%m%dT%H%M%SZ)
case "$DB_TYPE" in
  sqlite)
    destination="$BACKUP_DIR/gateway-$timestamp.sqlite"
    cp -p "$SQLITE_PATH" "$destination"
    ;;
  postgres)
    destination="$BACKUP_DIR/gateway-$timestamp.pgdump"
    pg_dump --format=custom --no-owner --file "$destination"
    ;;
  mysql)
    destination="$BACKUP_DIR/gateway-$timestamp.sql"
    MYSQL_PWD="$MYSQL_PASSWORD" mysqldump --single-transaction --host "$MYSQL_HOST" --user "$MYSQL_USER" "$MYSQL_DATABASE" > "$destination"
    ;;
esac
shasum -a 256 "$destination" > "$destination.sha256"
echo "$destination"
