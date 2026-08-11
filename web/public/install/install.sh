#!/usr/bin/env bash
# Secure same-origin bootstrap for the LLMAPI onboarding helpers.
set -eu
umask 077

fail() {
  printf '%s\n' "LLMAPI installer: $*" >&2
  exit 1
}

require_origin() {
  origin=${LLMAPI_SERVER_URL-}
  [ -n "$origin" ] || fail 'LLMAPI_SERVER_URL is required'
  origin=${origin%/}
  case "$origin" in
    https://*) scheme=https ; hostport=${origin#https://} ;;
    http://*) scheme=http ; hostport=${origin#http://} ;;
    *) fail 'server URL must use HTTPS or loopback HTTP' ;;
  esac
  [ -n "$hostport" ] || fail 'server URL has no host'
  case "$hostport" in *[/?#@]*|*[![:print:]]*) fail 'server URL must be an origin' ;; esac
  if [ "$scheme" = http ]; then
    case "$hostport" in
      localhost|127.0.0.1|'[::1]') ;;
      localhost:[0-9]*|127.0.0.1:[0-9]*|'[::1]':[0-9]*)
        port=${hostport##*:}
        case "$port" in *[!0-9]*|'') fail 'loopback HTTP port is invalid' ;; esac
        ;;
      *) fail 'HTTP is allowed only for loopback servers' ;;
    esac
  fi
  printf '%s\n' "$origin"
}

checksum_for() {
  target=$1
  awk -v target="$target" '
    function invalid() { bad = 1 }
    {
      if (length($0) < 67 || substr($0, 65, 2) != "  ") { invalid(); next }
      digest = substr($0, 1, 64)
      name = substr($0, 67)
      if (length(digest) != 64 || digest !~ /^[0-9a-fA-F]+$/ ||
          name == "" || name !~ /^[A-Za-z0-9._-]+$/ || index(name, "..") != 0) { invalid(); next }
      if (seenLine[$0]++ || seenName[name]++) { invalid(); next }
      if (name == target) { selected = tolower(digest); selectedCount++ }
    }
    END {
      if (bad || selectedCount != 1) exit 1
      print selected
    }
  ' "$checksums"
}

sha256_file() {
  file=$1
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$file" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$file" | awk '{print $1}'
  else
    fail 'sha256sum or shasum is required'
  fi
}

download() {
  path=$1
  destination=$2
  curl --fail --silent --show-error --proto '=https,http' --tlsv1.2 --max-redirs 0 \
    --connect-timeout 10 --max-time 120 --output "$destination" "$origin/install/$path"
}

file_owner() {
  stat -f '%u' "$1" 2>/dev/null || stat -c '%u' "$1"
}

file_mode() {
  stat -f '%Lp' "$1" 2>/dev/null || stat -c '%a' "$1"
}

require_private_directory() {
  directory=$1
  if [ -e "$directory" ] || [ -L "$directory" ]; then
    [ -d "$directory" ] && [ ! -L "$directory" ] || fail "unsafe directory: $directory"
  else
    mkdir "$directory" || fail "cannot create directory: $directory"
  fi
  chmod 700 "$directory" || fail "cannot secure directory: $directory"
  [ "$(file_owner "$directory")" = "$(id -u)" ] || fail "directory is not owned by the current user"
  [ "$(file_mode "$directory")" = 700 ] || fail "directory permissions are not private"
}

require_private_file() {
  file=$1
  [ -f "$file" ] && [ ! -L "$file" ] || fail "unsafe helper: $file"
  [ "$(file_owner "$file")" = "$(id -u)" ] || fail "helper is not owned by the current user"
  [ "$(file_mode "$file")" = 700 ] || fail "helper permissions are not private"
}

install_helper() {
  source=$1
  target=$2
  if [ -e "$target" ] || [ -L "$target" ]; then
    [ -f "$target" ] && [ ! -L "$target" ] || fail "existing helper is unsafe: $target"
  fi
  chmod 700 "$source" || fail 'cannot secure downloaded helper'
  require_private_file "$source"
  mv -f "$source" "$target" || fail 'cannot atomically install helper'
  require_private_file "$target"
}

origin=$(require_origin)
case "$(uname -s)" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) fail 'unsupported operating system' ;;
esac
case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) fail 'unsupported processor architecture' ;;
esac

[ -n "${HOME-}" ] || fail 'HOME is required'
case "$HOME" in /*) ;; *) fail 'HOME must be absolute' ;; esac
require_private_directory "$HOME/.llmapi"
require_private_directory "$HOME/.llmapi/bin"
temporary_directory=$(mktemp -d "$HOME/.llmapi/bin/.install.XXXXXXXX") || fail 'cannot create private temporary directory'
trap 'rm -rf "$temporary_directory"' EXIT HUP INT TERM
require_private_directory "$temporary_directory"

checksums="$temporary_directory/checksums.txt"
download checksums.txt "$checksums"

onboard_artifact="llmapi-onboard-$os-$arch"
bridge_artifact="llmapi-mcp-bridge-$os-$arch"
download "$onboard_artifact" "$temporary_directory/$onboard_artifact"
download "$bridge_artifact" "$temporary_directory/$bridge_artifact"

onboard_checksum=$(checksum_for "$onboard_artifact") || fail 'checksum file is invalid or lacks onboarding helper'
bridge_checksum=$(checksum_for "$bridge_artifact") || fail 'checksum file is invalid or lacks MCP helper'
[ "$(sha256_file "$temporary_directory/$onboard_artifact")" = "$onboard_checksum" ] || fail 'onboarding helper checksum mismatch'
[ "$(sha256_file "$temporary_directory/$bridge_artifact")" = "$bridge_checksum" ] || fail 'MCP helper checksum mismatch'

install_helper "$temporary_directory/$onboard_artifact" "$HOME/.llmapi/bin/llmapi-onboard"
install_helper "$temporary_directory/$bridge_artifact" "$HOME/.llmapi/bin/llmapi-mcp-bridge"
rm -f "$checksums"
rmdir "$temporary_directory" || fail 'cannot clean temporary downloads'
trap - EXIT HUP INT TERM

export LLMAPI_SERVER_URL="$origin"
exec "$HOME/.llmapi/bin/llmapi-onboard" install
