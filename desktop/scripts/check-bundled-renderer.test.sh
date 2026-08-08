#!/usr/bin/env bash
#
# Regression tests for check-bundled-renderer.sh using disposable renderer
# fixtures. These cover source/package preflight failures before build-mac.sh
# invokes electron-builder.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && cd .. && pwd)"
CHECK="$SCRIPT_DIR/check-bundled-renderer.sh"
SOURCE_RENDERER="$REPO_ROOT/desktop/renderer/en/desktop"

tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/workmax-check-bundled-renderer-test.XXXXXX")"
trap 'rm -rf "$tmp_dir"' EXIT

make_fixture() {
  local fixture="$1"
  mkdir -p "$fixture"
  cp "$SOURCE_RENDERER/index.html" "$fixture/index.html"
  cp "$SOURCE_RENDERER/styles.css" "$fixture/styles.css"
  cp "$SOURCE_RENDERER/renderer.js" "$fixture/renderer.js"
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

missing_csp_fixture="$tmp_dir/missing-csp"
make_fixture "$missing_csp_fixture"
perl -0pi -e 's/\s*<meta\s+http-equiv="Content-Security-Policy"\s+content="[^"]+"\s*\/>//s' "$missing_csp_fixture/index.html"
expect_fail "rejects missing CSP" "$missing_csp_fixture" "index.html must declare a Content-Security-Policy"

bad_csp_fixture="$tmp_dir/bad-csp"
make_fixture "$bad_csp_fixture"
perl -0pi -e 's/connect-src http:\/\/127\.0\.0\.1:\*/connect-src https:\/\/workmax.app/' "$bad_csp_fixture/index.html"
expect_fail "rejects non-loopback CSP" "$bad_csp_fixture" "CSP directive connect-src mismatch"

extra_csp_origin_fixture="$tmp_dir/extra-csp-origin"
make_fixture "$extra_csp_origin_fixture"
perl -0pi -e 's/connect-src http:\/\/127\.0\.0\.1:\*/connect-src http:\/\/127.0.0.1:* https:\/\/workmax.app/' "$extra_csp_origin_fixture/index.html"
expect_fail "rejects extra remote CSP connect source" \
  "$extra_csp_origin_fixture" \
  "CSP directive connect-src mismatch"

bad_css_ref_fixture="$tmp_dir/bad-css-ref"
make_fixture "$bad_css_ref_fixture"
perl -0pi -e 's/href="\.\/styles\.css"/href="styles.css"/' "$bad_css_ref_fixture/index.html"
expect_fail "rejects non-relative CSS reference" "$bad_css_ref_fixture" "index.html must reference ./styles.css"

bad_js_ref_fixture="$tmp_dir/bad-js-ref"
make_fixture "$bad_js_ref_fixture"
perl -0pi -e 's/src="\.\/renderer\.js"/src="renderer.js"/' "$bad_js_ref_fixture/index.html"
expect_fail "rejects non-relative JS reference" "$bad_js_ref_fixture" "index.html must reference ./renderer.js"

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
