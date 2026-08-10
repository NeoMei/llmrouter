#!/usr/bin/env bash
set -euo pipefail

root_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$root_dir"

node <<'NODE'
const fs = require('node:fs')

for (const path of ['web/package.json', 'cli/package.json']) {
  const pkg = JSON.parse(fs.readFileSync(path, 'utf8'))
  if (typeof pkg.scripts?.test !== 'string' || pkg.scripts.test.length === 0) {
    throw new Error(`${path} must define a test script`)
  }
}
NODE

for script in \
  scripts/verify-backend.sh scripts/verify-web.sh scripts/verify-cli.sh \
  scripts/certify-providers.sh scripts/certify-restore.sh scripts/certify-source.sh \
  scripts/release-gate.sh scripts/release_gate_test.sh scripts/restore_test.sh; do
  test -x "$script" || {
    echo "$script must exist and be executable" >&2
    exit 1
  }
done

bash scripts/restore_test.sh
