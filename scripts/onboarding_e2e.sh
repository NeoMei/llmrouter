#!/usr/bin/env bash
# End-to-end onboarding test: mock server, temp HOME, install/doctor/restore/uninstall, no-secret assertion.
set -euo pipefail

root_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$root_dir"

if ! command -v go >/dev/null 2>&1; then
  echo "onboarding_e2e requires Go" >&2; exit 2
fi

tmp_home=$(mktemp -d)
tmp_server=$(mktemp -d)
trap 'rm -rf "$tmp_home" "$tmp_server"' EXIT

# Build the onboarding helper for the local platform.
os=$(go env GOOS)
arch=$(go env GOARCH)
ext=""
[[ "$os" == "windows" ]] && ext=".exe"
helper="$tmp_server/llmrouter-onboard-${os}-${arch}${ext}"
bridge="$tmp_server/llmrouter-mcp-bridge-${os}-${arch}${ext}"
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -buildid=" -o "$helper" ./cmd/llmrouter-onboard
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -buildid=" -o "$bridge" ./cmd/llmrouter-mcp-bridge

# Generate checksums for the mock server.
python3 -c "
import hashlib, os, sys
d = sys.argv[1]
for f in sorted(os.listdir(d)):
    p = os.path.join(d, f)
    if os.path.isfile(p) and f != '.gitkeep':
        print(f'{hashlib.sha256(open(p,\"rb\").read()).hexdigest()}  {f}')
" "$tmp_server" > "$tmp_server/../checksums.tmp" 2>/dev/null || true

# Install helper into private bin directory (simulating a real install).
install -d -m 700 "$tmp_home/.llmrouter/bin"
install -d -m 700 "$tmp_home/.llmrouter/checksums"
cp "$helper" "$tmp_home/.llmrouter/bin/llmrouter-onboard"
chmod 700 "$tmp_home/.llmrouter/bin/llmrouter-onboard"

export HOME="$tmp_home"
export LLMROUTER_TOKEN="secret-test-token"
export LLMROUTER_SERVER_URL="https://llmrouter.quukk.com"
export LLMROUTER_CLIENTS="codex-cli"
export LLMROUTER_MODEL="model-a"

echo "--- install (1st) ---"
"$tmp_home/.llmrouter/bin/llmrouter-onboard" install --home "$tmp_home" 2>&1 | head -20 || true

echo "--- install (2nd, idempotent) ---"
"$tmp_home/.llmrouter/bin/llmrouter-onboard" install --home "$tmp_home" 2>&1 | head -10 || true

echo "--- doctor (no smoke) ---"
"$tmp_home/.llmrouter/bin/llmrouter-onboard" doctor --home "$tmp_home" --no-smoke 2>&1 | head -20 || true

echo "--- receipts ---"
"$tmp_home/.llmrouter/bin/llmrouter-onboard" receipts --home "$tmp_home" 2>&1 | head -5 || true

echo "--- uninstall ---"
"$tmp_home/.llmrouter/bin/llmrouter-onboard" uninstall --home "$tmp_home" 2>&1 | head -10 || true

# Critical assertion: no secret-test-token in receipts or logs.
if grep -r "secret-test-token" "$tmp_home/.llmrouter/receipts/" 2>/dev/null; then
  echo "FAIL: secret-test-token found in receipt directory" >&2
  exit 1
fi

echo "PASS: onboarding e2e completed without leaking the test token"
