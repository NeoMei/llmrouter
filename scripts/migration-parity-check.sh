#!/bin/bash
set -euo pipefail

# Migration parity evidence collector
# Usage (on staging host):
#   bash scripts/migration-parity-check.sh
# Required environment (sourced from next.env):
#   OLD_URL, NEW_URL, EVIDENCE_DIR, optionally MANAGEMENT_TOKEN

OLD_URL="${OLD_URL:-http://127.0.0.1:13000}"
NEW_URL="${NEW_URL:-http://127.0.0.1:13100}"
EVIDENCE="${EVIDENCE_DIR:-/opt/codex-gateway-next/evidence}"
mkdir -p "$EVIDENCE"

set -a
if [[ -f /opt/codex-gateway-next/next.env ]]; then
  source /opt/codex-gateway-next/next.env
fi
set +a

RELAY_TOKEN=$(docker exec -e PGPASSWORD=Postgres9803 postgres psql -U root -d codex_gateway_next -Atc "SELECT key FROM tokens WHERE status = 1 ORDER BY id LIMIT 1" 2>/dev/null || true)
if [[ -z "${RELAY_TOKEN:-}" ]]; then
  echo "WARNING: No usable relay token found in codex_gateway_next" >&2
fi

fetch() {
  local out=$1
  local method=${2:-GET}
  local url=$3
  shift 3 || true
  curl -sS -D "$out.headers" -o "$out.json" -X "$method" -w "%{http_code}" "$url" "$@" > "$out.status" || true
}

label_from_url() {
  echo "$1" | sed 's|http://||; s|https://||; s|/|-|g; s|:|-|g'
}

echo "=== Parity evidence collection: $(date -Iseconds) ===" > "$EVIDENCE/parity.log"

for svc in new-api codex-gateway-next; do
  docker inspect -f "{{.Name}} {{.Config.Image}} {{.Image}}" "$svc" >> "$EVIDENCE/container-info.txt" 2>/dev/null || true
done

for base in "$OLD_URL" "$NEW_URL"; do
  label=$(label_from_url "$base")
  fetch "$EVIDENCE/health-${label}" GET "$base/healthz"
  fetch "$EVIDENCE/status-${label}" GET "$base/api/status"
  fetch "$EVIDENCE/setup-${label}" GET "$base/api/setup"
  if [[ -n "${RELAY_TOKEN:-}" ]]; then
    fetch "$EVIDENCE/models-${label}" GET "$base/v1/models" -H "Authorization: Bearer ${RELAY_TOKEN}"
  fi
  if [[ -n "${MANAGEMENT_TOKEN:-}" ]]; then
    fetch "$EVIDENCE/readyz-${label}" GET "$base/readyz" -H "Authorization: Bearer ${MANAGEMENT_TOKEN}"
  fi

  health=$(jq -r '.status // . // "n/a"' < "$EVIDENCE/health-${label}.json" 2>/dev/null || echo n/a)
  echo "${label} health=${health} http-status=$(cat "$EVIDENCE/health-${label}.status" 2>/dev/null || echo n/a)" >> "$EVIDENCE/parity.log"
  echo "${label} status-http-status=$(cat "$EVIDENCE/status-${label}.status" 2>/dev/null || echo n/a)" >> "$EVIDENCE/parity.log"
  echo "${label} setup-http-status=$(cat "$EVIDENCE/setup-${label}.status" 2>/dev/null || echo n/a)" >> "$EVIDENCE/parity.log"
  if [[ -f "$EVIDENCE/models-${label}.status" ]]; then
    echo "${label} models-http-status=$(cat "$EVIDENCE/models-${label}.status") count=$(jq '.data | length' < "$EVIDENCE/models-${label}.json" 2>/dev/null || echo n/a)" >> "$EVIDENCE/parity.log"
  fi
  if [[ -f "$EVIDENCE/readyz-${label}.status" ]]; then
    echo "${label} readyz-http-status=$(cat "$EVIDENCE/readyz-${label}.status")" >> "$EVIDENCE/parity.log"
  fi

  # Security headers and frontend entrypoint
  curl -sS -D "$EVIDENCE/root-${label}.headers" -o "$EVIDENCE/root-${label}.html" "$base" || true
  echo "${label} root-cache-control=$(grep -i '^cache-control:' "$EVIDENCE/root-${label}.headers" | tr -d '\r' | cut -d' ' -f2- || true)" >> "$EVIDENCE/parity.log"
  echo "${label} root-x-frame-options=$(grep -i '^x-frame-options:' "$EVIDENCE/root-${label}.headers" | tr -d '\r' | cut -d' ' -f2- || true)" >> "$EVIDENCE/parity.log"
  echo "${label} root-content-security-policy=$(grep -i '^content-security-policy:' "$EVIDENCE/root-${label}.headers" | tr -d '\r' | cut -d' ' -f2- || true)" >> "$EVIDENCE/parity.log"

  # CORS untrusted origin
  out="$EVIDENCE/cors-${label}"
  curl -sS -D "$out.headers" -o "$out.json" -H "Origin: https://evil.example" "$base/v1/models" -H "Authorization: Bearer ${RELAY_TOKEN}" >/dev/null 2>&1 || true
  origin=$(grep -i "^access-control-allow-origin:" "$out.headers" 2>/dev/null | tr -d "\r" | cut -d' ' -f2- || true)
  echo "${label} cors-evil-origin: ${origin:-denied}" >> "$EVIDENCE/parity.log"

  # Management route protection with relay token
  if [[ -n "${RELAY_TOKEN:-}" ]]; then
    status=$(curl -sS -o /dev/null -w "%{http_code}" "$base/api/log/unknown/route-decision" -H "Authorization: Bearer ${RELAY_TOKEN}" 2>/dev/null || true)
    echo "${label} mgmt-reject-with-relay-status: ${status:-n/a}" >> "$EVIDENCE/parity.log"
  fi
