#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$script_dir/.." && pwd)"
cd "$repo_root"

for required_file in LICENSE THIRD_PARTY_NOTICES.md server/go.mod server/go.sum desktop/electron/package.json desktop/electron/package-lock.json; do
  if [ ! -s "$required_file" ]; then
    echo "license-audit: missing or empty required file: $required_file" >&2
    exit 1
  fi
done

if ! head -n 1 LICENSE | grep -Fq "GNU AFFERO GENERAL PUBLIC LICENSE"; then
  echo "license-audit: root LICENSE is not the canonical AGPL v3 document" >&2
  exit 1
fi

node <<'NODE'
const fs = require("node:fs");
const packageJSON = JSON.parse(fs.readFileSync("desktop/electron/package.json", "utf8"));
const lock = JSON.parse(fs.readFileSync("desktop/electron/package-lock.json", "utf8"));

if (packageJSON.license !== "AGPL-3.0-only") {
  throw new Error(`desktop package license is ${packageJSON.license || "missing"}, want AGPL-3.0-only`);
}
if (lock.packages?.[""]?.license !== packageJSON.license) {
  throw new Error("desktop package-lock root license does not match package.json");
}

// These are the reviewed license expressions currently present in the
// build-only Electron dependency tree. Any new expression requires review.
const allowed = new Set([
  "(MIT OR CC0-1.0)",
  "(WTFPL OR MIT)",
  "Apache-2.0",
  "BSD-2-Clause",
  "BSD-3-Clause",
  "BlueOak-1.0.0",
  "ISC",
  "MIT",
  "Python-2.0",
  "WTFPL",
  "WTFPL OR ISC",
]);
const failures = [];
for (const [name, metadata] of Object.entries(lock.packages ?? {})) {
  if (!name || metadata.link) continue;
  if (!metadata.license) {
    failures.push(`${name}: missing license metadata`);
  } else if (!allowed.has(metadata.license)) {
    failures.push(`${name}: unreviewed license expression ${metadata.license}`);
  }
}
if (failures.length > 0) {
  throw new Error(`Electron dependency license review failed:\n${failures.join("\n")}`);
}
console.log(`license-audit: reviewed ${Object.keys(lock.packages ?? {}).length - 1} Electron dependency entries`);
NODE

xdb_path="server/resource/ip2region/ip2region.xdb"
expected_xdb_sha="867b619b567f51bb9dd3c384a4cbf7c33e71a178aa58f13201499aadaf2cf78e"
if command -v shasum >/dev/null 2>&1; then
  actual_xdb_sha="$(shasum -a 256 "$xdb_path" | awk '{print $1}')"
elif command -v sha256sum >/dev/null 2>&1; then
  actual_xdb_sha="$(sha256sum "$xdb_path" | awk '{print $1}')"
else
  echo "license-audit: shasum or sha256sum is required" >&2
  exit 1
fi
if [ "$actual_xdb_sha" != "$expected_xdb_sha" ]; then
  echo "license-audit: ip2region database changed; verify provenance and update THIRD_PARTY_NOTICES.md" >&2
  exit 1
fi
if ! grep -Fq "$expected_xdb_sha" THIRD_PARTY_NOTICES.md; then
  echo "license-audit: ip2region database hash is missing from THIRD_PARTY_NOTICES.md" >&2
  exit 1
fi

(
  cd server
  GOFLAGS="-tags=desktop" go run github.com/google/go-licenses/v2@v2.0.1 \
    check --ignore server ./cmd/workagent-desktop ./cmd/agent-worker
)

echo "license-audit: WorkMax and distributable dependency checks passed"
