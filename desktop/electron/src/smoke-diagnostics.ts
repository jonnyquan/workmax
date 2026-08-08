import { createHash } from "node:crypto";
import { statSync } from "node:fs";
import { connect } from "node:net";

export interface SmokeSidecarRuntime {
  port: number;
  token: string;
}

export interface SmokeDiagnosticsCheck {
  status: number;
  version: string | null;
  integrityCheck: string | null;
  dataDir: string | null;
  dbPath: string | null;
  backupPath: string | null;
  dataDirReadable: boolean;
  dbPathReadable: boolean;
  backupPathReadable: boolean;
  appliedMigrations: string[];
  heapAllocBytes: number | null;
  heapSysBytes: number | null;
  numGoroutine: number | null;
}

export const SmokeLocalTokenRedaction = "[REDACTED_LOCAL_TOKEN]";
export const SmokeSensitiveRedaction = "[REDACTED_SMOKE_SECRET]";
export const SmokeSensitiveKeyRedaction = "[REDACTED_SMOKE_SECRET_KEY]";

const smokeURLCredentialsRE = /(https?:\/\/)[^/\s:@]+(?::[^/\s@]*)?@/gi;
const smokeAuthorizationRE = /(Authorization:\s*(?:Bearer|Basic)\s+)\S+/gi;
const smokeBearerRE = /\bBearer\s+[A-Za-z0-9._\-]+/gi;
const smokeBasicRE = /\bBasic\s+[A-Za-z0-9+/=._\-]+/gi;
const smokeTokenPairRE =
  /((?:(?:access|refresh|id)_token|token|client_secret|password|api[_-]?key|apikey|secret)["']?\s*[:=]\s*["']?)[^"',&\s]+/gi;
const smokeJSONTokenFieldRE =
  /("(?:(?:access|refresh|id)_token|token|client_secret|password|api[_-]?key|apikey|secret)"\s*:\s*")[^",}\s]+/gi;
const smokeSensitiveKeyRE =
  /^(authorization|x-local-token|workmax_local_token|access_token|refresh_token|id_token|token|client_secret|password|api[_-]?key|apikey|secret)$/i;

export function redactSmokeLocalToken(
  value: unknown,
  token: string
): { value: unknown; redacted: boolean } {
  const result = redactSmokeArtifactSecrets(value, token);
  return { value: result.value, redacted: result.localTokenRedacted };
}

export function redactSmokeArtifactSecrets(
  value: unknown,
  token: string
): { value: unknown; localTokenRedacted: boolean; sensitiveRedacted: boolean } {
  return redactSmokeArtifactValue(value, token, "");
}

function redactSmokeArtifactValue(
  value: unknown,
  token: string,
  key: string
): { value: unknown; localTokenRedacted: boolean; sensitiveRedacted: boolean } {
  if (key && smokeSensitiveKeyRE.test(key)) {
    return {
      value: SmokeSensitiveRedaction,
      localTokenRedacted: false,
      sensitiveRedacted: true,
    };
  }
  if (!token) {
    return redactGenericSmokeValue(value, "");
  }
  if (typeof value === "string") {
    const localRedacted = value.includes(token);
    const localSafe = localRedacted
      ? value.split(token).join(SmokeLocalTokenRedaction)
      : value;
    const generic = redactGenericSmokeString(localSafe);
    return {
      value: generic.value,
      localTokenRedacted: localRedacted,
      sensitiveRedacted: generic.redacted,
    };
  }
  if (Array.isArray(value)) {
    let localTokenRedacted = false;
    let sensitiveRedacted = false;
    const next = value.map((item) => {
      const child = redactSmokeArtifactValue(item, token, "");
      localTokenRedacted ||= child.localTokenRedacted;
      sensitiveRedacted ||= child.sensitiveRedacted;
      return child.value;
    });
    return { value: next, localTokenRedacted, sensitiveRedacted };
  }
  if (value && typeof value === "object") {
    let localTokenRedacted = false;
    let sensitiveRedacted = false;
    const next: Record<string, unknown> = {};
    for (const [key, childValue] of Object.entries(value)) {
      const localKeyRedacted = key.includes(token);
      const redactedKey = localKeyRedacted
        ? key.split(token).join(SmokeLocalTokenRedaction)
        : key;
      const safeKey = smokeSensitiveKeyRE.test(redactedKey)
        ? SmokeSensitiveKeyRedaction
        : redactGenericSmokeString(redactedKey).value;
      const child = redactSmokeArtifactValue(childValue, token, key);
      localTokenRedacted ||= child.localTokenRedacted || localKeyRedacted;
      sensitiveRedacted ||= child.sensitiveRedacted || safeKey !== redactedKey;
      next[safeKey] = child.value;
    }
    return { value: next, localTokenRedacted, sensitiveRedacted };
  }
  return { value, localTokenRedacted: false, sensitiveRedacted: false };
}

