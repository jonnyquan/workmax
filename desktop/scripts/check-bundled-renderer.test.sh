#!/usr/bin/env bash
#
# Regression tests for check-bundled-renderer.sh using disposable renderer
# fixtures. These cover source/package preflight failures before build-mac.sh
# packages the app.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && cd .. && pwd)"
CHECK="$SCRIPT_DIR/check-bundled-renderer.sh"
SOURCE_RENDERER="$REPO_ROOT/desktop/renderer/en/desktop"

tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/workmax-check-bundled-renderer-test.XXXXXX")"
trap 'rm -rf "$tmp_dir"' EXIT

# A fixture is a whole renderer directory, not the three files the CSP cases
# happen to touch.
#
# It used to copy only index.html, styles.css and renderer.js. That was enough
# until the behaviour suite began reading shim.js and lib/desktop-bridge.js,
# after which the very first case — "accepts current bundled renderer fixture"
# — failed on a missing file, and had been failing ever since. The check under
# test was fine; its fixture was not a renderer.
make_fixture() {
  local fixture="$1"
  mkdir -p "$fixture"
  cp -R "$SOURCE_RENDERER/." "$fixture/"
}

expect_pass() {
  local name="$1"
  local fixture="$2"
  local output
  if ! output="$(WORKMAX_BUNDLED_RENDERER_DIR="$fixture" "$CHECK" 2>&1)"; then
    printf 'not ok - %s\n%s\n' "$name" "$output" >&2
    exit 1
  fi
  printf 'ok - %s\n' "$name"
}

expect_fail() {
  local name="$1"
  local fixture="$2"
  local want="$3"
  local output
  if output="$(WORKMAX_BUNDLED_RENDERER_DIR="$fixture" "$CHECK" 2>&1)"; then
    printf 'not ok - %s: check unexpectedly passed\n%s\n' "$name" "$output" >&2
    exit 1
  fi
  if ! grep -Fq -- "$want" <<<"$output"; then
    printf 'not ok - %s: missing expected failure text: %s\n%s\n' "$name" "$want" "$output" >&2
    exit 1
  fi
  printf 'ok - %s\n' "$name"
}

valid_fixture="$tmp_dir/valid"
make_fixture "$valid_fixture"
expect_pass "accepts current bundled renderer fixture" "$valid_fixture"

missing_file_fixture="$tmp_dir/missing-file"
make_fixture "$missing_file_fixture"
rm "$missing_file_fixture/renderer.js"
expect_fail "rejects missing renderer.js" "$missing_file_fixture" "missing or empty bundled renderer file"

# The policy has one home: the response header in desktop/wails/uiserver.go.
# These cases used to edit a <meta http-equiv> in index.html, which was the
# second copy — and the two had drifted, which is why the meta is gone. What is
# checked now is that the meta cannot come back and that the header's directives
# are still pinned.
readded_meta_fixture="$tmp_dir/readded-meta"
make_fixture "$readded_meta_fixture"
perl -0pi -e 's/<meta charset="utf-8" \/>/<meta charset="utf-8" \/>\n    <meta http-equiv="Content-Security-Policy" content="default-src *" \/>/' \
  "$readded_meta_fixture/index.html"
expect_fail "rejects a re-added CSP meta tag" \
  "$readded_meta_fixture" \
  "index.html must NOT declare a Content-Security-Policy meta tag"

# The header cases vary a copy of uiserver.go rather than a renderer file, so
# they need the fixture directory to stay valid while the Go source is broken.
expect_fail_ui_server() {
  local name="$1"
  local ui_server="$2"
  local want="$3"
  local output
  if output="$(WORKMAX_BUNDLED_RENDERER_DIR="$valid_fixture" \
    WORKMAX_UI_SERVER_SOURCE="$ui_server" "$CHECK" 2>&1)"; then
    printf 'not ok - %s: check unexpectedly passed\n%s\n' "$name" "$output" >&2
    exit 1
  fi
  if ! grep -Fq -- "$want" <<<"$output"; then
    printf 'not ok - %s: missing expected failure text: %s\n%s\n' "$name" "$want" "$output" >&2
    exit 1
  fi
  printf 'ok - %s\n' "$name"
}

