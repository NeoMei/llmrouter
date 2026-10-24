#!/usr/bin/env bash
# Explicit, isolated actual-consumer acceptance. No clone or installation setup.
set -euo pipefail
if [[ $# != 2 ]]; then
  echo "usage: $0 /absolute/isolated-pinned-consumer-checkout /absolute/new-evidence-directory" >&2
  exit 2
fi
consumer=$1
evidence=$2
pin=f5adb9eac166e7d3da7dceed7aa50049d4c4db1f
repo=$(cd "$(dirname "$0")/.." && pwd -P)
[[ "$consumer" = /* && "$evidence" = /* ]] || { echo 'absolute paths required' >&2; exit 2; }
consumer=$(cd "$consumer" && pwd -P)
[[ "$consumer" != "$repo" ]] || { echo 'an isolated external consumer checkout is required' >&2; exit 2; }
[[ ! -e "$evidence" ]] || { echo 'evidence directory must be new (failed runs are retained)' >&2; exit 2; }
mkdir -m 700 -p "$evidence"
run() {
  local label=$1; shift
  printf 'cwd=%q\n' "$PWD" > "$evidence/$label.command"
  printf '%q ' "$@" >> "$evidence/$label.command"
  printf '\n' >> "$evidence/$label.command"
  local status=0
  "$@" > "$evidence/$label.stdout" 2> "$evidence/$label.stderr" || status=$?
  printf '%s\n' "$status" > "$evidence/$label.exit"
  if [[ $status != 0 ]]; then
    cat "$evidence/$label.stdout" "$evidence/$label.stderr" >&2
    return "$status"
  fi
}
cd "$consumer"
run consumer-pin git rev-parse HEAD
[[ $(cat "$evidence/consumer-pin.stdout") = "$pin" ]] || { echo 'consumer pin mismatch' >&2; exit 1; }
# HEAD comparison covers staged AND unstaged changes across EVERY tracked path.
run consumer-tracked-before git diff --exit-code HEAD --
run consumer-status-before git status --porcelain --untracked-files=no
[[ ! -s "$evidence/consumer-status-before.stdout" ]] || { echo 'tracked consumer changes' >&2; exit 1; }
run consumer-tracked-manifest git ls-files --stage
run consumer-root git rev-parse --show-toplevel
[[ $(cat "$evidence/consumer-root.stdout") = "$consumer" ]] || { echo 'consumer must be the checkout root' >&2; exit 1; }
run runner-tracked git -C "$repo" ls-files --error-unmatch scripts/testdata/knowledge-consumer/main.go
run runner-clean git -C "$repo" diff --exit-code HEAD -- scripts/testdata/knowledge-consumer/main.go
run runner-source shasum -a 256 "$repo/scripts/testdata/knowledge-consumer/main.go"
# A new command path cannot overwrite another worker's fixture or consumer code.
command_dir=$(mktemp -d "$consumer/cmd/llmrouter-certification-XXXXXXXX")
trap 'rm -f "$command_dir/main.go"; rmdir "$command_dir"' EXIT
cp "$repo/scripts/testdata/knowledge-consumer/main.go" "$command_dir/main.go"
run copied-runner-source shasum -a 256 "$command_dir/main.go"
run consumer-build env GOTOOLCHAIN=go1.26.10 CGO_ENABLED=1 go build -buildvcs=true -tags sqlite_fts5 -o "$evidence/knowledge-consumer" "./cmd/$(basename "$command_dir")"
run consumer-tracked-after git diff --exit-code HEAD --
run consumer-status-after git status --porcelain --untracked-files=no
[[ ! -s "$evidence/consumer-status-after.stdout" ]] || { echo 'consumer build changed tracked source/dependencies' >&2; exit 1; }
run consumer-build-provenance env GOTOOLCHAIN=go1.26.10 go version -m "$evidence/knowledge-consumer"
run consumer-binary-sha shasum -a 256 "$evidence/knowledge-consumer"
binary_sha=$(cut -d ' ' -f 1 "$evidence/consumer-binary-sha.stdout")
cd "$repo"
run gateway-source git rev-parse HEAD
run gateway-diff git diff -- router/knowledge_consumer_integration_test.go scripts/certify-knowledge-consumer.sh
run gateway-test-source shasum -a 256 router/knowledge_consumer_integration_test.go scripts/certify-knowledge-consumer.sh
cp router/knowledge_consumer_integration_test.go "$evidence/gateway-test-source.go"
cp scripts/certify-knowledge-consumer.sh "$evidence/certification-script.sh"
cp scripts/testdata/knowledge-consumer/main.go "$evidence/consumer-runner.go"
run gateway-chain env GOTOOLCHAIN=go1.26.10 LLMROUTER_KNOWLEDGE_CONSUMER_BINARY="$evidence/knowledge-consumer" LLMROUTER_KNOWLEDGE_CONSUMER_SHA256="$binary_sha" LLMROUTER_KNOWLEDGE_EVIDENCE="$evidence" go test ./router -run '^TestKnowledgePinnedConsumerIntegration$' -count=1 -timeout=240s -v
cat "$evidence/gateway-chain.stdout"
