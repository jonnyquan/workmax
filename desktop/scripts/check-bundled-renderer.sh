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

node - "$renderer_dir" "$REPO_ROOT/desktop/renderer/embed.go" \
  "$REPO_ROOT/desktop/scripts/build-mac.sh" "$REPO_ROOT/desktop/scripts/inspect-mac-package.sh" <<'NODE'
const fs = require("node:fs");
const path = require("node:path");
const [rendererDir, embedPath, buildMacPath, inspectPath] = process.argv.slice(2);
// The bundled renderer is an allowlist, not a directory copy: an unexpected
// file here would be shipped and served to the webview. shim.js and its
// generated bridge library are the Wails shell's replacement for the Electron
// preload — see desktop/renderer/en/desktop/shim.js.
//
// renderer.js is the ES module entry; the eight files after it are the modules
// it imports. They are listed one by one rather than globbed for the same
// reason as before — a directory copy would ship whatever happened to be in
// the directory — but the consequence of forgetting one has changed: a module
// that is imported and not shipped is a 404 during link, which is a blank
// window, not a missing button.
const allowed = new Set([
  "index.html",
  "styles.css",
  "renderer.js",
  "dom.js",
  "fence.js",
  "protocol.js",
  "events.js",
  "markdown.js",
  "transcript.js",
  "composer.js",
  "threads.js",
  "context-panel.js",
  "shim.js",
  "lib/desktop-bridge.js",
]);
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

// The same files are named in four places (this allowlist, embed.go,
// build-mac.sh, inspect-mac-package.sh) and all four are now checked against
// each other. embed.go decides what is compiled into the binary and therefore
// what the app actually runs; the two packaging scripts decide what a build
// refuses to ship and what an inspected .app is allowed to contain. A file
// added to one and forgotten in another used to be a shipped 404 discovered by
// a user. Four hand-maintained copies of a fourteen-entry list is not a thing
// to leave to diligence.
function compare(label, other) {
  const missingThere = [...allowed].filter((file) => !other.has(file));
  const missingHere = [...other].filter((file) => !allowed.has(file));
  if (missingThere.length === 0 && missingHere.length === 0) return;
  console.error(`check-bundled-renderer.sh: renderer allowlist and ${label} disagree:`);
  for (const file of missingThere) console.error(`  allowed but absent there: ${file}`);
  for (const file of missingHere) console.error(`  present there but not allowed: ${file}`);
  process.exit(1);
}

const embedSource = fs.readFileSync(embedPath, "utf8");
compare(
  "desktop/renderer/embed.go",
  new Set([...embedSource.matchAll(/^\/\/go:embed\s+en\/desktop\/(\S+)$/gmu)].map((m) => m[1]))
);

const buildMac = fs.readFileSync(buildMacPath, "utf8");
const rendererFiles = buildMac.match(/^RENDERER_FILES=\(([^)]*)\)/mu);
if (!rendererFiles) {
  console.error("check-bundled-renderer.sh: could not read RENDERER_FILES from build-mac.sh");
  process.exit(1);
}
compare("desktop/scripts/build-mac.sh (RENDERER_FILES)", new Set(rendererFiles[1].trim().split(/\s+/u)));

// inspect-mac-package.sh names the list twice — once as "these must exist",
// once as a case arm of "these may exist" — and both are read here, because a
// file present in one and not the other is exactly the half-updated state this
// check exists to catch.
const inspect = fs.readFileSync(inspectPath, "utf8");
const required = inspect.match(/^\s*for required in (.+); do$/mu);
const caseArm = inspect.match(/^\s*([\w./|-]*index\.html[\w./|-]*)\)\s*;;$/mu);
if (!required || !caseArm) {
  console.error("check-bundled-renderer.sh: could not read the renderer lists from inspect-mac-package.sh");
  process.exit(1);
}
compare("desktop/scripts/inspect-mac-package.sh (required)", new Set(required[1].trim().split(/\s+/u)));
compare("desktop/scripts/inspect-mac-package.sh (case arm)", new Set(caseArm[1].split("|")));
NODE

