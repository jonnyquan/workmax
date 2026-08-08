import assert from "node:assert/strict";
import { mkdtempSync, readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, describe, it } from "node:test";

const originalEnv = { ...process.env };
const originalConsole = {
  error: console.error,
  log: console.log,
  warn: console.warn,
};

afterEach(() => {
  process.env = { ...originalEnv };
  console.error = originalConsole.error;
  console.log = originalConsole.log;
  console.warn = originalConsole.warn;
  deleteMainLogFromRequireCache();
});

describe("mainLog", () => {
  it("redacts token-like values and URL credentials in structured logs", () => {
    const dataDir = mkdtempSync(join(tmpdir(), "workmax-main-log-"));
    process.env = { ...originalEnv, WORKMAX_DESKTOP_DATA_DIR: dataDir };
    console.warn = () => {};

    const { mainLog } = require("./main-log") as typeof import("./main-log");
    mainLog.warn(
      "blocked https://user:pass@example.com/path Authorization: Bearer message-token Authorization: Basic basic-message-token client_secret=message-client-secret",
      {
        targetURL: "https://user:pass@example.com/path?access_token=query-token",
        apiKey: "plain-api-secret",
        apikey: "plain-compact-api-secret",
        client_secret: "plain-client-secret",
        password: "plain-password-secret",
        token: "plain-token-secret",
        nested: {
          "https://client:secret@example.org/callback": "key leak",
          Authorization: "Basic plain-header-secret",
          headers: [
            "X-Local-Token: local-secret",
            "refresh_token=refresh-secret",
          ],
        },
      }
    );

    const logFile = join(dataDir, "logs", "sidecar-main.log");
    const entry = JSON.parse(readFileSync(logFile, "utf8").trim()) as {
      message: string;
      extra: Record<string, unknown>;
    };
    const serialized = JSON.stringify(entry);

    assert.match(entry.message, /https:\/\/\[REDACTED\]@example\.com\/path/);
    assert.doesNotMatch(serialized, /user:pass|client:secret|message-token|basic-message-token|message-client-secret|query-token|plain-api-secret|plain-compact-api-secret|plain-client-secret|plain-password-secret|plain-token-secret|plain-header-secret|local-secret|refresh-secret/);
    assert.match(serialized, /\[REDACTED\]/);
  });
});

function deleteMainLogFromRequireCache() {
  const mainLogPath = require.resolve("./main-log");
  delete require.cache[mainLogPath];
}
