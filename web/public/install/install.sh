#!/usr/bin/env bash
# Secure same-origin bootstrap for the LLMAPI onboarding helpers.
set -eu
umask 077

fail() { printf '%s\n' "LLMAPI installer: $*" >&2; exit 1; }

require_origin() {
  origin=${LLMAPI_SERVER_URL-}
  [ -n "$origin" ] || fail 'LLMAPI_SERVER_URL is required'
  origin=${origin%/}
  case "$origin" in https://*) scheme=https; hostport=${origin#https://} ;; http://*) scheme=http; hostport=${origin#http://} ;; *) fail 'server URL must use HTTPS or loopback HTTP' ;; esac
  [ -n "$hostport" ] || fail 'server URL has no host'
  case "$hostport" in *[/?#@]*|*[![:print:]]*) fail 'server URL must be an origin' ;; esac
  if [ "$scheme" = http ]; then
    case "$hostport" in
      localhost|127.0.0.1|'[::1]') ;;
      localhost:[0-9]*|127.0.0.1:[0-9]*|'[::1]':[0-9]*) port=${hostport##*:}; case "$port" in *[!0-9]*|'') fail 'loopback HTTP port is invalid' ;; esac ;;
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
      digest = substr($0, 1, 64); name = substr($0, 67)
      if (length(digest) != 64 || digest !~ /^[0-9a-fA-F]+$/ || name == "" || name !~ /^[A-Za-z0-9._-]+$/ || index(name, "..") != 0) { invalid(); next }
      if (seenLine[$0]++ || seenName[name]++) { invalid(); next }
      if (name == target) { selected = tolower(digest); selectedCount++ }
    }
    END { if (bad || selectedCount != 1) exit 1; print selected }
  ' "$checksums"
}

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then shasum -a 256 "$1" | awk '{print $1}'
  else fail 'sha256sum or shasum is required'; fi
}

download() {
  path=$1 destination=$2
  status=$(curl --fail --silent --show-error --proto '=https,http' --tlsv1.2 --max-redirs 0 --connect-timeout 10 --max-time 120 --output "$destination" --write-out '%{http_code}' "$origin$path") || return 1
  case "$status" in 2[0-9][0-9]) ;; *) rm -f "$destination"; return 1 ;; esac
}

file_owner() { stat -f '%u' "$1" 2>/dev/null || stat -c '%u' "$1"; }
file_mode() { stat -f '%Lp' "$1" 2>/dev/null || stat -c '%a' "$1"; }

clear_extra_acls() {
  path=$1
  case "$os" in
    darwin)
      chmod -N "$path" 2>/dev/null || fail "cannot remove inherited ACLs: $path"
      [ -z "$(ls -led "$path" 2>/dev/null | sed 1d)" ] || fail "extra ACL entries remain: $path"
      ;;
    linux)
      if command -v setfacl >/dev/null 2>&1; then setfacl -b -- "$path" || fail "cannot remove ACLs: $path"; fi
      if command -v getfacl >/dev/null 2>&1; then
        getfacl -cp -- "$path" | awk '/^#/ { next } /^user::/ || /^group::---$/ || /^other::---$/ { next } { exit 1 }' || fail "non-owner ACL access remains: $path"
      fi
      ;;
  esac
}

require_private_directory() {
  directory=$1
  if [ -e "$directory" ] || [ -L "$directory" ]; then [ -d "$directory" ] && [ ! -L "$directory" ] || fail "unsafe directory: $directory"; else mkdir "$directory" || fail "cannot create directory: $directory"; fi
  chmod 700 "$directory" || fail "cannot secure directory: $directory"
  clear_extra_acls "$directory"
  chmod 700 "$directory" || fail "cannot secure directory: $directory"
  [ "$(file_owner "$directory")" = "$(id -u)" ] || fail "directory is not owned by the current user"
  [ "$(file_mode "$directory")" = 700 ] || fail "directory permissions are not private"
}

require_private_file() {
  file=$1
  [ -f "$file" ] && [ ! -L "$file" ] || fail "unsafe helper: $file"
  chmod 700 "$file" || fail "cannot secure helper: $file"
  clear_extra_acls "$file"
  chmod 700 "$file" || fail "cannot secure helper: $file"
  [ "$(file_owner "$file")" = "$(id -u)" ] || fail "helper is not owned by the current user"
  [ "$(file_mode "$file")" = 700 ] || fail "helper permissions are not private"
}

rollback_pair() {
  [ "${transaction_committed:-0}" = 1 ] && return
  rollback_failed=0
  if [ "${onboard_backup_ready:-0}" = 1 ]; then
    if mv -f "$backup_directory/llmapi-onboard" "$onboard_target"; then onboard_backup_ready=0; else rollback_failed=1; fi
  elif [ "${onboard_had_original:-0}" = 0 ]; then rm -f "${onboard_target:-}" || :; fi
  if [ "${bridge_backup_ready:-0}" = 1 ]; then
    if mv -f "$backup_directory/llmapi-mcp-bridge" "$bridge_target"; then bridge_backup_ready=0; else rollback_failed=1; fi
  elif [ "${bridge_had_original:-0}" = 0 ]; then rm -f "${bridge_target:-}" || :; fi
  [ "$rollback_failed" = 0 ]
}