done

# Database invariant counts before/after API tests
export PGPASSWORD=Postgres9803
for db in new-api codex_gateway_next; do
  echo "== $db invariant counts ==" >> "$EVIDENCE/parity.log"
  for tbl in users channels tokens abilities options; do
    cnt=$(docker exec -e PGPASSWORD=$PGPASSWORD postgres psql -U root -d "$db" -Atc "SELECT count(*) FROM $tbl" 2>/dev/null || echo ERR)
    echo "$tbl: $cnt" >> "$EVIDENCE/parity.log"
  done
done

# Diff key normalized responses
if [[ -f "$EVIDENCE/models-http127.0.0.1-13000.json" && -f "$EVIDENCE/models-http127.0.0.1-13100.json" ]]; then
  jq -S '.data | sort_by(.id)' "$EVIDENCE/models-http127.0.0.1-13000.json" > "$EVIDENCE/models-old.sorted.json"
  jq -S '.data | sort_by(.id)' "$EVIDENCE/models-http127.0.0.1-13100.json" > "$EVIDENCE/models-new.sorted.json"
  if diff -q "$EVIDENCE/models-old.sorted.json" "$EVIDENCE/models-new.sorted.json" >/dev/null; then
    echo "models-list: identical" >> "$EVIDENCE/parity.log"
  else
    echo "models-list: DIFFER" >> "$EVIDENCE/parity.log"
    diff "$EVIDENCE/models-old.sorted.json" "$EVIDENCE/models-new.sorted.json" > "$EVIDENCE/models-list.diff" || true
  fi
fi

# Synthetic provider routing check (unknown model, should fail before upstream billing)
if [[ -n "${RELAY_TOKEN:-}" ]]; then
  for base in "$OLD_URL" "$NEW_URL"; do
    label=$(label_from_url "$base")
    out="$EVIDENCE/relay-invalid-${label}"
    curl -sS -D "$out.headers" -o "$out.json" -X POST "$base/v1/chat/completions" \
      -H "Authorization: Bearer ${RELAY_TOKEN}" \
      -H "Content-Type: application/json" \
      -d '{"model":"__nonexistent_model_for_parity_test__","messages":[{"role":"user","content":"x"}]}' \
      -w "%{http_code}" > "$out.status" 2>/dev/null || true
    code=$(cat "$out.status" 2>/dev/null || echo n/a)
    echo "${label} relay-invalid-model-status: ${code}" >> "$EVIDENCE/parity.log"
  done
fi

echo "=== Parity evidence written to $EVIDENCE ===" >> "$EVIDENCE/parity.log"
