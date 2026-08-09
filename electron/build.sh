#!/usr/bin/env bash
set -euo pipefail

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
root_dir=$(cd "$script_dir/.." && pwd)
version=${VERSION:-$(git -C "$root_dir" describe --tags --always --dirty 2>/dev/null || echo dev)}
package_version=${version#v}
if [[ ! "$package_version" =~ ^[0-9]+\.[0-9]+\.[0-9]+([+-][0-9A-Za-z.-]+)?$ ]]; then
  package_version=0.0.0-dev
fi

cd "$root_dir/web"
bun_bin=$(command -v bun || true)
if [[ -z "$bun_bin" && -x "${HOME:-}/.bun/bin/bun" ]]; then
  bun_bin="${HOME}/.bun/bin/bun"
fi
if [[ -n "$bun_bin" ]]; then
  "$bun_bin" install --frozen-lockfile
  VITE_REACT_APP_VERSION="$version" "$bun_bin" run build
else
  npm install --ignore-scripts
  VITE_REACT_APP_VERSION="$version" npm run build
fi

cd "$root_dir"
binary_name=new-api
package_script=package:linux
case "$(uname -s)" in
  Darwin)
    package_script=package:mac
    ;;
  MINGW*|MSYS*|CYGWIN*)
    binary_name=new-api.exe
    package_script=package:win
    ;;
esac

CGO_ENABLED=${CGO_ENABLED:-1} go build \
  -trimpath \
  -ldflags "-s -w -X github.com/QuantumNous/new-api/common.Version=$version" \
  -o "$binary_name" \
  .

cd "$script_dir"
env -u npm_config_allow_scripts -u NPM_CONFIG_ALLOW_SCRIPTS \
  npm --userconfig=/dev/null ci
npm test
CSC_IDENTITY_AUTO_DISCOVERY=false npm run "$package_script" -- \
  --config.extraMetadata.version="$package_version"
