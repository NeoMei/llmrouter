#!/usr/bin/env bash
# Reproducibly cross-build onboarding helper binaries and generate deterministic checksums.
set -euo pipefail

root_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$root_dir"

bin_dir="web/public/install/bin"
checksums_file="web/public/install/checksums.txt"

commands=("llmrouter-onboard" "llmrouter-mcp-bridge")
platforms=(
  "linux/amd64"
  "linux/arm64"
  "darwin/amd64"
  "darwin/arm64"
  "windows/amd64"
  "windows/arm64"
)

ldflags="-s -w -buildid="

# --- --check mode: verify every expected binary exists and checksums match ---
if [[ "${1:-}" == "--check" ]]; then
  missing=0
  for cmd in "${commands[@]}"; do
    for pf in "${platforms[@]}"; do
      os="${pf%%/*}"
      arch="${pf##*/}"
      ext=""
      [[ "$os" == "windows" ]] && ext=".exe"
      artifact="$bin_dir/${cmd}-${os}-${arch}${ext}"
      if [[ ! -f "$artifact" ]]; then
        echo "MISSING: $artifact" >&2
        missing=$((missing + 1))
      fi
    done
  done
  if [[ $missing -gt 0 ]]; then
    echo "FAIL: $missing missing onboarding binaries" >&2
    exit 1
  fi
  if [[ -f "$checksums_file" ]]; then
    (cd "$bin_dir" && sha256sum -c < <(sed 's|^\([^ ]*\)  \*\(.*\)$|\1  \2|' "../$checksums_file" 2>/dev/null || true)) || {
      # Fallback for macOS where sha256sum may not exist
      (cd "$bin_dir" && while IFS= read -r line; do
        expected=$(echo "$line" | awk '{print $1}')
        fname=$(echo "$line" | awk '{print $2}')
        [[ -z "$expected" || -z "$fname" ]] && continue
        actual=$(shasum -a 256 "$fname" 2>/dev/null | awk '{print $1}') || actual=$(python3 -c "import hashlib;print(hashlib.sha256(open('$fname','rb').read()).hexdigest())")
        if [[ "$expected" != "$actual" ]]; then
          echo "CHECKSUM MISMATCH: $fname" >&2
          exit 1
        fi
      done < "../$checksums_file")
    }
    echo "OK: all onboarding binaries present and checksums verified"
  else
    echo "OK: all onboarding binaries present (no checksums file to verify)"
  fi
  exit 0
fi

# --- Build mode: cross-compile all platforms ---
mkdir -p "$bin_dir"

for cmd in "${commands[@]}"; do
  for pf in "${platforms[@]}"; do
    os="${pf%%/*}"
    arch="${pf##*/}"
    ext=""
    [[ "$os" == "windows" ]] && ext=".exe"
    artifact="$bin_dir/${cmd}-${os}-${arch}${ext}"
    echo "Building $artifact..."
    GOOS="$os" GOARCH="$arch" CGO_ENABLED=0 go build -trimpath -ldflags "$ldflags" -o "$artifact" "./cmd/$cmd"
  done
done

# --- Generate deterministic checksums (sorted bytewise, same format everywhere) ---
python3 -c "
import hashlib, os, sys
bin_dir = sys.argv[1]
out_file = sys.argv[2]
entries = []
for fname in sorted(os.listdir(bin_dir)):
    fpath = os.path.join(bin_dir, fname)
    if not os.path.isfile(fpath):
        continue
    if fname == '.gitkeep':
        continue
    digest = hashlib.sha256(open(fpath, 'rb').read()).hexdigest()
    entries.append(f'{digest}  {fname}')
with open(out_file, 'w') as f:
    f.write('\n'.join(entries) + '\n')
print(f'Wrote {len(entries)} checksums to {out_file}')
" "$bin_dir" "$checksums_file"

echo "Done. ${#commands[@]} commands × ${#platforms[@]} platforms = $((${#commands[@]} * ${#platforms[@]})) binaries."
