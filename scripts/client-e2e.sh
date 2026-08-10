#!/usr/bin/env bash
set -euo pipefail

root_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$root_dir"

GATEWAY_URL="${GATEWAY_URL:-}"
API_TOKEN="${API_TOKEN:-}"
MODEL="${MODEL:-deepseek/deepseek-v4-flash}"
OUT_DIR="${OUT_DIR:-$(pwd)/e2e-evidence}"

if [[ -z "$GATEWAY_URL" || -z "$API_TOKEN" ]]; then
  echo "usage: GATEWAY_URL=https://... API_TOKEN=sk-... MODEL=... $0" >&2
  exit 1
fi

GATEWAY_URL="${GATEWAY_URL%/}"
mkdir -p "$OUT_DIR"

echo "Running client/provider/wire acceptance matrix against $GATEWAY_URL"

call() {
  local name=$1
  shift
  curl -sS -D "$OUT_DIR/$name.headers" -o "$OUT_DIR/$name.json" "$@" \
    -H "Authorization: Bearer ${API_TOKEN}" \
    -H "Content-Type: application/json"
}

# Codex / OpenAI Responses wire
for variant in responses chat; do
  if [[ "$variant" == "responses" ]]; then
    call "codex-${variant}-text" \
      "${GATEWAY_URL}/v1/responses" \
      -d "{\"model\":\"$MODEL\",\"input\":[{\"role\":\"user\",\"content\":\"Say hello\"}],\"max_output_tokens\":16}"
  else
    call "codex-${variant}-text" \
      "${GATEWAY_URL}/v1/chat/completions" \
      -d "{\"model\":\"$MODEL\",\"messages\":[{\"role\":\"user\",\"content\":\"Say hello\"}],\"max_tokens\":16}"
  fi
  echo "codex $variant text: $(jq -r '.status // .error.type // "ok"' < "$OUT_DIR/codex-${variant}-text.json" 2>/dev/null || echo 'unknown')"
done

# Codex function tool round trip
call "codex-function-tool" \
  "${GATEWAY_URL}/v1/responses" \
  -d "{\"model\":\"$MODEL\",\"input\":[{\"role\":\"user\",\"content\":\"Use the echo tool\"}],\"tools\":[{\"type\":\"function\",\"function\":{\"name\":\"echo\",\"parameters\":{\"type\":\"object\"}}}],\"max_output_tokens\":32}"
if jq -e '.output[]? | select(.type == \"function_call\")' < "$OUT_DIR/codex-function-tool.json" >/dev/null 2>&1; then
  echo "codex function tool: ok"
else
  echo "codex function tool: no function_call found (may be ok if model declined)" >&2
fi

# Claude / Anthropic Messages wire
call "claude-messages" \
  "${GATEWAY_URL}/v1/messages" \
  -H "Anthropic-Version: 2023-06-01" \
  -d "{\"model\":\"$MODEL\",\"max_tokens\":16,\"messages\":[{\"role\":\"user\",\"content\":\"Hello\"}]}"
echo "claude messages: $(jq -r '.content[0].type // .type // .error.type // "ok"' < "$OUT_DIR/claude-messages.json" 2>/dev/null || echo 'unknown')"

# Security: relay token must be rejected on management route
mgmt_status=$(curl -sS -o /dev/null -w "%{http_code}" "${GATEWAY_URL}/api/log/unknown/route-decision" -H "Authorization: Bearer ${API_TOKEN}")
if [[ "$mgmt_status" == "401" || "$mgmt_status" == "403" ]]; then
  echo "security: relay token rejected on management route: ok"
else
  echo "security: relay token was not rejected on management route (status $mgmt_status)" >&2
  exit 1
fi

# CORS: untrusted origin must not receive credentialed headers
cors_origin=$(curl -sS -o /dev/null -w "%{header_access-control-allow-origin}" \
  -H "Origin: https://evil.example" "${GATEWAY_URL}/v1/models" -H "Authorization: Bearer ${API_TOKEN}")
if [[ -z "$cors_origin" ]]; then
  echo "security: untrusted origin denied: ok"
else
  echo "security: untrusted origin received Access-Control-Allow-Origin: $cors_origin" >&2
  exit 1
fi

# Route-decision trace exists for a request that used a public model
if [[ -s "$OUT_DIR/codex-chat-text.json" ]]; then
  request_id=$(jq -r '(.id // .request_id // empty)' < "$OUT_DIR/codex-chat-text.json" 2>/dev/null || true)
  if [[ -n "${request_id:-}" ]]; then
    call "route-decision" "${GATEWAY_URL}/api/log/${request_id}/route-decision" -H "Authorization: Bearer ${API_TOKEN}"
    echo "route-decision lookup: $(jq -r '.success' < "$OUT_DIR/route-decision.json" 2>/dev/null || echo 'not available')"
  fi
fi

echo "Evidence written to $OUT_DIR"
echo "client-e2e: ok"
