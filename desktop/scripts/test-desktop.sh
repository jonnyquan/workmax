#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$script_dir/../.." && pwd)"
host_goos="$(go env GOHOSTOS)"
host_goarch="$(go env GOHOSTARCH)"

node "$repo_root/desktop/scripts/check-desktop-boundaries.mjs"
node "$repo_root/desktop/scripts/check-bundled-renderer-behavior.mjs"

(
  cd "$repo_root/desktop/electron"
  npm test
)

(
  cd "$repo_root/server"
  env \
    -u BODO_CONFIG \
    -u WORKMAX_TEST_MYSQL_DSN \
    -u WORKMAX_TEST_MYSQL_DSN_ADMIN \
    -u WORKMAX_TEST_MYSQL_DSN_APP \
    -u WORKMAX_AGENTTURN_MYSQL_DSN \
    -u WORKMAX_AGENTTURN_MYSQL_DSN_ADMIN \
    -u WORKMAX_AGENTTURN_MYSQL_DSN_APP \
    -u WORKMAX_AGENTTURN_MYSQL_CONFIG \
    -u WORKMAX_AGENTTURN_MYSQL_CONTRACT \
    -u WORKMAX_AGENTTURN_MYSQL_ALLOW_DIRECT_DSN \
    -u WORKMAX_AGENTTURN_MYSQL_ALLOW_PLAINTEXT \
    -u WORKMAX_AGENTTURN_MYSQL_ALLOW_INSECURE_TLS \
    GOOS="$host_goos" \
    GOARCH="$host_goarch" \
    go test -tags desktop \
    ./desktop/... \
    ./api/desktop/... \
    ./service/desktop/... \
    ./router/desktop/... \
    ./middleware/... \
    ./cmd/workagent-desktop
)

echo "desktop verification passed"