function redactGenericSmokeValue(
  value: unknown,
  key: string
): { value: unknown; localTokenRedacted: boolean; sensitiveRedacted: boolean } {
  if (key && smokeSensitiveKeyRE.test(key)) {
    return {
      value: SmokeSensitiveRedaction,
      localTokenRedacted: false,
      sensitiveRedacted: true,
    };
  }
  if (typeof value === "string") {
    const generic = redactGenericSmokeString(value);
    return {
      value: generic.value,
      localTokenRedacted: false,
      sensitiveRedacted: generic.redacted,
    };
  }
  if (Array.isArray(value)) {
    let sensitiveRedacted = false;
    const next = value.map((item) => {
      const child = redactGenericSmokeValue(item, "");
      sensitiveRedacted ||= child.sensitiveRedacted;
      return child.value;
    });
    return { value: next, localTokenRedacted: false, sensitiveRedacted };
  }
  if (value && typeof value === "object") {
    let sensitiveRedacted = false;
    const next: Record<string, unknown> = {};
    for (const [key, childValue] of Object.entries(value)) {
      const safeKey = smokeSensitiveKeyRE.test(key)
        ? SmokeSensitiveKeyRedaction
        : redactGenericSmokeString(key).value;
      const child = redactGenericSmokeValue(childValue, key);
      sensitiveRedacted ||= child.sensitiveRedacted || safeKey !== key;
      next[safeKey] = child.value;
    }
    return { value: next, localTokenRedacted: false, sensitiveRedacted };
  }
  return { value, localTokenRedacted: false, sensitiveRedacted: false };
}

function redactGenericSmokeString(value: string): { value: string; redacted: boolean } {
  let next = value;
  next = next.replace(smokeURLCredentialsRE, "$1[REDACTED]@");
  next = next.replace(smokeAuthorizationRE, "$1[REDACTED]");
  next = next.replace(smokeBearerRE, "Bearer [REDACTED]");
  next = next.replace(smokeBasicRE, "Basic [REDACTED]");
  next = next.replace(smokeTokenPairRE, "$1[REDACTED]");
  next = next.replace(smokeJSONTokenFieldRE, "$1[REDACTED]");
  return { value: next, redacted: next !== value };
}

export async function runSmokeLocalTokenRejectionChecks(
  runtime: SmokeSidecarRuntime
): Promise<Array<{ name: string; status: number }>> {
  return [
    {
      name: "missing",
      status: await rawRequestStatus(runtime.port, { path: "/health" }),
    },
    {
      name: "wrong",
      status: await rawRequestStatus(runtime.port, {
        path: "/health",
        headers: [["X-Local-Token", "smoke-wrong-token"]],
      }),
    },
    {
      name: "duplicate",
      status: await rawRequestStatus(runtime.port, {
        path: "/health",
        headers: [
          ["X-Local-Token", runtime.token],
          ["X-Local-Token", "smoke-wrong-token"],
        ],
      }),
    },
    {
      name: "auth-status-missing",
      status: await rawRequestStatus(runtime.port, { path: "/auth/status" }),
    },
    {
      name: "diagnostics-wrong",
      status: await rawRequestStatus(runtime.port, {
        path: "/system/diagnostics",
        headers: [["X-Local-Token", "smoke-wrong-token"]],
      }),
    },
    {
      name: "threads-missing",
      status: await rawRequestStatus(runtime.port, {
        path: "/agent/threads?include_paused=false",
      }),
    },
    {
      name: "renderer-log-wrong",
      status: await rawRequestStatus(runtime.port, {
        method: "POST",
        path: "/system/log",
        headers: [
          ["X-Local-Token", "smoke-wrong-token"],
          ["Content-Type", "application/json"],
        ],
        body: `{"level":"error","message":"packaged smoke token rejection"}`,
      }),
    },
    {
      name: "trigger-sync-missing",
      status: await rawRequestStatus(runtime.port, {
        method: "POST",
        path: "/system/trigger-sync",
        headers: [["Content-Type", "application/json"]],
        body: "{}",
      }),
    },
  ];
}

