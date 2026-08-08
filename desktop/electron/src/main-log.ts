// main-log.ts
//
// Append-only structured logger for the Electron main process.
// Complements the bundled Desktop renderer logger, which POSTs to the
// sidecar's /system/log.
//
// Why a separate logger for main:
//   - Main process can't reach the sidecar's HTTP endpoint reliably
//     because the most important failures (sidecar didn't start,
//     sidecar died, system-browser login transaction failed to start)
//     happen BEFORE or AFTER the sidecar is healthy.
//   - Sidecar stderr and SidecarManager lifecycle diagnostics are
//     visible in dev terminals, but packaged users need the same
//     lines in a predictable support file.
//   - In a packaged Electron build there's no terminal; console.*
//     in main goes to syslog (macOS Console.app) which most users
//     can't navigate. A predictable file path the user can attach
//     to a support email is the goal.
//
// File location: <dataDir>/logs/sidecar-main.log, where dataDir
// matches WORKMAX_DESKTOP_DATA_DIR (or ~/.workmax by default — same
// resolution as the Go sidecar's ResolveDataDir).
//
// Rotation: minimal. On startup, if the existing file is >5MB,
// rotate to .log.1 (overwriting any prior .1). Keeps state
// trivially bounded without pulling in a Node lumberjack package.
// Renderer-log's lumberjack covers the high-volume case; this
// file should grow slowly.

import { appendFileSync, existsSync, mkdirSync, renameSync, statSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";

import { redactSidecarLogLine } from "./security-helpers";

const ROTATION_THRESHOLD_BYTES = 5 * 1024 * 1024;
const LOG_FILENAME = "sidecar-main.log";
const ROTATED_FILENAME = "sidecar-main.log.1";
const SENSITIVE_EXTRA_KEY_RE = /^(authorization|x-local-token|workmax_local_token|access_token|refresh_token|id_token|token|api[_-]?key|apikey|client_secret|password|secret)$/i;

type Level = "error" | "warn" | "info";

let logFilePath: string | null = null;
let initialized = false;

function resolveDataDir(): string {
  return process.env.WORKMAX_DESKTOP_DATA_DIR ?? join(homedir(), ".workmax");
}

function ensureInitialized(): string | null {
  if (initialized) return logFilePath;
  initialized = true;

  try {
    const dir = join(resolveDataDir(), "logs");
    mkdirSync(dir, { recursive: true });
    const path = join(dir, LOG_FILENAME);

    // One-shot rotation on init. If we miss the threshold (process
    // crashed before the next init), the next startup catches it.
    if (existsSync(path)) {
      try {
        const stats = statSync(path);
        if (stats.size > ROTATION_THRESHOLD_BYTES) {
          renameSync(path, join(dir, ROTATED_FILENAME));
        }
      } catch {
        // Best-effort. If rotation fails (permissions, race), we
        // continue appending to the current file.
      }
    }

    logFilePath = path;
    return path;
  } catch (err) {
    // If we can't even create the log directory, fall back to
    // console-only mode. logFilePath stays null and the helpers
    // below short-circuit to console.
    // eslint-disable-next-line no-console
    console.error("[main-log] init failed; falling back to console:", err);
    logFilePath = null;
    return null;
  }
}

function write(level: Level, message: string, extra?: Record<string, unknown>): void {
  const safeMessage = redactSidecarLogLine(message);
  const safeExtra = extra ? redactLogValue(extra) as Record<string, unknown> : undefined;
  const line = JSON.stringify({
    time: new Date().toISOString(),
    level,
    message: safeMessage,
    ...(safeExtra ? { extra: safeExtra } : {}),
  });

  // Always emit to console (developers running `npm run dev` see it;
  // packaged users don't, but the file path below catches them).
  const consoleArgs: unknown[] = [`[main]`, safeMessage];
  if (safeExtra) consoleArgs.push(safeExtra);
  if (level === "error") {
    // eslint-disable-next-line no-console
    console.error(...consoleArgs);
  } else if (level === "warn") {
    // eslint-disable-next-line no-console
    console.warn(...consoleArgs);
  } else {
    // eslint-disable-next-line no-console
    console.log(...consoleArgs);
  }

  const path = ensureInitialized();
  if (!path) return;
  try {
    appendFileSync(path, line + "\n");
  } catch (err) {
    // Disk full / permission denied / etc. — log to console only;
    // do NOT throw, the caller cannot do anything useful with it.
    // eslint-disable-next-line no-console
    console.error("[main-log] append failed:", err);
  }
}

function redactLogValue(value: unknown, key?: string): unknown {
  if (key && SENSITIVE_EXTRA_KEY_RE.test(key)) {
    return "[REDACTED]";
  }
  if (typeof value === "string") {
    return redactSidecarLogLine(value);
  }
  if (Array.isArray(value)) {
    return value.map((item) => redactLogValue(item));
  }
  if (value && typeof value === "object") {
    const redacted: Record<string, unknown> = {};
    for (const [key, child] of Object.entries(value)) {
      redacted[redactSidecarLogLine(key)] = redactLogValue(child, key);
    }
    return redacted;
  }
  return value;
}

export const mainLog = {
  error(message: string, extra?: Record<string, unknown>): void {
    write("error", message, extra);
  },
  warn(message: string, extra?: Record<string, unknown>): void {
    write("warn", message, extra);
  },
  info(message: string, extra?: Record<string, unknown>): void {
    write("info", message, extra);
  },

  /** Returns the absolute log path once initialized, or null if init failed. */
  logFilePath(): string | null {
    ensureInitialized();
    return logFilePath;
  },
};
