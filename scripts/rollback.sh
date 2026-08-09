#!/usr/bin/env bash
set -euo pipefail

: "${ROLLBACK_IMAGE:?ROLLBACK_IMAGE is required and must include a digest}"
if [[ "$ROLLBACK_IMAGE" != *@sha256:* ]]; then
  echo "ROLLBACK_IMAGE must include an immutable digest" >&2
  exit 1
fi
if [[ "${1:-}" == "--check" ]]; then
  echo "rollback configuration: ok"
  exit 0
fi
if [[ "${1:-}" != "--confirm" ]]; then
  echo "rollback changes the deployed revision; pass --confirm" >&2
  exit 2
fi

command -v docker >/dev/null || { echo "docker is required" >&2; exit 1; }
export APP_IMAGE=${ROLLBACK_IMAGE%@sha256:*}
export APP_IMAGE_DIGEST=sha256:${ROLLBACK_IMAGE#*@sha256:}
docker compose pull gateway
docker compose up -d --no-deps gateway
docker compose exec -T gateway wget -q -O - http://127.0.0.1:3000/readyz | grep -q '"status":"ok"'
