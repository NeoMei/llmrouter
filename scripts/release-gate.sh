#!/usr/bin/env bash
set -uo pipefail

root_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$root_dir"
evidence_dir="$root_dir/artifacts/release"
if [[ "${1:-}" == "--evidence-dir" && -n "${2:-}" && $# -eq 2 ]]; then
  evidence_dir=$2
elif [[ $# -gt 0 ]]; then
  echo "usage: $0 [--evidence-dir DIR]" >&2
  exit 2
fi

release_commit=${RELEASE_COMMIT:-$(git -C "$root_dir" rev-parse HEAD 2>/dev/null || true)}
blockers=()
add_blocker() { blockers+=("$1"); }

require_json() {
  local file=$1 label=$2
  if [[ ! -s "$file" ]]; then
    add_blocker "$label is missing: $file"
    return 1
  fi
  if ! jq -e . "$file" >/dev/null 2>&1; then
    add_blocker "$label is not valid JSON: $file"
    return 1
  fi
  return 0
}

check_commit() {
  local file=$1 label=$2 actual
  actual=$(jq -r '.commit // empty' "$file")
  if [[ -z "$release_commit" || "$actual" != "$release_commit" ]]; then
    add_blocker "$label commit mismatch: expected $release_commit, got ${actual:-missing}"
  fi
}

provider_manifest="$evidence_dir/provider-certification.json"
if require_json "$provider_manifest" "provider certification manifest"; then
  check_commit "$provider_manifest" "provider certification"
  if ! jq -e '.schema_version == 1 and (.registry_version | type == "string" and length > 0) and (.generated_at | type == "string" and length > 0) and (.results | length > 0)' "$provider_manifest" >/dev/null; then
    add_blocker "provider certification manifest is incomplete"
  fi
  failed_provider_count=$(jq '[.results[] | select(.status != "pass" or (.http_status < 200) or (.http_status >= 400) or .http_status == 501)] | length' "$provider_manifest" 2>/dev/null || echo 1)
  if [[ "$failed_provider_count" != 0 ]]; then
    add_blocker "provider certification contains $failed_provider_count failed, missing-credential, or public 501 result(s)"
  fi
  signature="$evidence_dir/provider-certification.sig"
  public_key=${PROVIDER_CERTIFICATION_PUBLIC_KEY:-}
  if [[ -z "$public_key" || ! -s "$public_key" ]]; then
    add_blocker "provider certification trusted public key is missing"
  elif [[ ! -s "$signature" ]]; then
    add_blocker "provider certification signature is missing"
  elif ! openssl pkeyutl -verify -pubin -inkey "$public_key" -rawin -in "$provider_manifest" -sigfile "$signature" >/dev/null 2>&1; then
    add_blocker "provider certification signature verification failed"
  fi
fi

image_evidence="$evidence_dir/image.json"
if require_json "$image_evidence" "image certification evidence"; then
  check_commit "$image_evidence" "image certification"
  if ! jq -e '.digest | test("^sha256:[0-9a-f]{64}$")' "$image_evidence" >/dev/null ||
    ! jq -e '.signed == true and .sbom == true and .provenance == true and .container_smoke == true' "$image_evidence" >/dev/null; then
    add_blocker "image evidence requires immutable digest, signature, SBOM, provenance, and container smoke pass"
  fi
fi

restore_evidence="$evidence_dir/restore-rehearsal.json"
if require_json "$restore_evidence" "restore rehearsal evidence"; then
  check_commit "$restore_evidence" "restore rehearsal"
  if ! jq -e '.status == "pass" and (.tested_at | type == "string" and length > 0) and (.backup_checksum | test("^[0-9a-f]{64}$")) and (.rto_seconds | type == "number" and . > 0)' "$restore_evidence" >/dev/null; then
    add_blocker "restore rehearsal evidence is incomplete or failed"
  fi
fi

desktop_evidence="$evidence_dir/desktop.json"
if require_json "$desktop_evidence" "desktop certification evidence"; then
  check_commit "$desktop_evidence" "desktop certification"
  candidate_dir=${RELEASE_CANDIDATE_DIR:-}
  for platform in linux macos windows; do
    if ! jq -e --arg platform "$platform" '.platforms[$platform].startup_probe == "pass" and (.platforms[$platform].checksum | test("^[0-9a-f]{64}$"))' "$desktop_evidence" >/dev/null; then
      add_blocker "desktop certification missing checksum or startup probe for $platform"
      continue
    fi
    if [[ -z "$candidate_dir" || ! -d "$candidate_dir" ]]; then
      add_blocker "release candidate directory is missing for desktop artifact verification"
      break
    fi

    backend_artifact=$(jq -r --arg platform "$platform" '.platforms[$platform].backend_artifact // empty' "$desktop_evidence")
    backend_checksum=$(jq -r --arg platform "$platform" '.platforms[$platform].backend_checksum // empty' "$desktop_evidence")
    checksum_file=$(jq -r --arg platform "$platform" '.platforms[$platform].electron_checksum_file // empty' "$desktop_evidence")
    checksum_digest=$(jq -r --arg platform "$platform" '.platforms[$platform].checksum // empty' "$desktop_evidence")
    if [[ -z "$backend_artifact" || "$backend_artifact" == */* || ! "$backend_checksum" =~ ^[0-9a-f]{64}$ || ! -f "$candidate_dir/$backend_artifact" ]]; then
      add_blocker "desktop backend artifact evidence is invalid for $platform"
    elif [[ "$(shasum -a 256 "$candidate_dir/$backend_artifact" | awk '{print $1}')" != "$backend_checksum" ]]; then
      add_blocker "desktop backend checksum mismatch for $platform"
    fi
    if [[ -z "$checksum_file" || "$checksum_file" == */* || ! -f "$candidate_dir/$checksum_file" ]]; then
      add_blocker "desktop package checksum manifest is missing for $platform"
      continue
    fi
    if [[ "$(shasum -a 256 "$candidate_dir/$checksum_file" | awk '{print $1}')" != "$checksum_digest" ]]; then
      add_blocker "desktop package checksum manifest mismatch for $platform"
      continue
    fi
    manifest_entries=0
    while read -r artifact_digest artifact_name extra; do
      artifact_name=${artifact_name#\*}
      if [[ ! "$artifact_digest" =~ ^[0-9a-f]{64}$ || -n "${extra:-}" || -z "$artifact_name" || "$artifact_name" == */* || ! -f "$candidate_dir/electron/dist/$artifact_name" ]]; then
        add_blocker "desktop package checksum entry is invalid for $platform"
        continue
      fi
      manifest_entries=$((manifest_entries + 1))
      if [[ "$(shasum -a 256 "$candidate_dir/electron/dist/$artifact_name" | awk '{print $1}')" != "$artifact_digest" ]]; then
        add_blocker "desktop package checksum mismatch for $platform: $artifact_name"
      fi
    done < "$candidate_dir/$checksum_file"
    if ((manifest_entries == 0)); then
      add_blocker "desktop package checksum manifest is empty for $platform"
    fi
  done
fi

nightly_evidence="$evidence_dir/nightly.json"
if require_json "$nightly_evidence" "nightly certification evidence"; then
  check_commit "$nightly_evidence" "nightly certification"
  if ! jq -e '.status == "pass" and .fault_recovery == "pass" and .performance == "pass" and .billing == "pass"' "$nightly_evidence" >/dev/null; then
    add_blocker "nightly evidence requires fault recovery, performance, and billing passes"
  fi
fi

source_evidence="$evidence_dir/source.json"
if require_json "$source_evidence" "source certification evidence"; then
  check_commit "$source_evidence" "source certification"
  if ! jq -e '.public_501_count == 0 and .supported_placeholder_count == 0 and .generated_docs_clean == true' "$source_evidence" >/dev/null; then
    add_blocker "source certification found a public 501, supported placeholder adapter, or dirty generated docs"
  fi
fi

run_gate() {
  local label=$1
  shift
  local log_dir="$evidence_dir/logs"
  mkdir -p "$log_dir"
  local slug=${label// /-}
  if ! "$@" >"$log_dir/$slug.log" 2>&1; then
    add_blocker "$label failed; see $log_dir/$slug.log"
  fi
}

if [[ "${RELEASE_GATE_FAST:-0}" != 1 ]]; then
  command -v jq >/dev/null 2>&1 || add_blocker "jq is required"
  command -v openssl >/dev/null 2>&1 || add_blocker "openssl is required"
  command -v docker >/dev/null 2>&1 || add_blocker "Docker with Compose is required for container, failover, and restore certification"
  if [[ -n "$(git -C "$root_dir" status --porcelain --untracked-files=no)" ]]; then
    add_blocker "tracked working tree is dirty"
  fi
  run_gate "release configuration contract" bash "$root_dir/scripts/release_config_test.sh"
  run_gate "release script contract" bash "$root_dir/scripts/release_scripts_test.sh"
  run_gate "provider and route contracts" go test ./tests/contracts/...
  run_gate "backend verification" bash "$root_dir/scripts/verify-backend.sh"
  run_gate "web verification" bash "$root_dir/scripts/verify-web.sh"
  run_gate "CLI verification" bash "$root_dir/scripts/verify-cli.sh"
  run_gate "browser acceptance" bash -c "cd '$root_dir/web' && npm run e2e"
  if command -v docker >/dev/null 2>&1; then
    run_gate "nightly integration matrix" bash "$root_dir/scripts/run-integration-matrix.sh" --nightly
  fi
fi

if ((${#blockers[@]} > 0)); then
  echo "release gate: blocked (${#blockers[@]} item(s))" >&2
  for blocker in "${blockers[@]}"; do
    echo "- $blocker" >&2
  done
  exit 1
fi

echo "release gate: passed for $release_commit"