# The Content-Security-Policy has exactly one source of truth: the response
# header in desktop/wails/uiserver.go, set by the only server that ever serves
# these files.
#
# There used to be a second one — a <meta http-equiv> in index.html, pinned
# here — and the two had already drifted: the meta allowed
# connect-src http://127.0.0.1:*, the header allows 'self'; the meta forbade
# blob: images, the header permits them. What the webview enforced was the
# intersection, which is to say a policy neither file states. So the meta is
# gone, this check makes sure it stays gone, and the directive pinning moved to
# the file that actually carries the policy.
# Overridable for the same reason WORKMAX_BUNDLED_RENDERER_DIR is: so
# check-bundled-renderer.test.sh can point the check at a deliberately broken
# copy and prove it still fires. Nothing in the build path sets it.
ui_server="${WORKMAX_UI_SERVER_SOURCE:-$REPO_ROOT/desktop/wails/uiserver.go}"
node - "$entry" "$ui_server" <<'NODE'
const fs = require("node:fs");
const [entry, uiServerPath] = process.argv.slice(2);

function fail(message) {
  console.error(`check-bundled-renderer.sh: ${message}`);
  process.exit(1);
}

const html = fs.readFileSync(entry, "utf8");
if (/<meta\s+[^>]*http-equiv=["']Content-Security-Policy["']/iu.test(html)) {
  fail(
    "index.html must NOT declare a Content-Security-Policy meta tag: the policy " +
      "is the response header in desktop/wails/uiserver.go, and a second copy here " +
      "is a policy nobody reads as a whole"
  );
}

const uiServer = fs.readFileSync(uiServerPath, "utf8");
const policyMatch = uiServer.match(
  /const contentSecurityPolicy = ((?:"[^"]*"(?:\s*\+\s*)?)+)/u
);
if (!policyMatch) {
  fail(`could not read contentSecurityPolicy from ${uiServerPath}`);
}
const policy = [...policyMatch[1].matchAll(/"([^"]*)"/gu)].map((m) => m[1]).join("");

const expected = new Map([
  ["default-src", "'none'"],
  ["script-src", "'self'"],
  ["style-src", "'self'"],
  ["img-src", "'self' data: blob:"],
  ["font-src", "'self'"],
  ["connect-src", "'self'"],
  ["media-src", "'self' blob:"],
  ["form-action", "'none'"],
  ["frame-ancestors", "'none'"],
  ["frame-src", "'none'"],
  ["object-src", "'none'"],
  ["base-uri", "'none'"],
]);

const actual = new Map();
for (const directive of policy.split(";")) {
  const trimmed = directive.trim().replace(/\s+/gu, " ");
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

# type="module" is load-bearing, not stylistic: renderer.js imports the other
# eight files, and a classic <script> would fail on the first import statement.
# It is also what defers the renderer past the two classic bridge scripts, so
# window.desktopBridge exists before its first line runs.
if ! grep -Fq '<script type="module" src="./renderer.js"></script>' "$entry"; then
  echo "check-bundled-renderer.sh: index.html must load ./renderer.js as a module" >&2
  exit 1
fi

# The bridge scripts must stay classic. Making either one a module would defer
# it alongside the renderer, and the order the renderer depends on would then
# be whatever the parser happened to do.
for classic in "./lib/desktop-bridge.js" "./shim.js"; do
  if ! grep -Fq "<script src=\"$classic\"></script>" "$entry"; then
    echo "check-bundled-renderer.sh: index.html must load $classic as a classic script" >&2
    exit 1
  fi
done

if grep -R -E 'WORKMAX_LOCAL_TOKEN|refresh_token|access_token|Authorization: Bearer' "$renderer_dir" >/dev/null; then
  echo "check-bundled-renderer.sh: bundled renderer source must not embed token-like secrets" >&2
  exit 1
fi

WORKMAX_BUNDLED_RENDERER_DIR="$renderer_dir" node "$SCRIPT_DIR/check-bundled-renderer-behavior.mjs"

echo "ok bundled renderer source: $renderer_dir"
