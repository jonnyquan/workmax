import assert from "node:assert/strict";
import { mkdirSync, mkdtempSync, writeFileSync } from "node:fs";
import { createServer } from "node:http";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, describe, it } from "node:test";

import {
  SmokeLocalTokenRedaction,
  SmokeSensitiveKeyRedaction,
  SmokeSensitiveRedaction,
  redactSmokeArtifactSecrets,
  redactSmokeLocalToken,
  runSmokeDiagnosticsCheck,
  runSmokeLocalTokenRejectionChecks,
  smokeTokenFingerprint,
} from "./smoke-diagnostics";

const originalFetch = globalThis.fetch;

afterEach(() => {
  globalThis.fetch = originalFetch;
});

describe("smoke diagnostics helpers", () => {
  it("checks local-token rejection across representative sidecar routes", async () => {
    const seenRequests: Array<{
      method: string | undefined;
      url: string | undefined;
      rawHeaders: string[];
    }> = [];
    const server = createServer((req, res) => {
      seenRequests.push({
        method: req.method,
        url: req.url,
        rawHeaders: req.rawHeaders,
      });
      req.resume();
      res.writeHead(403, { "Content-Type": "application/json" });
      res.end(`{"error":"forbidden"}`);
    });
    await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
    const address = server.address();
    assert(address && typeof address === "object");

    try {
      const checks = await runSmokeLocalTokenRejectionChecks({
        port: address.port,
        token: "local-token",
      });

      assert.deepEqual(
        checks.map((check) => [check.name, check.status]),
        [
          ["missing", 403],
          ["wrong", 403],
          ["duplicate", 403],
          ["auth-status-missing", 403],
          ["diagnostics-wrong", 403],
          ["threads-missing", 403],
          ["renderer-log-wrong", 403],
          ["trigger-sync-missing", 403],
        ]
      );
      assert.deepEqual(
        seenRequests.map((req) => `${req.method} ${req.url}`),
        [
          "GET /health",
          "GET /health",
          "GET /health",
          "GET /auth/status",
          "GET /system/diagnostics",
          "GET /agent/threads?include_paused=false",
          "POST /system/log",
          "POST /system/trigger-sync",
        ]
      );
      const duplicateHealth = seenRequests[2];
      assert(duplicateHealth);
      assert.equal(
        duplicateHealth.rawHeaders.filter((header) => header === "X-Local-Token").length,
        2
      );
    } finally {
      await new Promise<void>((resolve, reject) => {
        server.close((err) => err ? reject(err) : resolve());
      });
    }
  });

  it("maps sidecar diagnostics into the packaged smoke assertion shape", async () => {
    const dataDir = mkdtempSync(join(tmpdir(), "workmax-smoke-diagnostics-"));
    const dbPath = join(dataDir, "workagent.db");
    const backupDir = join(dataDir, "backups");
    const backupPath = join(backupDir, "workagent-20260521.db");
    mkdirSync(backupDir);
    writeFileSync(dbPath, "sqlite fixture");
    writeFileSync(backupPath, "sqlite backup fixture");

    const seenRequests: Array<{ url: string; init?: RequestInit }> = [];
    globalThis.fetch = (async (url: string | URL | Request, init?: RequestInit) => {
      seenRequests.push({ url: String(url), init });
      return {
        ok: true,
        status: 200,
        json: async () => ({
          sidecar: {
            version: "0.1.0-p1-ea",
            integrity_check: "ok",
            data_dir: dataDir,
            db_path: dbPath,
            backup_path: backupPath,
            applied_migrations: ["0001", "", "0002", 3],
            heap_alloc_bytes: 1024,
            heap_sys_bytes: 2048,
            num_goroutine: 7,
          },
        }),
      } as Response;
    }) as typeof fetch;

    const result = await runSmokeDiagnosticsCheck({ port: 49152, token: "local-token" });

    assert.equal(seenRequests.length, 1);
    assert.equal(
      seenRequests[0]?.url,
      "http://127.0.0.1:49152/system/diagnostics"
    );
    assert.deepEqual(seenRequests[0]?.init?.headers, {
      "X-Local-Token": "local-token",
      Accept: "application/json",
    });
    assert.deepEqual(result, {
      status: 200,
      version: "0.1.0-p1-ea",
      integrityCheck: "ok",
      dataDir,
      dbPath,
      backupPath,
      dataDirReadable: true,
      dbPathReadable: true,
      backupPathReadable: true,
      appliedMigrations: ["0001", "0002"],
      heapAllocBytes: 1024,
      heapSysBytes: 2048,
      numGoroutine: 7,
    });
  });

  it("preserves diagnostics HTTP status when the response body is not JSON", async () => {
    globalThis.fetch = (async () => ({
      ok: false,
      status: 500,
      json: async () => {
        throw new Error("invalid json");
      },
    }) as unknown as Response) as typeof fetch;

    const result = await runSmokeDiagnosticsCheck({ port: 49152, token: "local-token" });

    assert.deepEqual(result, {
      status: 500,
      version: null,
      integrityCheck: null,
      dataDir: null,
      dbPath: null,
      backupPath: null,
      dataDirReadable: false,
      dbPathReadable: false,
      backupPathReadable: false,
      appliedMigrations: [],
      heapAllocBytes: null,
      heapSysBytes: null,
      numGoroutine: null,
    });
  });

  it("uses a stable short token fingerprint without exposing the token", () => {
    const token = "local-token-secret";
    const fingerprint = smokeTokenFingerprint(token);

    assert.equal(fingerprint.length, 16);
    assert.match(fingerprint, /^[0-9a-f]+$/);
    assert.doesNotMatch(fingerprint, /local-token-secret/);
    assert.equal(fingerprint, smokeTokenFingerprint(token));
  });

  it("redacts the raw local token from nested smoke artifacts", () => {
    const token = "local-token-secret";
    const result = redactSmokeLocalToken(
      {
        bodyText: `before ${token} after ${token}`,
        [`header-${token}`]: "key leak",
        nested: [{ value: token }, { value: "safe" }],
        count: 2,
      },
      token
    );

    assert.equal(result.redacted, true);
    assert.deepEqual(result.value, {
      bodyText: `before ${SmokeLocalTokenRedaction} after ${SmokeLocalTokenRedaction}`,
      [`header-${SmokeLocalTokenRedaction}`]: "key leak",
      nested: [{ value: SmokeLocalTokenRedaction }, { value: "safe" }],
      count: 2,
    });
    assert.doesNotMatch(JSON.stringify(result.value), /local-token-secret/);
  });

  it("redacts token-like strings from smoke artifacts", () => {
    const result = redactSmokeArtifactSecrets(
      {
        bodyText:
          "Authorization: Bearer bearer-secret https://user:pass@example.com/path access_token=query-secret token=plain-token apikey=compact-secret secret=generic-secret",
        context: {
          client_secret: "json-secret",
          apikey: "compact-json-secret",
          secret: "generic-json-secret",
          "api_key=key-secret": "key leak",
          "secret=key-generic-secret": "key leak",
          nested: ["Basic basic-secret"],
        },
      },
      "local-token-secret"
    );

    assert.equal(result.localTokenRedacted, false);
    assert.equal(result.sensitiveRedacted, true);
    assert.deepEqual(result.value, {
      bodyText:
        "Authorization: Bearer [REDACTED] https://[REDACTED]@example.com/path access_token=[REDACTED] token=[REDACTED] apikey=[REDACTED] secret=[REDACTED]",
      context: {
        [SmokeSensitiveKeyRedaction]: SmokeSensitiveRedaction,
        "api_key=[REDACTED]": "key leak",
        "secret=[REDACTED]": "key leak",
        nested: ["Basic [REDACTED]"],
      },
    });
    assert.doesNotMatch(
      JSON.stringify(result.value),
      /bearer-secret|user:pass|query-secret|plain-token|compact-secret|generic-secret|json-secret|compact-json-secret|generic-json-secret|key-secret|key-generic-secret|basic-secret/
    );
  });

  it("leaves smoke artifacts unchanged when the local token is absent", () => {
    const artifact = { bodyText: "no secrets here", nested: ["safe"] };
    const result = redactSmokeLocalToken(artifact, "local-token-secret");

    assert.equal(result.redacted, false);
    assert.deepEqual(result.value, artifact);
  });
});
