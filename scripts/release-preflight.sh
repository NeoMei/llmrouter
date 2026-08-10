#!/usr/bin/env bash
set -euo pipefail

mode=${1:-}
if [[ "$mode" != "--check" && -n "$mode" ]]; then
  echo "usage: $0 [--check]" >&2
  exit 2
fi

required=(
  APP_IMAGE APP_IMAGE_DIGEST SESSION_SECRET GATEWAY_AES_KEY MANAGEMENT_TOKEN SQL_DSN
  REDIS_CONN_STRING OBJECT_STORE_TYPE OBJECT_STORE_ENDPOINT OBJECT_STORE_BUCKET
  OBJECT_STORE_REGION OBJECT_STORE_ACCESS_KEY_ID OBJECT_STORE_SECRET_ACCESS_KEY
  SESSION_COOKIE_SECURE SESSION_COOKIE_TRUSTED_URL TRUSTED_PROXIES BACKUP_DIR
  ROLLBACK_IMAGE
)

for name in "${required[@]}"; do
  if [[ -z "${!name:-}" ]]; then
    echo "$name is required" >&2
    exit 1
  fi
  if [[ "${!name}" == *replace-with* ]]; then
    echo "$name still contains an example placeholder" >&2
    exit 1
  fi
done

if [[ "${RUN_MODE:-}" != "production" ]]; then
  echo "RUN_MODE must be production" >&2
  exit 1
fi
if [[ ! "$APP_IMAGE_DIGEST" =~ ^sha256:[0-9a-f]{64}$ ]]; then
  echo "APP_IMAGE_DIGEST must be a sha256 digest" >&2
  exit 1
fi
if [[ ${#SESSION_SECRET} -lt 32 || ${#GATEWAY_AES_KEY} -lt 32 || ${#MANAGEMENT_TOKEN} -lt 32 ]]; then
  echo "SESSION_SECRET, GATEWAY_AES_KEY, and MANAGEMENT_TOKEN must contain at least 32 bytes" >&2
  exit 1
fi
if [[ "$OBJECT_STORE_TYPE" != "s3" || "$OBJECT_STORE_ENDPOINT" != https://* ]]; then
  echo "production object storage must be S3-compatible over HTTPS" >&2
  exit 1
fi
if [[ "$SESSION_COOKIE_SECURE" != "true" || "$SESSION_COOKIE_TRUSTED_URL" != https://* ]]; then
  echo "production cookies require secure mode and exact HTTPS origins" >&2
  exit 1
fi
if [[ "$ROLLBACK_IMAGE" != *@sha256:* ]]; then
  echo "ROLLBACK_IMAGE must include an immutable digest" >&2
  exit 1
fi

if [[ "$mode" == "--check" ]]; then
  echo "release preflight configuration: ok"
  exit 0
fi

command -v docker >/dev/null || { echo "docker is required" >&2; exit 1; }
docker compose config --quiet
mkdir -p "$BACKUP_DIR"
test -w "$BACKUP_DIR" || { echo "BACKUP_DIR is not writable" >&2; exit 1; }
echo "release preflight: ok"
