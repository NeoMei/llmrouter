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
    [[ -f "$SQLITE_PATH" && -r "$SQLITE_PATH" ]] || { echo "SQLite database is not a readable file" >&2; exit 1; }
    command -v python3 >/dev/null || { echo "Python 3 with sqlite3 online backup support is required" >&2; exit 1; }
    timestamp=""
    if [[ "$mode" != "--check" ]]; then
      timestamp=$(date -u +%Y%m%dT%H%M%SZ)
    fi
    python3 - "$mode" "$SQLITE_PATH" "$BACKUP_DIR" "$timestamp" <<'PYTHON'
import contextlib
import hashlib
import os
from pathlib import Path
import sqlite3
import sys
import tempfile
import time

try:
    if not callable(getattr(sqlite3.Connection, "backup", None)):
        raise RuntimeError("Python sqlite3 online backup support is required")
    mode, source_name, backup_name, timestamp = sys.argv[1:]
    # URI escaping preserves literal ?, #, quotes and spaces in filesystem paths.
    source_uri = Path(source_name).absolute().as_uri() + "?mode=ro"
    deadline = time.monotonic() + 30
    with contextlib.closing(sqlite3.connect(source_uri, uri=True, timeout=5)) as source:
        source.set_progress_handler(lambda: time.monotonic() >= deadline, 1000)
        source.execute("BEGIN")
        source.execute("PRAGMA schema_version").fetchone()
        if mode == "--check":
            print("backup configuration: ok")
            sys.exit(0)

        backup_dir = Path(backup_name)
        backup_dir.mkdir(mode=0o700, parents=True, exist_ok=True)
        destination = backup_dir / f"gateway-{timestamp}.sqlite"
        sidecar = Path(str(destination) + ".sha256")
        if os.path.lexists(destination) or os.path.lexists(sidecar):
            raise FileExistsError("backup or checksum already exists for this timestamp")

        def check_deadline(status, remaining, total):
            if time.monotonic() >= deadline:
                raise TimeoutError("SQLite online backup exceeded 30 seconds")

        with tempfile.TemporaryDirectory(prefix=".sqlite-backup-", dir=backup_dir) as pending:
            snapshot = Path(pending) / "snapshot.sqlite"
            checksum = Path(pending) / "snapshot.sha256"
            with contextlib.closing(sqlite3.connect(snapshot)) as target:
                source.backup(target, pages=64, progress=check_deadline, sleep=0.05)
                target.set_progress_handler(lambda: time.monotonic() >= deadline, 1000)
                if target.execute("PRAGMA integrity_check").fetchall() != [("ok",)]:
                    raise RuntimeError("SQLite backup integrity check failed")
            snapshot.chmod(0o600)
            digest = hashlib.sha256()
            with snapshot.open("rb") as data:
                for chunk in iter(lambda: data.read(1024 * 1024), b""):
                    digest.update(chunk)
                os.fsync(data.fileno())
            with open(checksum, "x", opener=lambda path, flags: os.open(path, flags, 0o600)) as data:
                data.write(f"{digest.hexdigest()}  {destination}\n")
                data.flush()
                os.fsync(data.fileno())

            # Hard-link publication is atomic and exclusive even if another backup races us.
            published = []
            try:
                os.link(snapshot, destination)
                published.append(destination)
                os.link(checksum, sidecar)
                published.append(sidecar)
                directory_fd = os.open(backup_dir, os.O_RDONLY)
                try:
                    os.fsync(directory_fd)
                finally:
                    os.close(directory_fd)
            except BaseException:
                for artifact in reversed(published):
                    artifact.unlink()
                raise
        print(destination)
except Exception as error:
    print(f"SQLite backup failed: {error}", file=sys.stderr)
    sys.exit(1)
PYTHON
    exit 0
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