cleanup() {
  [ "${cleanup_done:-0}" = 0 ] || return
  cleanup_done=1
  if ! rollback_pair; then
    printf '%s\n' "LLMAPI installer: rollback could not restore every helper; recover from $temporary_directory" >&2
    return 1
  fi
  [ -z "${temporary_directory:-}" ] || rm -rf "$temporary_directory" || :
}

handle_signal() {
  trap - EXIT HUP INT TERM
  cleanup
  exit "$1"
}

replace_helper_pair() {
  require_private_file "$temporary_directory/$onboard_artifact"
  require_private_file "$temporary_directory/$bridge_artifact"
  if [ -e "$onboard_target" ] || [ -L "$onboard_target" ]; then onboard_had_original=1; require_private_file "$onboard_target"; fi
  if [ -e "$bridge_target" ] || [ -L "$bridge_target" ]; then bridge_had_original=1; require_private_file "$bridge_target"; fi
  mkdir "$backup_directory" || return 1
  require_private_directory "$backup_directory"
  if [ "$onboard_had_original" = 1 ]; then mv "$onboard_target" "$backup_directory/llmapi-onboard" || return 1; onboard_backup_ready=1; fi
  if [ "$bridge_had_original" = 1 ]; then mv "$bridge_target" "$backup_directory/llmapi-mcp-bridge" || return 1; bridge_backup_ready=1; fi
  mv "$temporary_directory/$onboard_artifact" "$onboard_target" || return 1
  require_private_file "$onboard_target"
  mv "$temporary_directory/$bridge_artifact" "$bridge_target" || return 1
  require_private_file "$bridge_target"
  [ "$(sha256_file "$onboard_target")" = "$onboard_checksum" ] || return 1
  [ "$(sha256_file "$bridge_target")" = "$bridge_checksum" ] || return 1
  transaction_committed=1
  rm -rf "$backup_directory" || return 1
}

origin=$(require_origin)
case "$(uname -s)" in Linux) os=linux ;; Darwin) os=darwin ;; *) fail 'unsupported operating system' ;; esac
case "$(uname -m)" in x86_64|amd64) arch=amd64 ;; arm64|aarch64) arch=arm64 ;; *) fail 'unsupported processor architecture' ;; esac
[ -n "${HOME-}" ] || fail 'HOME is required'
case "$HOME" in /*) ;; *) fail 'HOME must be absolute' ;; esac
require_private_directory "$HOME/.llmapi"
require_private_directory "$HOME/.llmapi/bin"
require_private_directory "$HOME/.llmapi/checksums"
temporary_directory=$(mktemp -d "$HOME/.llmapi/bin/.install.XXXXXXXX") || fail 'cannot create private temporary directory'
transaction_committed=0
onboard_had_original=0
bridge_had_original=0
onboard_backup_ready=0
bridge_backup_ready=0
cleanup_done=0
trap cleanup EXIT
trap 'handle_signal 129' HUP
trap 'handle_signal 130' INT
trap 'handle_signal 143' TERM
require_private_directory "$temporary_directory"

checksums="$temporary_directory/checksums.txt"
onboard_artifact="llmapi-onboard-$os-$arch"
bridge_artifact="llmapi-mcp-bridge-$os-$arch"
onboard_target="$HOME/.llmapi/bin/llmapi-onboard"
bridge_target="$HOME/.llmapi/bin/llmapi-mcp-bridge"
checksum_target="$HOME/.llmapi/checksums/llmapi-onboard.sha256"
backup_directory="$temporary_directory/backups"

download /install/checksums.txt "$checksums"
download "/install/bin/$onboard_artifact" "$temporary_directory/$onboard_artifact"
download "/install/bin/$bridge_artifact" "$temporary_directory/$bridge_artifact"
onboard_checksum=$(checksum_for "$onboard_artifact") || fail 'checksum file is invalid or lacks onboarding helper'
bridge_checksum=$(checksum_for "$bridge_artifact") || fail 'checksum file is invalid or lacks MCP helper'
[ "$(sha256_file "$temporary_directory/$onboard_artifact")" = "$onboard_checksum" ] || fail 'onboarding helper checksum mismatch'
[ "$(sha256_file "$temporary_directory/$bridge_artifact")" = "$bridge_checksum" ] || fail 'MCP helper checksum mismatch'
replace_helper_pair || fail 'cannot transactionally install both helpers'
printf '%s\n' "$onboard_checksum" > "$temporary_directory/llmapi-onboard.sha256" || fail 'cannot stage helper checksum'
chmod 600 "$temporary_directory/llmapi-onboard.sha256" || fail 'cannot secure helper checksum'
mv "$temporary_directory/llmapi-onboard.sha256" "$checksum_target" || fail 'cannot install helper checksum'
require_private_file "$checksum_target"
chmod 600 "$checksum_target" || fail 'cannot secure helper checksum'

rm -rf "$temporary_directory" || fail 'cannot clean temporary downloads'
temporary_directory=
trap - EXIT HUP INT TERM
export LLMAPI_SERVER_URL="$origin"
if [ -r /dev/tty ] && [ -w /dev/tty ] && (: </dev/tty) 2>/dev/null; then exec "$onboard_target" install </dev/tty; fi
exec "$onboard_target" install