bad_csp_source="$tmp_dir/uiserver-remote-connect.go"
perl -pe "s/\"connect-src 'self'; \" \+/\"connect-src https:\\/\\/workmax.app; \" +/" \
  "$REPO_ROOT/desktop/wails/uiserver.go" > "$bad_csp_source"
expect_fail_ui_server "rejects a non-self connect-src in the header" \
  "$bad_csp_source" \
  "CSP directive connect-src mismatch"

extra_csp_origin_source="$tmp_dir/uiserver-extra-origin.go"
perl -pe "s/\"connect-src 'self'; \" \+/\"connect-src 'self' https:\\/\\/workmax.app; \" +/" \
  "$REPO_ROOT/desktop/wails/uiserver.go" > "$extra_csp_origin_source"
expect_fail_ui_server "rejects an extra remote CSP connect source" \
  "$extra_csp_origin_source" \
  "CSP directive connect-src mismatch"

missing_csp_source="$tmp_dir/uiserver-no-policy.go"
perl -0pe 's/const contentSecurityPolicy = /const someOtherPolicy = /' \
  "$REPO_ROOT/desktop/wails/uiserver.go" > "$missing_csp_source"
expect_fail_ui_server "rejects a missing header policy" \
  "$missing_csp_source" \
  "could not read contentSecurityPolicy"

bad_css_ref_fixture="$tmp_dir/bad-css-ref"
make_fixture "$bad_css_ref_fixture"
perl -0pi -e 's/href="\.\/styles\.css"/href="styles.css"/' "$bad_css_ref_fixture/index.html"
expect_fail "rejects non-relative CSS reference" "$bad_css_ref_fixture" "index.html must reference ./styles.css"

bad_js_ref_fixture="$tmp_dir/bad-js-ref"
make_fixture "$bad_js_ref_fixture"
perl -0pi -e 's/src="\.\/renderer\.js"/src="renderer.js"/' "$bad_js_ref_fixture/index.html"
expect_fail "rejects non-relative JS reference" "$bad_js_ref_fixture" "index.html must load ./renderer.js as a module"

# The renderer is a module graph now, and the two ways to break that are both
# silent in review and total at runtime: dropping type="module" (the first
# import is a syntax error) and adding it to a bridge script (which defers the
# bridge past the renderer that expects it to be installed already).
classic_entry_fixture="$tmp_dir/classic-entry"
make_fixture "$classic_entry_fixture"
perl -0pi -e 's/<script type="module" src="\.\/renderer\.js">/<script src=".\/renderer.js">/' \
  "$classic_entry_fixture/index.html"
expect_fail "rejects a renderer entry loaded as a classic script" "$classic_entry_fixture" \
  "index.html must load ./renderer.js as a module"

module_shim_fixture="$tmp_dir/module-shim"
make_fixture "$module_shim_fixture"
perl -0pi -e 's/<script src="\.\/shim\.js">/<script type="module" src=".\/shim.js">/' \
  "$module_shim_fixture/index.html"
expect_fail "rejects a bridge script promoted to a module" "$module_shim_fixture" \
  "index.html must load ./shim.js as a classic script"

# A module that is imported but not on the allowlist would be a 404 during
# link — a blank window, not a missing feature — so the allowlist has to fail
# on a file it has never heard of even though the file is real and imported.
stray_module_fixture="$tmp_dir/stray-module"
make_fixture "$stray_module_fixture"
printf 'export const stray = 1;\n' > "$stray_module_fixture/stray.js"
expect_fail "rejects a renderer module nobody put on the allowlist" "$stray_module_fixture" \
  "bundled renderer source contains unexpected files"

secret_fixture="$tmp_dir/secret"
make_fixture "$secret_fixture"
printf '\nconst leaked = "access_token=leak";\n' >> "$secret_fixture/renderer.js"
expect_fail "rejects token-like strings" "$secret_fixture" "bundled renderer source must not embed token-like secrets"

extra_file_fixture="$tmp_dir/extra-file"
make_fixture "$extra_file_fixture"
printf 'debug\n' > "$extra_file_fixture/renderer.js.map"
expect_fail "rejects unexpected bundled renderer source file" \
  "$extra_file_fixture" \
  "bundled renderer source contains unexpected files"
