#!/usr/bin/env bash
set -euo pipefail

root_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
provider_manifest=${PROVIDER_CERTIFICATION_MANIFEST:-$root_dir/artifacts/release/provider-certification.json}
output_dir=${CERTIFICATION_OUTPUT_DIR:-$root_dir/artifacts/release}
commit=${RELEASE_COMMIT:-$(git -C "$root_dir" rev-parse HEAD)}
command -v jq >/dev/null 2>&1 || { echo "jq is required" >&2; exit 2; }
cd "$root_dir"
go test ./tests/contracts/...

public_501_matches=$(git grep -n -E 'StatusNotImplemented|http\.StatusNotImplemented' -- router controller ':!**/*_test.go' 2>/dev/null || true)
public_501_count=0
if [[ -n "$public_501_matches" ]]; then
  public_501_count=$(wc -l <<<"$public_501_matches" | tr -d ' ')
fi
supported_placeholder_count=0
if [[ -s "$provider_manifest" ]]; then
  supported_placeholder_count=$(jq '[.results[] | select(.status != "pass" or .http_status == 501)] | length' "$provider_manifest")
else
  supported_placeholder_count=1
fi
generated_docs_clean=true
if ! git diff --quiet -- docs/openapi; then
  generated_docs_clean=false
fi
mkdir -p "$output_dir"
jq -n --arg commit "$commit" --argjson public_501_count "$public_501_count" \
  --argjson supported_placeholder_count "$supported_placeholder_count" --argjson generated_docs_clean "$generated_docs_clean" \
  '{commit:$commit,public_501_count:$public_501_count,supported_placeholder_count:$supported_placeholder_count,generated_docs_clean:$generated_docs_clean}' \
  > "$output_dir/source.json"

if ((public_501_count > 0 || supported_placeholder_count > 0)) || [[ "$generated_docs_clean" != true ]]; then
  echo "source certification failed: public_501=$public_501_count supported_placeholder=$supported_placeholder_count generated_docs_clean=$generated_docs_clean" >&2
  exit 1
fi
echo "source certification: passed"
