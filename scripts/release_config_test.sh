#!/usr/bin/env bash
set -euo pipefail

root_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$root_dir"

grep -q 'github.com/QuantumNous/new-api/common.Version' .github/workflows/release.yml
if grep -Fq -- "-X 'new-api/common.Version" .github/workflows/release.yml; then
  echo "release workflow still contains the invalid linker symbol" >&2
  exit 1
fi

grep -Fq '${APP_IMAGE:?APP_IMAGE is required}@${APP_IMAGE_DIGEST:?APP_IMAGE_DIGEST is required}' docker-compose.yml
grep -Fq 'MANAGEMENT_TOKEN: ${MANAGEMENT_TOKEN:?MANAGEMENT_TOKEN is required}' docker-compose.yml
grep -Fq 'Authorization: Bearer $${MANAGEMENT_TOKEN}' docker-compose.yml
if grep -Eq '(^|[^[:alnum:]])latest([^[:alnum:]]|$)|123456' docker-compose.yml; then
  echo "production compose contains a mutable tag or example password" >&2
  exit 1
fi

grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+-rc\.[0-9]+$' VERSION
test -f .github/workflows/ci.yml
test -f .env.production.example

for script in release-preflight.sh backup.sh restore.sh rollback.sh; do
  test -x "scripts/$script"
done

env \
  RUN_MODE=production \
  APP_IMAGE=ghcr.io/example/codex-gateway \
  APP_IMAGE_DIGEST=sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa \
  SESSION_SECRET=session-secret-that-is-at-least-32-bytes \
  GATEWAY_AES_KEY=gateway-key-that-is-at-least-32-bytes \
  MANAGEMENT_TOKEN=management-token-that-is-at-least-32-bytes \
  SQL_DSN='gateway:strong-db-secret@tcp(db:3306)/gateway' \
  REDIS_CONN_STRING='redis://default:strong-redis-secret@redis:6379/0' \
  OBJECT_STORE_TYPE=s3 \
  OBJECT_STORE_ENDPOINT=https://objects.example.com \
  OBJECT_STORE_BUCKET=gateway-files \
  OBJECT_STORE_REGION=us-east-1 \
  OBJECT_STORE_ACCESS_KEY_ID=gateway-access-key \
  OBJECT_STORE_SECRET_ACCESS_KEY=gateway-secret-key \
  SESSION_COOKIE_SECURE=true \
  SESSION_COOKIE_TRUSTED_URL=https://gateway.example.com \
  TRUSTED_PROXIES=10.0.0.10/32 \
  BACKUP_DIR=/tmp/codex-gateway-backups \
  ROLLBACK_IMAGE=ghcr.io/example/codex-gateway@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb \
  bash scripts/release-preflight.sh --check
