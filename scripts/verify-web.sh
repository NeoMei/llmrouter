#!/usr/bin/env bash
set -euo pipefail

root_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$root_dir/web"

run_script() {
  if command -v bun >/dev/null 2>&1; then
    bun run "$@"
  else
    npm run "$@"
  fi
}

run_script format:check
run_script copyright:check
run_script lint
run_script build:check
run_script test
run_script build
run_script knip
