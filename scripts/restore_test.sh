#!/usr/bin/env bash
set -euo pipefail

root_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
tmp_dir=$(mktemp -d)
trap 'rm -rf "$tmp_dir"' EXIT

restore_file="$tmp_dir/restore.sqlite"
benign_file="$tmp_dir/benign.sqlite"
destination="$tmp_dir/destination.sqlite"
printf 'attacker-controlled restore\n' > "$restore_file"
printf 'known-good backup\n' > "$benign_file"
printf 'original database\n' > "$destination"

shasum -a 256 "$benign_file" > "$restore_file.sha256"
set +e
DB_TYPE=sqlite RESTORE_FILE="$restore_file" SQLITE_PATH="$destination" \
  bash "$root_dir/scripts/restore.sh" --confirm >/dev/null 2>&1
mismatch_status=$?
set -e
test "$mismatch_status" -ne 0
grep -Fqx 'original database' "$destination"

rm "$restore_file.sha256"
set +e
DB_TYPE=sqlite RESTORE_FILE="$restore_file" SQLITE_PATH="$destination" \
  bash "$root_dir/scripts/restore.sh" --confirm >/dev/null 2>&1
missing_status=$?
set -e
test "$missing_status" -ne 0
grep -Fqx 'original database' "$destination"

shasum -a 256 "$restore_file" > "$restore_file.sha256"
DB_TYPE=sqlite RESTORE_FILE="$restore_file" SQLITE_PATH="$destination" \
  bash "$root_dir/scripts/restore.sh" --confirm >/dev/null
cmp -s "$restore_file" "$destination"

echo 'restore integrity checks: ok'
