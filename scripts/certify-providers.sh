#!/usr/bin/env bash
set -euo pipefail

root_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
: "${CERTIFICATION_GATEWAY_URL:?CERTIFICATION_GATEWAY_URL is required}"
: "${CERTIFICATION_CASES_FILE:?CERTIFICATION_CASES_FILE is required}"
: "${CERTIFICATION_SIGNING_KEY:?CERTIFICATION_SIGNING_KEY is required}"
CERTIFICATION_BUILD_VERSION=${CERTIFICATION_BUILD_VERSION:-${RELEASE_COMMIT:-}}
: "${CERTIFICATION_BUILD_VERSION:?CERTIFICATION_BUILD_VERSION or RELEASE_COMMIT is required}"
output_dir=${CERTIFICATION_OUTPUT_DIR:-$root_dir/artifacts/release}
commit=${RELEASE_COMMIT:-$(git -C "$root_dir" rev-parse HEAD)}

for command_name in curl jq openssl; do
  command -v "$command_name" >/dev/null 2>&1 || { echo "$command_name is required" >&2; exit 2; }
done
jq -e '.cases | type == "array"' "$CERTIFICATION_CASES_FILE" >/dev/null
[[ -s "$CERTIFICATION_SIGNING_KEY" ]] || { echo "certification signing key is missing" >&2; exit 2; }
mkdir -p "$output_dir"
tmp_dir=$(mktemp -d)
trap 'rm -rf "$tmp_dir"' EXIT

base_url=${CERTIFICATION_GATEWAY_URL%/}
capability_file="$tmp_dir/capabilities.json"
capability_headers="$tmp_dir/capabilities.headers"
curl --fail --silent --show-error --max-time 15 --dump-header "$capability_headers" "$base_url/api/status" >/dev/null
reported_version=$(awk 'BEGIN{IGNORECASE=1} /^X-New-Api-Version:/ {gsub("\r", "", $2); print $2}' "$capability_headers" | tail -1)
if [[ "$reported_version" != "$CERTIFICATION_BUILD_VERSION" ]]; then
  echo "certification gateway version mismatch: expected $CERTIFICATION_BUILD_VERSION, got ${reported_version:-missing}" >&2
  exit 1
fi
(
  cd "$root_dir"
  go run ./cmd/capability-manifest
) > "$capability_file"
registry_version=$(jq -r '.version // empty' "$capability_file")
[[ -n "$registry_version" ]] || { echo "gateway did not return a capability registry version" >&2; exit 1; }
jq -c '.capabilities[] | select(.supported == true)' "$capability_file" > "$tmp_dir/supported.jsonl"
[[ -s "$tmp_dir/supported.jsonl" ]] || { echo "gateway reported no supported provider capabilities" >&2; exit 1; }

failures=0
while IFS= read -r capability; do
  channel_type=$(jq -r '.channel_type' <<<"$capability")
  channel_name=$(jq -r '.channel_name' <<<"$capability")
  protocol=$(jq -r '.protocol' <<<"$capability")
  operation=$(jq -r '.operation' <<<"$capability")
  certification_case=$(jq -c --argjson channel_type "$channel_type" --arg protocol "$protocol" --arg operation "$operation" '
    [.cases[] | select(.channel_type == $channel_type and .protocol == $protocol and .operation == $operation)] | first // empty
  ' "$CERTIFICATION_CASES_FILE")

  status=missing_credential
  http_status=0
  reason="no live certification case"
  if [[ -n "$certification_case" ]]; then
    token_env=$(jq -r '.token_env // empty' <<<"$certification_case")
    token=""
    if [[ -n "${CERTIFICATION_TOKENS_FILE:-}" && -s "${CERTIFICATION_TOKENS_FILE:-}" && -n "$token_env" ]]; then
      token=$(jq -r --arg key "$token_env" '.[$key] // empty' "$CERTIFICATION_TOKENS_FILE")
    elif [[ -n "$token_env" ]]; then
      token=${!token_env:-}
    fi
    if [[ -z "$token_env" || -z "$token" ]]; then
      reason="credential reference ${token_env:-missing} is not set"
    elif [[ ! "$token" =~ ^[A-Za-z0-9._:-]+$ ]]; then
      status=fail
      reason="credential contains unsupported characters"
    else
      method=$(jq -r '.method // "POST" | ascii_upcase' <<<"$certification_case")
      request_path=$(jq -r '.path // empty' <<<"$certification_case")
      if [[ ! "$method" =~ ^(GET|POST|PATCH|DELETE)$ ]]; then
        status=fail
        reason="certification method is invalid"
      elif [[ "$request_path" != /* || "$request_path" == *..* ]]; then
        status=fail
        reason="certification path is invalid"
      else
        response_file="$tmp_dir/response.json"
        body=$(jq -c '.body // {}' <<<"$certification_case")
        auth_config="$tmp_dir/curl-auth.conf"
        (umask 077; printf 'header = "Authorization: Bearer %s"\n' "$token" > "$auth_config")
        curl_args=(--config "$auth_config" --silent --show-error --max-time 120 --request "$method" --header 'Content-Type: application/json' --output "$response_file" --write-out '%{http_code}')
        if [[ "$method" != GET && "$method" != DELETE ]]; then
          curl_args+=(--data "$body")
        fi
        set +e
        http_status=$(curl "${curl_args[@]}" "$base_url$request_path")
        curl_status=$?
        set -e
        if [[ "$curl_status" -ne 0 ]]; then
          status=fail
          reason="request transport failed"
        elif [[ "$http_status" == 501 ]]; then
          status=fail
          reason="public route returned 501"
        elif [[ "$http_status" -lt 200 || "$http_status" -ge 400 ]]; then
          status=fail
          reason="unexpected HTTP status"
        else
          assertion=$(jq -r '.response_jq // empty' <<<"$certification_case")
          if [[ -n "$assertion" ]] && ! jq -e "$assertion" "$response_file" >/dev/null 2>&1; then
            status=fail
            reason="response assertion failed"
          else
            status=pass
            reason=""
          fi
        fi
      fi
    fi
  fi
  [[ "$status" == pass ]] || failures=$((failures + 1))
  http_status_number=$((10#${http_status:-0}))
  jq -nc \
    --argjson channel_type "$channel_type" --arg channel_name "$channel_name" \
    --arg protocol "$protocol" --arg operation "$operation" --arg status "$status" \
    --argjson http_status "$http_status_number" --arg reason "$reason" \
    '{channel_type:$channel_type,channel_name:$channel_name,protocol:$protocol,operation:$operation,status:$status,http_status:$http_status} + (if $reason == "" then {} else {reason:$reason} end)' \
    >> "$tmp_dir/results.jsonl"
done < "$tmp_dir/supported.jsonl"

manifest="$output_dir/provider-certification.json"
jq -s --arg commit "$commit" --arg registry_version "$registry_version" --arg generated_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" '
  {schema_version:1,commit:$commit,registry_version:$registry_version,generated_at:$generated_at,results:.}
' "$tmp_dir/results.jsonl" > "$manifest"
openssl pkeyutl -sign -rawin -inkey "$CERTIFICATION_SIGNING_KEY" -in "$manifest" -out "$output_dir/provider-certification.sig"
openssl pkey -in "$CERTIFICATION_SIGNING_KEY" -pubout -out "$output_dir/provider-certification.pub" >/dev/null 2>&1

if ((failures > 0)); then
  echo "provider certification incomplete: $failures supported capability result(s) failed or lack live credentials" >&2
  exit 1
fi
echo "provider certification: passed ($(wc -l < "$tmp_dir/results.jsonl" | tr -d ' ') results)"
