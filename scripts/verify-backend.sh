#!/usr/bin/env bash
set -euo pipefail

root_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$root_dir"

unformatted=$(git ls-files -z -- '*.go' | xargs -0 gofmt -l)
if [[ -n "$unformatted" ]]; then
  echo "gofmt is required for:" >&2
  echo "$unformatted" >&2
  exit 1
fi

go test ./...
go vet ./...
go test -race ./...
go build -trimpath ./...

# Onboarding tests
go test ./internal/onboarding/... ./cmd/llmrouter-onboard/... -count=1