export async function runSmokeDiagnosticsCheck(
  runtime: SmokeSidecarRuntime
): Promise<SmokeDiagnosticsCheck> {
  const response = await fetch(`http://127.0.0.1:${runtime.port}/system/diagnostics`, {
    headers: {
      "X-Local-Token": runtime.token,
      Accept: "application/json",
    },
  });
  let body: {
    sidecar?: {
      version?: unknown;
      integrity_check?: unknown;
      data_dir?: unknown;
      db_path?: unknown;
      backup_path?: unknown;
      heap_alloc_bytes?: unknown;
      heap_sys_bytes?: unknown;
      num_goroutine?: unknown;
      applied_migrations?: unknown;
    };
  } = {};
  try {
    body = await response.json() as typeof body;
  } catch {
    body = {};
  }
  const sidecar = body.sidecar ?? {};
  const dataDir = stringOrNull(sidecar.data_dir);
  const dbPath = stringOrNull(sidecar.db_path);
  const backupPath = stringOrNull(sidecar.backup_path);

  return {
    status: response.status,
    version: stringOrNull(sidecar.version),
    integrityCheck: stringOrNull(sidecar.integrity_check),
    dataDir,
    dbPath,
    backupPath,
    dataDirReadable: dataDir ? isReadableDirectory(dataDir) : false,
    dbPathReadable: dbPath ? isReadableFile(dbPath) : false,
    backupPathReadable: backupPath ? isReadableFile(backupPath) : false,
    appliedMigrations: stringArray(sidecar.applied_migrations),
    heapAllocBytes: numberOrNull(sidecar.heap_alloc_bytes),
    heapSysBytes: numberOrNull(sidecar.heap_sys_bytes),
    numGoroutine: numberOrNull(sidecar.num_goroutine),
  };
}

export function smokeTokenFingerprint(token: string): string {
  return createHash("sha256").update(token).digest("hex").slice(0, 16);
}

interface RawRequest {
  method?: "GET" | "POST";
  path: string;
  headers?: Array<[string, string]>;
  body?: string;
}

function rawRequestStatus(port: number, request: RawRequest): Promise<number> {
  return new Promise((resolveStatus, rejectStatus) => {
    const socket = connect({ host: "127.0.0.1", port });
    let response = "";
    let settled = false;
    const method = request.method ?? "GET";
    const body = request.body ?? "";

    const finish = (status?: number, err?: Error): void => {
      if (settled) {
        return;
      }
      settled = true;
      socket.destroy();
      if (err) {
        rejectStatus(err);
        return;
      }
      resolveStatus(status ?? 0);
    };

    socket.setTimeout(5000, () => {
      finish(undefined, new Error("timed out checking local-token rejection"));
    });
    socket.on("error", (err) => {
      finish(undefined, err);
    });
    socket.on("data", (chunk) => {
      response += chunk.toString("utf8");
    });
    socket.on("end", () => {
      const statusLine = response.split("\r\n", 1)[0] ?? "";
      const match = /^HTTP\/\d(?:\.\d)?\s+(\d{3})\b/.exec(statusLine);
      if (!match) {
        finish(undefined, new Error(`invalid HTTP status line: ${statusLine}`));
        return;
      }
      finish(Number(match[1]));
    });
    socket.on("connect", () => {
      const rawHeaders = (request.headers ?? [])
        .map(([name, value]) => `${name}: ${value}\r\n`)
        .join("");
      const bodyHeaders = body
        ? `Content-Length: ${Buffer.byteLength(body, "utf8")}\r\n`
        : "";
      socket.write(
        `${method} ${request.path} HTTP/1.1\r\nHost: 127.0.0.1:${port}\r\n${rawHeaders}${bodyHeaders}Connection: close\r\n\r\n${body}`
      );
    });
  });
}

function stringOrNull(value: unknown): string | null {
  return typeof value === "string" && value.length > 0 ? value : null;
}

function numberOrNull(value: unknown): number | null {
  return typeof value === "number" && Number.isFinite(value) ? value : null;
}

function stringArray(value: unknown): string[] {
  return Array.isArray(value)
    ? value.filter((item): item is string => typeof item === "string" && item.length > 0)
    : [];
}

function isReadableDirectory(path: string): boolean {
  try {
    return statSync(path).isDirectory();
  } catch {
    return false;
  }
}

function isReadableFile(path: string): boolean {
  try {
    return statSync(path).isFile();
  } catch {
    return false;
  }
}
