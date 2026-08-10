#!/usr/bin/env bash
set -euo pipefail

root_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
compose_file=${INTEGRATION_COMPOSE_FILE:-$root_dir/docker-compose.test.yml}
artifact_dir=${ARTIFACT_DIR:-$root_dir/artifacts/release}
commit=${RELEASE_COMMIT:-$(git -C "$root_dir" rev-parse HEAD)}
command -v docker >/dev/null 2>&1 || { echo "docker is required" >&2; exit 2; }
command -v jq >/dev/null 2>&1 || { echo "jq is required" >&2; exit 2; }
mkdir -p "$artifact_dir"
dump_file=$(mktemp)
trap 'rm -f "$dump_file"' EXIT

started=$(date +%s)
docker compose -f "$compose_file" exec -T postgres \
  pg_dump --username gateway --format custom --no-owner --no-acl gateway > "$dump_file"
[[ -s "$dump_file" ]] || { echo "restore rehearsal dump is empty" >&2; exit 1; }
docker compose -f "$compose_file" exec -T postgres \
  psql --username gateway --dbname postgres --set ON_ERROR_STOP=1 \
  --command 'drop database if exists gateway_restore' \
  --command 'create database gateway_restore'
docker compose -f "$compose_file" exec -T postgres \
  pg_restore --username gateway --dbname gateway_restore --no-owner --no-acl < "$dump_file"
table_count=$(docker compose -f "$compose_file" exec -T postgres \
  psql --username gateway --dbname gateway_restore --tuples-only --no-align \
  --command "select count(*) from information_schema.tables where table_schema='public'" | tr -d '[:space:]')
[[ "$table_count" =~ ^[1-9][0-9]*$ ]] || { echo "restored database has no public tables" >&2; exit 1; }

if command -v sha256sum >/dev/null 2>&1; then
  checksum=$(sha256sum "$dump_file" | awk '{print $1}')
else
  checksum=$(shasum -a 256 "$dump_file" | awk '{print $1}')
fi
rto_seconds=$(($(date +%s) - started))
((rto_seconds > 0)) || rto_seconds=1
jq -n --arg commit "$commit" --arg tested_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  --arg backup_checksum "$checksum" --argjson rto_seconds "$rto_seconds" --argjson table_count "$table_count" \
  '{commit:$commit,status:"pass",tested_at:$tested_at,backup_checksum:$backup_checksum,rto_seconds:$rto_seconds,restored_table_count:$table_count}' \
  > "$artifact_dir/restore-rehearsal.json"

echo "restore rehearsal: passed ($table_count tables, ${rto_seconds}s)"
