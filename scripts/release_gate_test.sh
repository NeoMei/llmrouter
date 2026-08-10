#!/usr/bin/env bash
set -euo pipefail

root_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
tmp_dir=$(mktemp -d)
trap 'rm -rf "$tmp_dir"' EXIT
commit=$(git -C "$root_dir" rev-parse HEAD)

set +e
incomplete_output=$(RELEASE_GATE_FAST=1 RELEASE_COMMIT="$commit" bash "$root_dir/scripts/release-gate.sh" --evidence-dir "$tmp_dir" 2>&1)
incomplete_status=$?
set -e
test "$incomplete_status" -ne 0
for blocker in \
  'provider certification manifest is missing' \
  'image certification evidence is missing' \
  'restore rehearsal evidence is missing' \
  'desktop certification evidence is missing' \
  'nightly certification evidence is missing' \
  'source certification evidence is missing'; do
  grep -Fq "$blocker" <<<"$incomplete_output"
done

mkdir -p "$tmp_dir/provider"
jq -n --arg commit "$commit" '{
  schema_version:1,commit:$commit,registry_version:"fixture-registry-v1",generated_at:"2026-08-10T00:00:00Z",
  results:[
    {channel_type:1,channel_name:"missing",protocol:"openai",operation:"chat.completions.create",status:"missing_credential",http_status:0},
    {channel_type:1,channel_name:"placeholder",protocol:"openai",operation:"files.create",status:"fail",http_status:501}
  ]
}' > "$tmp_dir/provider-certification.json"
jq -n --arg commit "$commit" '{commit:$commit,digest:"latest",signed:false,sbom:false,provenance:false,container_smoke:false}' > "$tmp_dir/image.json"
jq -n --arg commit "$commit" '{commit:$commit,status:"fail"}' > "$tmp_dir/restore-rehearsal.json"
jq -n --arg commit "$commit" '{commit:$commit,platforms:{linux:{checksum:"bad",startup_probe:"fail"}}}' > "$tmp_dir/desktop.json"
jq -n --arg commit "$commit" '{commit:$commit,status:"fail",fault_recovery:"fail",performance:"fail",billing:"fail"}' > "$tmp_dir/nightly.json"
jq -n --arg commit "$commit" '{commit:$commit,public_501_count:1,supported_placeholder_count:1,generated_docs_clean:false}' > "$tmp_dir/source.json"
set +e
invalid_output=$(RELEASE_GATE_FAST=1 RELEASE_COMMIT="$commit" bash "$root_dir/scripts/release-gate.sh" --evidence-dir "$tmp_dir" 2>&1)
invalid_status=$?
set -e
test "$invalid_status" -ne 0
for blocker in \
  'provider certification contains 2 failed, missing-credential, or public 501 result(s)' \
  'provider certification trusted public key is missing' \
  'image evidence requires immutable digest, signature, SBOM, provenance, and container smoke pass' \
  'restore rehearsal evidence is incomplete or failed' \
  'desktop certification missing checksum or startup probe for linux' \
  'desktop certification missing checksum or startup probe for macos' \
  'desktop certification missing checksum or startup probe for windows' \
  'nightly evidence requires fault recovery, performance, and billing passes' \
  'source certification found a public 501, supported placeholder adapter, or dirty generated docs'; do
  grep -Fq "$blocker" <<<"$invalid_output"
done

mkdir -p "$tmp_dir/provider"
openssl genpkey -algorithm ED25519 -out "$tmp_dir/provider/private.pem" >/dev/null 2>&1
openssl pkey -in "$tmp_dir/provider/private.pem" -pubout -out "$tmp_dir/provider/public.pem" >/dev/null 2>&1

jq -n --arg commit "$commit" '{
  schema_version: 1,
  commit: $commit,
  registry_version: "fixture-registry-v1",
  generated_at: "2026-08-10T00:00:00Z",
  results: [{channel_type: 1, channel_name: "fixture", protocol: "openai", operation: "models.list", status: "pass", http_status: 200}]
}' > "$tmp_dir/provider-certification.json"
openssl pkeyutl -sign -rawin -inkey "$tmp_dir/provider/private.pem" \
  -in "$tmp_dir/provider-certification.json" -out "$tmp_dir/provider-certification.sig"
cp "$tmp_dir/provider/public.pem" "$tmp_dir/provider-certification.pub"

jq -n --arg commit "$commit" '{commit:$commit,digest:("sha256:" + ("a" * 64)),signed:true,sbom:true,provenance:true,container_smoke:true}' > "$tmp_dir/image.json"
jq -n --arg commit "$commit" '{commit:$commit,status:"pass",tested_at:"2026-08-10T00:00:00Z",backup_checksum:("b" * 64),rto_seconds:120}' > "$tmp_dir/restore-rehearsal.json"
jq -n --arg commit "$commit" '{commit:$commit,platforms:{linux:{checksum:("c" * 64),startup_probe:"pass"},macos:{checksum:("d" * 64),startup_probe:"pass"},windows:{checksum:("e" * 64),startup_probe:"pass"}}}' > "$tmp_dir/desktop.json"
jq -n --arg commit "$commit" '{commit:$commit,status:"pass",fault_recovery:"pass",performance:"pass",billing:"pass"}' > "$tmp_dir/nightly.json"
jq -n --arg commit "$commit" '{commit:$commit,public_501_count:0,supported_placeholder_count:0,generated_docs_clean:true}' > "$tmp_dir/source.json"

openssl genpkey -algorithm ED25519 -out "$tmp_dir/provider/trusted-private.pem" >/dev/null 2>&1
openssl pkey -in "$tmp_dir/provider/trusted-private.pem" -pubout -out "$tmp_dir/provider/trusted-public.pem" >/dev/null 2>&1
set +e
forged_output=$(RELEASE_GATE_FAST=1 RELEASE_COMMIT="$commit" \
  PROVIDER_CERTIFICATION_PUBLIC_KEY="$tmp_dir/provider/trusted-public.pem" \
  bash "$root_dir/scripts/release-gate.sh" --evidence-dir "$tmp_dir" 2>&1)
forged_status=$?
set -e
test "$forged_status" -ne 0
grep -Fq 'provider certification signature verification failed' <<<"$forged_output"

RELEASE_GATE_FAST=1 RELEASE_COMMIT="$commit" \
  PROVIDER_CERTIFICATION_PUBLIC_KEY="$tmp_dir/provider/public.pem" \
  bash "$root_dir/scripts/release-gate.sh" --evidence-dir "$tmp_dir" | grep -Fq 'release gate: passed'

echo 'release gate fixtures: ok'
