#!/usr/bin/env bash
set -euo pipefail

root_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
compose_file="$root_dir/docker-compose.test.yml"
nightly=false
if [[ "${1:-}" == "--nightly" ]]; then
  nightly=true
elif [[ $# -gt 0 ]]; then
  echo "usage: $0 [--nightly]" >&2
  exit 2
fi

if ! command -v docker >/dev/null 2>&1; then
  echo 'integration matrix requires Docker with the Compose plugin' >&2
  exit 2
fi
if ! docker compose version >/dev/null 2>&1; then
  echo 'integration matrix requires the Docker Compose plugin' >&2
  exit 2
fi

cleanup() {
  docker compose -f "$compose_file" down --volumes --remove-orphans
}
trap cleanup EXIT INT TERM

docker compose -f "$compose_file" up --detach --build --wait

export TEST_MYSQL_DSN='gateway:test-only-mysql-secret@tcp(127.0.0.1:53306)/gateway?parseTime=true'
export TEST_POSTGRES_DSN='postgresql://gateway:test-only-postgres-secret@127.0.0.1:55432/gateway?sslmode=disable'
export TEST_REDIS_URL='redis://default:test-only-redis-secret@127.0.0.1:56379/0'
export TEST_S3_ENDPOINT='http://127.0.0.1:59000'
export TEST_S3_REGION='us-east-1'
export TEST_S3_BUCKET='gateway-files'
export TEST_S3_ACCESS_KEY_ID='test-access-key'
export TEST_S3_SECRET_ACCESS_KEY='test-secret-key-that-is-long-enough'
export TEST_GATEWAY_A_URL='http://127.0.0.1:53001'
export TEST_GATEWAY_B_URL='http://127.0.0.1:53002'

cd "$root_dir"
go test -count=1 ./tests/contracts/...
go test -count=1 -tags=integration ./tests/integration/...
go test -count=1 ./service/objectstore -run 'Test(Local|S3)Store'
go test -count=1 ./model -run 'Test(UserSession|FineTuning|APIFile)'
go test -count=1 ./middleware -run 'Test.*(RateLimit|Capability)'
go test -count=1 ./controller -run 'Test.*(File|FineTuning|Variation|Model)'

if [[ "$nightly" == true ]]; then
  go test -count=1 -tags=integration ./tests/fault/... ./tests/performance/...
fi
