#!/usr/bin/env bash
# Regression tests for inspect-mac-package.sh.
#
# The inspector is the last thing between a packaging mistake and Apple, so
# each rejection it claims to make is exercised against a disposable bundle
# built to fail exactly that way. A check nobody has watched fail is a check
# nobody knows is wired up — this suite exists because the previous inspector's
# connect-src rule used a grep feature grep does not have and matched nothing
# for as long as it existed.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
INSPECT="$SCRIPT_DIR/inspect-mac-package.sh"
RENDERER_SRC="$REPO_ROOT/desktop/renderer/en/desktop"

tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/workmax-inspect-test.XXXXXX")"
trap 'rm -rf "$tmp_dir"' EXIT

failures=0

expect_ok() {
  local name="$1"; shift
  if "$@" >/dev/null 2>&1; then
    echo "ok - $name"
  else
    echo "FAIL - $name (expected success)" >&2
    failures=$((failures + 1))
  fi
}

expect_fail() {
  local name="$1" expected="$2"; shift 2
  local output
  if output="$("$@" 2>&1)"; then
    echo "FAIL - $name (expected failure, got success)" >&2
    failures=$((failures + 1))
    return
  fi
  if ! printf '%s\n' "$output" | grep -Fq "$expected"; then
    echo "FAIL - $name (expected message containing: $expected)" >&2
    printf '  got: %s\n' "$output" >&2
    failures=$((failures + 1))
    return
  fi
  echo "ok - $name"
}

# make_app <name> — a bundle that passes everything, ready to be broken.
make_app() {
  local app="$tmp_dir/$1/WorkMax Desktop.app"
  local renderer="$app/Contents/Resources/renderer/en/desktop"
  mkdir -p "$app/Contents/MacOS" "$renderer/lib"
  printf '#!/bin/sh\n# 0.1.0-p1-ea\nexit 0\n' > "$app/Contents/MacOS/WorkMax Desktop"
  chmod +x "$app/Contents/MacOS/WorkMax Desktop"
  cp "$RENDERER_SRC/index.html" "$RENDERER_SRC/styles.css" "$RENDERER_SRC/renderer.js" \
     "$RENDERER_SRC/shim.js" "$renderer/"
  cp "$RENDERER_SRC/lib/desktop-bridge.js" "$renderer/lib/"
  printf 'icnsfake icon bytes\n' > "$app/Contents/Resources/icon.icns"
  cat > "$app/Contents/Info.plist" <<'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleIdentifier</key><string>ai.workmax.desktop</string>
<key>CFBundleExecutable</key><string>WorkMax Desktop</string>
<key>CFBundleShortVersionString</key><string>0.1.0-p1-ea</string>
<key>CFBundleIconFile</key><string>icon.icns</string>
</dict></plist>
PLIST
  printf '%s' "$app"
}

app="$(make_app baseline)"
expect_ok "accepts a well-formed bundle" \
  "$INSPECT" --require-bundled-renderer --require-app-icon "$app"

expect_fail "rejects a non-.app path" "not an .app bundle" \
  "$INSPECT" "$tmp_dir/baseline"

# The bundle id scopes the user's Keychain entries; a build under a different
# id silently cannot see the session they already have.
app="$(make_app wrong-id)"
sed -i '' 's|ai\.workmax\.desktop|ai.workmax.other|' "$app/Contents/Info.plist"
expect_fail "rejects a bundle id that would orphan the Keychain session" "want ai.workmax.desktop" \
  "$INSPECT" "$app"

# A stale executable next to a bumped Info.plist ships old code under a new
# version number, which is worse than shipping old code honestly.
app="$(make_app stale-binary)"
printf '#!/bin/sh\nexit 0\n' > "$app/Contents/MacOS/WorkMax Desktop"
chmod +x "$app/Contents/MacOS/WorkMax Desktop"
expect_fail "rejects an executable that does not carry the declared version" "it is stale relative to Info.plist" \
  "$INSPECT" "$app"

