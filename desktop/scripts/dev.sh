#!/usr/bin/env bash
#
# Local dev orchestrator for WorkMax Desktop.
#
# What this does:
#   1. Builds the Go sidecar (workagent-desktop) for the host platform,
#      placing the binary at desktop/electron/bin/workagent-desktop
#   2. Restores Electron npm deps when required local binaries are missing
#   3. Compiles desktop/electron/src/*.ts → dist/
#   4. Selects an explicit trusted Desktop Renderer
#   5. Launches Electron, which spawns the sidecar and opens the window
#
# Repo go env defaults to GOOS=linux GOARCH=amd64 for production
# cross-compile, so this script detects the host platform and overrides
# both vars explicitly (see desktop/README.md "Heads-up for local dev").
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && cd .. && pwd)"

# Detect host platform → GOOS/GOARCH for the sidecar build.
host_signature="$(uname -sm)"
case "$host_signature" in
  "Darwin arm64")   target_goos="darwin"; target_goarch="arm64" ;;
  "Darwin x86_64")  target_goos="darwin"; target_goarch="amd64" ;;
  "Linux x86_64")   target_goos="linux";  target_goarch="amd64" ;;
  "Linux aarch64")  target_goos="linux";  target_goarch="arm64" ;;
  *)
    echo "dev.sh: unsupported host platform '$host_signature'" >&2
    echo "  add a case for it above, then rerun." >&2
    exit 1
    ;;
esac

bin_dir="$REPO_ROOT/desktop/electron/bin"
binary_path="$bin_dir/workagent-desktop"
desktop_version="$(cd "$REPO_ROOT/desktop/electron" && node -p "require('./package.json').version")"

# Development never inherits an implicit hosted Renderer. Default to the
# repository-bundled Desktop shell so this workspace is self-contained. To use
# a live development server, explicitly set WORKMAX_DESKTOP_RENDERER_URL to a
# loopback /desktop route. Remote development renderers additionally require a
# comma-separated bare HTTPS allowlist in
# WORKMAX_DESKTOP_TRUSTED_RENDERER_ORIGINS.
if [ "${WORKMAX_DESKTOP_RENDERER_URL+x}" != "x" ]; then
  bundled_renderer_entry="$REPO_ROOT/desktop/renderer/en/desktop/index.html"
  if [ ! -s "$bundled_renderer_entry" ]; then
    echo "dev.sh: bundled Desktop renderer is missing: $bundled_renderer_entry" >&2
    exit 1
  fi
  WORKMAX_DESKTOP_RENDERER_URL="$(node -e 'const { pathToFileURL } = require("node:url"); console.log(pathToFileURL(process.argv[1]).toString())' "$bundled_renderer_entry")"
  export WORKMAX_DESKTOP_RENDERER_URL
  echo "==> renderer: bundled development shell"
elif [ -z "$WORKMAX_DESKTOP_RENDERER_URL" ]; then
  echo "dev.sh: WORKMAX_DESKTOP_RENDERER_URL must not be empty" >&2
  exit 1
else
  echo "==> renderer: explicitly configured Desktop URL"
fi

echo "==> building Go sidecar for $target_goos/$target_goarch (version=$desktop_version)"
mkdir -p "$bin_dir"
( cd "$REPO_ROOT/server" \
  && GOOS="$target_goos" GOARCH="$target_goarch" \
     go build -tags desktop \
     -ldflags "-X server/desktop/buildinfo.Version=$desktop_version" \
     -o "$binary_path" ./cmd/workagent-desktop )
echo "    built: $binary_path"
sidecar_version="$("$binary_path" --version)"
if [ "$sidecar_version" != "$desktop_version" ]; then
  echo "dev.sh: sidecar version mismatch: binary=$sidecar_version package=$desktop_version" >&2
  exit 1
fi
echo "    verified sidecar version: $sidecar_version"

cd "$REPO_ROOT/desktop/electron"

if [ ! -d node_modules ] || [ ! -x node_modules/.bin/tsc ] || [ ! -x node_modules/.bin/electron ]; then
  echo "==> installing Electron npm deps"
  npm ci
fi

echo "==> compiling Electron TypeScript"
npm run build --silent

echo "==> launching Electron"
exec npm start
