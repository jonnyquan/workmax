#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$script_dir/../.." && pwd)"
host_goos="$(go env GOHOSTOS)"
host_goarch="$(go env GOHOSTARCH)"

node "$repo_root/desktop/scripts/check-desktop-boundaries.mjs"
node "$repo_root/desktop/scripts/check-bundled-renderer-behavior.mjs"

(
  cd "$repo_root/desktop/wails"
  # The shell's own tests: UI origin capability, privileged-route refusal,
  # containment headers. CGO because Wails binds Cocoa/WebKit through it.
  env GOOS="$host_goos" GOARCH="$host_goarch" CGO_ENABLED=1 go test -tags desktop ./...
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
    ./middleware/...
)

# The desktop package must still build without cgo. That is what keeps the
# knowledge seam honest and lets a machine with no C toolchain run these tests.
(
  cd "$repo_root/server"
  env GOOS="$host_goos" GOARCH="$host_goarch" CGO_ENABLED=0 go build -tags desktop ./...
)

echo "desktop verification passed"