# The shell and the sidecar are one process now; a packaged sidecar means the
# packaging step is still following the retired layout.
app="$(make_app stray-sidecar)"
printf 'stale sidecar\n' > "$app/Contents/Resources/workagent-desktop"
expect_fail "rejects a packaged sidecar binary" "the separate sidecar binary was retired" \
  "$INSPECT" "$app"

app="$(make_app second-binary)"
printf 'helper\n' > "$app/Contents/MacOS/helper"
expect_fail "rejects a second executable in MacOS" "want exactly 1" \
  "$INSPECT" "$app"

app="$(make_app stray-resource)"
printf 'notes\n' > "$app/Contents/Resources/leftover.txt"
expect_fail "rejects unreviewed entries in Resources" "unexpected entry in Contents/Resources" \
  "$INSPECT" "$app"

app="$(make_app extra-renderer-file)"
printf 'console.log(1)\n' > "$app/Contents/Resources/renderer/en/desktop/debug.js"
expect_fail "rejects unreviewed files in the bundled renderer" "unexpected file in bundled renderer" \
  "$INSPECT" --require-bundled-renderer "$app"

app="$(make_app missing-shim)"
rm "$app/Contents/Resources/renderer/en/desktop/shim.js"
expect_fail "rejects a renderer missing the shim that installs its bridge" "missing or has an empty shim.js" \
  "$INSPECT" --require-bundled-renderer "$app"

# CSP is the primary containment control on a shell with no cancellable
# navigation hook, so it is verified in the artifact rather than trusted from
# the repository.
app="$(make_app remote-connect-src)"
sed -i '' 's|connect-src http://127\.0\.0\.1:\*|connect-src https://evil.example|' \
  "$app/Contents/Resources/renderer/en/desktop/index.html"
expect_fail "rejects a non-loopback connect-src" "non-loopback connect-src source" \
  "$INSPECT" --require-bundled-renderer "$app"

app="$(make_app inline-csp)"
sed -i '' "s|script-src 'self'|script-src 'self' 'unsafe-inline'|" \
  "$app/Contents/Resources/renderer/en/desktop/index.html"
expect_fail "rejects an inline exemption in the packaged CSP" "grants an inline exemption" \
  "$INSPECT" --require-bundled-renderer "$app"

app="$(make_app remote-script)"
sed -i '' 's|<script src="./renderer.js">|<script src="https://cdn.example/r.js">|' \
  "$app/Contents/Resources/renderer/en/desktop/index.html"
expect_fail "rejects a remotely loaded script" "loads a remote resource via src=" \
  "$INSPECT" --require-bundled-renderer "$app"

app="$(make_app sourcemap)"
printf '{}\n' > "$app/Contents/Resources/renderer/en/desktop/lib/desktop-bridge.js.map"
expect_fail "rejects a shipped source map (via the renderer allowlist)" "unexpected file in bundled renderer" \
  "$INSPECT" --require-bundled-renderer "$app"

app="$(make_app no-icon)"
rm "$app/Contents/Resources/icon.icns"
expect_fail "rejects a missing app icon when required" "missing or empty Contents/Resources/icon.icns" \
  "$INSPECT" --require-app-icon "$app"

app="$(make_app bad-icon)"
printf 'not an icns\n' > "$app/Contents/Resources/icon.icns"
expect_fail "rejects an icon that is not really icns" "not a valid icns file" \
  "$INSPECT" --require-app-icon "$app"

# Signature verification deliberately lives in notarize-mac.sh, so the flag is
# accepted and does nothing here. Asserting that keeps someone from "fixing"
# the apparent gap by adding a second, weaker signature check.
app="$(make_app signature-flag)"
expect_ok "accepts --require-developer-id-signature without duplicating notarize's signature checks" \
  "$INSPECT" --require-developer-id-signature "$app"

if [ "$failures" -ne 0 ]; then
  echo "inspect-mac-package.test.sh: $failures check(s) failed" >&2
  exit 1
fi
echo "ok inspect-mac-package.test.sh"
