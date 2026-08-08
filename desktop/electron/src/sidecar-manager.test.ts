import assert from "node:assert/strict";
import { chmodSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { setTimeout as delay } from "node:timers/promises";
import { describe, it } from "node:test";

import { parseSidecarHandshakeLine, SidecarManager } from "./sidecar-manager";

const VALID_HANDSHAKE = {
  v: 1,
  port: 49152,
  pid: 12345,
  started_at: "2026-05-20T10:00:00Z",
  sidecar_version: "0.1.0-p1-ea",
};

describe("parseSidecarHandshakeLine", () => {
  it("parses the supported handshake shape", () => {
    assert.deepEqual(
      parseSidecarHandshakeLine(JSON.stringify(VALID_HANDSHAKE)),
      VALID_HANDSHAKE
    );
  });

  it("rejects malformed JSON and unsupported protocol versions", () => {
    assert.throws(() => parseSidecarHandshakeLine("not json"), /malformed handshake JSON/);
    assert.throws(
      () => parseSidecarHandshakeLine(JSON.stringify({ ...VALID_HANDSHAKE, v: 2 })),
      /unsupported handshake version/
    );
  });

  it("rejects invalid port, pid, timestamp, and version fields", () => {
    for (const port of [0, 65536, 1.2, "49152"]) {
      assert.throws(
        () => parseSidecarHandshakeLine(JSON.stringify({ ...VALID_HANDSHAKE, port })),
        /invalid port/
      );
    }
    assert.throws(
      () => parseSidecarHandshakeLine(JSON.stringify({ ...VALID_HANDSHAKE, pid: 0 })),
      /invalid pid/
    );
    assert.throws(
      () => parseSidecarHandshakeLine(JSON.stringify({ ...VALID_HANDSHAKE, started_at: "" })),
      /invalid started_at/
    );
    assert.throws(
      () => parseSidecarHandshakeLine(JSON.stringify({ ...VALID_HANDSHAKE, started_at: "not-a-date" })),
      /invalid started_at/
    );
    assert.throws(
      () => parseSidecarHandshakeLine(JSON.stringify({ ...VALID_HANDSHAKE, sidecar_version: "" })),
      /invalid sidecar_version/
    );
  });

  it("redacts token-like content from malformed handshake errors", () => {
    assert.throws(
      () =>
        parseSidecarHandshakeLine(
          'not json Authorization: Bearer access-secret X-Local-Token=local-secret https://user:pass@example.com/path?refresh_token=refresh-secret'
        ),
      (err: unknown) => {
        assert.ok(err instanceof Error);
        assert.match(err.message, /malformed handshake JSON/);
        assert.match(err.message, /Bearer \[REDACTED\]/);
        assert.match(err.message, /X-Local-Token=\[REDACTED\]/);
        assert.match(err.message, /https:\/\/\[REDACTED\]@example.com\/path/);
        assert.match(err.message, /refresh_token=\[REDACTED\]/);
        assert.doesNotMatch(err.message, /access-secret|local-secret|user:pass|refresh-secret/);
        return true;
      }
    );
  });
});

describe("SidecarManager boot failures", () => {
  it("kills a child that emits an invalid handshake", async () => {
    const dir = mkdtempSync(join(tmpdir(), "workmax-sidecar-manager-"));
    const pidFile = join(dir, "child.pid");
    const scriptPath = join(dir, "bad-handshake.js");
    writeFileSync(
      scriptPath,
      [
        "#!/usr/bin/env node",
        "const fs = require('node:fs');",
        `fs.writeFileSync(${JSON.stringify(pidFile)}, String(process.pid));`,
        "process.stdout.write('not json\\n');",
        "setInterval(() => {}, 1000);",
        "",
      ].join("\n")
    );
    chmodSync(scriptPath, 0o755);

    const manager = new SidecarManager({
      binaryPath: scriptPath,
      dataDir: dir,
      handshakeTimeoutMs: 3_000,
      onStderr: () => {},
    });

    let pid = 0;
    try {
      await assert.rejects(() => manager.start(), /malformed handshake JSON/);
      pid = Number(readFileSync(pidFile, "utf8"));
      assert.equal(manager.runtime, null);
      await waitUntil(() => !isProcessAlive(pid), 3_000);
    } finally {
      if (pid > 0 && isProcessAlive(pid)) {
        process.kill(pid, "SIGKILL");
      }
    }
  });
});

describe("SidecarManager lifecycle", () => {
  it("sends the shutdown command on stdin before waiting for child exit", async () => {
    const dir = mkdtempSync(join(tmpdir(), "workmax-sidecar-manager-"));
    const markerFile = join(dir, "shutdown.marker");
    const scriptPath = join(dir, "graceful-child.js");
    writeFileSync(
      scriptPath,
      [
        "#!/usr/bin/env node",
        "const fs = require('node:fs');",
        "const readline = require('node:readline');",
        `process.stdout.write(JSON.stringify({ ...${JSON.stringify(VALID_HANDSHAKE)}, pid: process.pid }) + '\\n');`,
        "readline.createInterface({ input: process.stdin }).on('line', (line) => {",
        "  if (line.trim() === 'shutdown') {",
        `    fs.writeFileSync(${JSON.stringify(markerFile)}, 'shutdown');`,
        "    process.exit(0);",
        "  }",
        "});",
        "setInterval(() => {}, 1000);",
        "",
      ].join("\n")
    );
    chmodSync(scriptPath, 0o755);

    const manager = new SidecarManager({
      binaryPath: scriptPath,
      dataDir: dir,
      handshakeTimeoutMs: 3_000,
      onStderr: () => {},
    });

    const runtime = await manager.start();
    assert.equal(manager.runtime?.pid, runtime.pid);

    await manager.shutdown();

    assert.equal(readFileSync(markerFile, "utf8"), "shutdown");
    await waitUntil(() => !isProcessAlive(runtime.pid), 3_000);
    assert.equal(manager.runtime, null);
  });

  it("does not restart after an unexpected post-handshake exit", async () => {
    const dir = mkdtempSync(join(tmpdir(), "workmax-sidecar-manager-"));
    const scriptPath = join(dir, "exits-after-handshake.js");
    writeFileSync(
      scriptPath,
      [
        "#!/usr/bin/env node",
        `process.stdout.write(JSON.stringify({ ...${JSON.stringify(VALID_HANDSHAKE)}, pid: process.pid }) + '\\n');`,
        "setTimeout(() => process.exit(7), 25);",
        "",
      ].join("\n")
    );
    chmodSync(scriptPath, 0o755);

    let unexpectedExit: { code: number | null; signal: NodeJS.Signals | null } | null = null;
    const manager = new SidecarManager({
      binaryPath: scriptPath,
      dataDir: dir,
      handshakeTimeoutMs: 3_000,
      onStderr: () => {},
      onUnexpectedExit: (code, signal) => {
        unexpectedExit = { code, signal };
      },
    });

    await manager.start();
    await waitUntil(() => unexpectedExit !== null, 3_000);

    assert.deepEqual(unexpectedExit, { code: 7, signal: null });
    assert.equal(manager.runtime, null);
  });
});

async function waitUntil(predicate: () => boolean, timeoutMs: number): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (predicate()) return;
    await delay(25);
  }
  assert.equal(predicate(), true);
}

function isProcessAlive(pid: number): boolean {
  try {
    process.kill(pid, 0);
    return true;
  } catch {
    return false;
  }
}
