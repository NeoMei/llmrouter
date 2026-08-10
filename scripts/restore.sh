#!/usr/bin/env bash
set -euo pipefail

verify_restore_checksum() {
  local restore_file=$1 sidecar expected_digest ignored actual_digest
  sidecar="$restore_file.sha256"
  [[ -f "$sidecar" ]] || { echo "restore checksum sidecar is required: $sidecar" >&2; return 1; }
  read -r expected_digest ignored < "$sidecar" || true
  [[ "$expected_digest" =~ ^[0-9A-Fa-f]{64}$ ]] || { echo "restore checksum sidecar is invalid" >&2; return 1; }
  actual_digest=$(shasum -a 256 "$restore_file" | awk '{print $1}')
  expected_digest=$(printf '%s' "$expected_digest" | tr 'A-F' 'a-f')
  [[ "$expected_digest" == "$actual_digest" ]] || { echo "restore checksum mismatch" >&2; return 1; }
}

if [[ "${1:-}" == "--check" ]]; then
  : "${DB_TYPE:?DB_TYPE is required}"
  : "${RESTORE_FILE:?RESTORE_FILE is required}"
  [[ -f "$RESTORE_FILE" ]] || { echo "RESTORE_FILE does not exist" >&2; exit 1; }
  verify_restore_checksum "$RESTORE_FILE"
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
verify_restore_checksum "$RESTORE_FILE"

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
