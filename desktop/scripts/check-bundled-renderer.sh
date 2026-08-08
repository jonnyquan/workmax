#!/usr/bin/env bash
#
# Validate the source bundled renderer before mac packaging copies it into
# Contents/Resources/renderer/.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && cd .. && pwd)"
renderer_dir="${WORKMAX_BUNDLED_RENDERER_DIR:-$REPO_ROOT/desktop/renderer/en/desktop}"
entry="$renderer_dir/index.html"
css="$renderer_dir/styles.css"
js="$renderer_dir/renderer.js"

for file in "$entry" "$css" "$js"; do
  if [ ! -s "$file" ]; then
    echo "check-bundled-renderer.sh: missing or empty bundled renderer file: $file" >&2
    exit 1
  fi
done

node - "$renderer_dir" <<'NODE'
const fs = require("node:fs");
const path = require("node:path");
const rendererDir = process.argv[2];
const allowed = new Set(["index.html", "styles.css", "renderer.js"]);
const unexpected = [];

function walk(dir) {
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    const fullPath = path.join(dir, entry.name);
    if (entry.isDirectory()) {
      walk(fullPath);
      continue;
    }
    if (!entry.isFile()) {
      unexpected.push(path.relative(rendererDir, fullPath));
      continue;
    }
    const rel = path.relative(rendererDir, fullPath).split(path.sep).join("/");
    if (!allowed.has(rel)) {
      unexpected.push(rel);
    }
  }
}

walk(rendererDir);
if (unexpected.length > 0) {
  console.error("check-bundled-renderer.sh: bundled renderer source contains unexpected files:");
  for (const file of unexpected) {
    console.error(file);
  }
  process.exit(1);
}
NODE

node - "$entry" <<'NODE'
const fs = require("node:fs");
const entry = process.argv[2];
const html = fs.readFileSync(entry, "utf8");
const metaMatch = html.match(/<meta\s+[^>]*http-equiv=["']Content-Security-Policy["'][^>]*>/i);

function fail(message) {
  console.error(`check-bundled-renderer.sh: ${message}`);
  process.exit(1);
}

if (!metaMatch) {
  fail("index.html must declare a Content-Security-Policy");
}
const contentMatch =
  metaMatch[0].match(/\bcontent="([^"]+)"/i) ||
  metaMatch[0].match(/\bcontent='([^']+)'/i);
if (!contentMatch) {
  fail("Content-Security-Policy meta tag must include a content attribute");
}

const expected = new Map([
  ["default-src", "'self'"],
  ["script-src", "'self'"],
  ["style-src", "'self'"],
  ["img-src", "'self' data:"],
  ["connect-src", "http://127.0.0.1:*"],
  ["object-src", "'none'"],
  ["base-uri", "'none'"],
  ["form-action", "'none'"],
  ["frame-ancestors", "'none'"],
]);

const actual = new Map();
for (const directive of contentMatch[1].split(";")) {
  const trimmed = directive.trim().replace(/\s+/g, " ");
  if (!trimmed) continue;
  const [name, ...rest] = trimmed.split(" ");
  if (actual.has(name)) {
    fail(`CSP directive is duplicated: ${name}`);
  }
  actual.set(name, rest.join(" "));
}

for (const [name, value] of expected) {
  if (actual.get(name) !== value) {
    fail(`CSP directive ${name} mismatch: got '${actual.get(name) || ""}', want '${value}'`);
  }
}
for (const name of actual.keys()) {
  if (!expected.has(name)) {
    fail(`CSP contains unexpected directive: ${name}`);
  }
}
NODE

if ! grep -Fq 'href="./styles.css"' "$entry"; then
  echo "check-bundled-renderer.sh: index.html must reference ./styles.css" >&2
  exit 1
fi

if ! grep -Fq 'src="./renderer.js"' "$entry"; then
  echo "check-bundled-renderer.sh: index.html must reference ./renderer.js" >&2
  exit 1
fi

if grep -R -E 'WORKMAX_LOCAL_TOKEN|refresh_token|access_token|Authorization: Bearer' "$renderer_dir" >/dev/null; then
  echo "check-bundled-renderer.sh: bundled renderer source must not embed token-like secrets" >&2
  exit 1
fi

WORKMAX_BUNDLED_RENDERER_DIR="$renderer_dir" node "$SCRIPT_DIR/check-bundled-renderer-behavior.mjs"

echo "ok bundled renderer source: $renderer_dir"
