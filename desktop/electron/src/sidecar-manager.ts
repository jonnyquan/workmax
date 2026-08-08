// sidecar-manager.ts
//
// Spawns the Go sidecar binary as a child process, parses its
// single-line stdout handshake to learn the port, and exposes a
// minimal lifecycle API to the Electron main process.
//
// Design refs:
//   - sidecar-protocol.md §2 (process lifecycle)
//   - sidecar-protocol.md §3 (handshake + token)
//
// Current scope: spawn, handshake, and graceful shutdown. The Go
// sidecar itself owns the data-dir PID lock; Electron owns the app-level
// single-instance lock. Post-P1
// hardening can add restart events, exponential backoff, a circuit
// breaker, periodic /health polling, and packaged negative e2e coverage.

import { ChildProcess, spawn } from "node:child_process";
import { randomBytes } from "node:crypto";
import { createInterface, Interface } from "node:readline";
import { setTimeout as delay } from "node:timers/promises";

import { redactSidecarLogLine } from "./security-helpers";

/** Wire shape of the first line of sidecar stdout. Must stay in lockstep
 *  with server/desktop/handshake.go::Handshake. Bump V on any breaking
 *  change. */
export interface SidecarHandshake {
  v: number;
  port: number;
  pid: number;
  started_at: string;
  sidecar_version: string;
}

/** All info the renderer needs to talk to the sidecar. */
export interface SidecarRuntime {
  port: number;
  token: string;
  pid: number;
  sidecarVersion: string;
  startedAt: string;
}

export interface SidecarManagerOptions {
  /** Absolute path to the compiled `workagent-desktop` binary. */
  binaryPath: string;
  /** Per-user data dir; passed to the sidecar via WORKMAX_DESKTOP_DATA_DIR. */
  dataDir: string;
  /** Optional: invoked on sidecar stderr and manager lifecycle diagnostics. Defaults to console.error. */
  onStderr?: (line: string) => void;
  /** Optional: invoked when the sidecar exits unexpectedly. P1 EA does
   *  not auto-restart because the preload bridge captures port/token. */
  onUnexpectedExit?: (code: number | null, signal: NodeJS.Signals | null) => void;
  /** Maximum ms to wait for the handshake JSON before considering boot failed. */
  handshakeTimeoutMs?: number;
}

const DEFAULT_HANDSHAKE_TIMEOUT_MS = 5_000;
const SUPPORTED_HANDSHAKE_VERSION = 1;

/** generateLocalToken returns a 32-byte URL-safe random string used as the
 *  per-spawn X-Local-Token. New token on every spawn (and every restart). */
export function generateLocalToken(): string {
  return randomBytes(32).toString("base64url");
}

/** SidecarManager owns the lifecycle of one sidecar child process. */
export class SidecarManager {
  private readonly opts: Required<Pick<SidecarManagerOptions, "binaryPath" | "dataDir" | "handshakeTimeoutMs">>
    & SidecarManagerOptions;

  private child: ChildProcess | null = null;
  private currentRuntime: SidecarRuntime | null = null;
  private stdoutReader: Interface | null = null;
  private stderrReader: Interface | null = null;
  private shuttingDown = false;

  constructor(opts: SidecarManagerOptions) {
    this.opts = {
      handshakeTimeoutMs: DEFAULT_HANDSHAKE_TIMEOUT_MS,
      ...opts,
    };
  }

  /** Spawn the sidecar and resolve once the handshake JSON has arrived
   *  on stdout. Rejects if the binary fails to start or the handshake
   *  doesn't appear in time. */
  async start(): Promise<SidecarRuntime> {
    if (this.child) {
      throw new Error("SidecarManager already started");
    }

    const token = generateLocalToken();
    const child = spawn(this.opts.binaryPath, [], {
      env: {
        ...process.env,
        WORKMAX_LOCAL_TOKEN: token,
        WORKMAX_DESKTOP_DATA_DIR: this.opts.dataDir,
      },
      stdio: ["pipe", "pipe", "pipe"],
    });

    this.child = child;
    let bootFailed: ((err: Error) => void) | null = null;
    let bootError: Error | null = null;
    let bootRejected = false;
    let bootComplete = false;

    child.once("error", (err) => {
      const wrapped = new Error(`sidecar spawn error: ${String(err)}`);
      this.logLine(`[sidecar-manager] ${wrapped.message}`);
      if (bootFailed) {
        const rejectBoot = bootFailed;
        bootFailed = null;
        bootRejected = true;
        rejectBoot(wrapped);
        return;
      }
      bootError = wrapped;
    });

    // Stderr → log line-by-line.
    if (child.stderr) {
      const stderrReader = createInterface({ input: child.stderr });
      this.stderrReader = stderrReader;
      stderrReader.on("line", (line) => {
        this.logLine(line);
      });
    }

    // Stdout: first line is the handshake JSON, then we drain (but log)
    // subsequent lines so the pipe doesn't fill.
    let stdoutReader: Interface | null = null;
    if (child.stdout) {
      stdoutReader = createInterface({ input: child.stdout });
      this.stdoutReader = stdoutReader;
    } else {
      throw new Error("sidecar child has no stdout pipe");
    }

    // exit handler — installed BEFORE waiting on handshake so we can
    // reject the promise if the binary dies during boot.
    child.once("exit", (code, signal) => {
      this.child = null;
      this.currentRuntime = null;
      this.stdoutReader?.close();
      this.stdoutReader = null;
      this.stderrReader?.close();
      this.stderrReader = null;

      if (bootFailed) {
        const rejectBoot = bootFailed;
        bootFailed = null;
        bootRejected = true;
        rejectBoot(new Error(`sidecar exited during boot (code=${code} signal=${signal})`));
        return;
      }

      if (bootRejected && !bootComplete) {
        return; // already reported as a boot failure
      }

      if (this.shuttingDown) {
        return; // expected
      }

      // P1 EA intentionally does not auto-restart. A restarted sidecar
      // gets a new port/token, while the current preload bridge captures
      // the old values. Restarting without a renderer notification path
      // would leave the UI talking to a dead endpoint.
      this.logLine(`[sidecar-manager] unexpected exit (code=${code} signal=${signal}); giving up until app restart`);
      this.opts.onUnexpectedExit?.(code, signal);
    });

    // Race: handshake-line OR timeout OR boot-time exit.
    const handshake = await new Promise<SidecarHandshake>((resolve, reject) => {
      let lineHandler: ((line: string) => void) | null = null;
      const timer = setTimeout(() => {
        failBoot(
          new Error(`sidecar handshake not received within ${this.opts.handshakeTimeoutMs}ms`),
          "SIGKILL"
        );
      }, this.opts.handshakeTimeoutMs);

      const failBoot = (err: Error, killSignal: NodeJS.Signals | null = "SIGKILL") => {
        bootRejected = true;
        bootFailed = null;
        clearTimeout(timer);
        if (lineHandler) {
          stdoutReader?.off("line", lineHandler);
        }
        if (killSignal && child.exitCode === null && child.signalCode === null) {
          child.kill(killSignal);
        }
        reject(err);
      };
      bootFailed = failBoot;

      if (bootError) {
        failBoot(bootError);
        return;
      }

      lineHandler = (line: string) => {
        clearTimeout(timer);
        bootFailed = null;
        if (lineHandler) {
          stdoutReader?.off("line", lineHandler);
        }
        // After we capture the handshake, surface subsequent stdout
        // lines as warnings (the sidecar should be putting log on stderr).
        stdoutReader?.on("line", (subsequentLine) => {
          this.logLine(`[sidecar-manager] unexpected stdout: ${subsequentLine}`);
        });
        try {
          resolve(parseSidecarHandshakeLine(line));
        } catch (err) {
          failBoot(err instanceof Error ? err : new Error(String(err)));
        }
      };

      stdoutReader?.once("line", lineHandler);
    });

    bootComplete = true;
    this.currentRuntime = {
      port: handshake.port,
      token,
      pid: handshake.pid,
      sidecarVersion: handshake.sidecar_version,
      startedAt: handshake.started_at,
    };

    return this.currentRuntime;
  }

  /** Currently-active runtime info, or null if the sidecar isn't running. */
  get runtime(): SidecarRuntime | null {
    return this.currentRuntime;
  }

  /** Initiate graceful shutdown: send "shutdown\n" on stdin, then escalate
   *  to SIGTERM and finally SIGKILL after the timeouts in sidecar-protocol.md §2.2. */
  async shutdown(): Promise<void> {
    if (!this.child) return;

    this.shuttingDown = true;
    const child = this.child;

    // 1. ask politely
    try {
      child.stdin?.write("shutdown\n");
      child.stdin?.end();
    } catch {
      // stdin might already be closed; fall through to signals
    }

    // 2. wait up to 5s for natural exit
    const exited = await waitForExit(child, 5_000);
    if (exited) return;

    // 3. SIGTERM + 2s grace
    this.logLine(`[sidecar-manager] sidecar did not exit on stdin shutdown; sending SIGTERM`);
    child.kill("SIGTERM");
    const exitedAfterTerm = await waitForExit(child, 2_000);
    if (exitedAfterTerm) return;

    // 4. SIGKILL
    this.logLine(`[sidecar-manager] sidecar did not respond to SIGTERM; sending SIGKILL`);
    child.kill("SIGKILL");
    await waitForExit(child, 1_000);
  }

  private logLine(line: string): void {
    (this.opts.onStderr ?? defaultStderrLogger)(redactSidecarLogLine(line));
  }
}

function waitForExit(child: ChildProcess, timeoutMs: number): Promise<boolean> {
  return new Promise((resolve) => {
    if (child.exitCode !== null || child.signalCode !== null) {
      resolve(true);
      return;
    }
    let resolved = false;
    const onExit = () => {
      if (resolved) return;
      resolved = true;
      resolve(true);
    };
    child.once("exit", onExit);
    delay(timeoutMs).then(() => {
      if (resolved) return;
      resolved = true;
      child.off("exit", onExit);
      resolve(false);
    });
  });
}

function defaultStderrLogger(line: string): void {
  // eslint-disable-next-line no-console
  console.error(line);
}

export function parseSidecarHandshakeLine(line: string): SidecarHandshake {
  let parsed: unknown;
  try {
    parsed = JSON.parse(line);
  } catch (err) {
    throw new Error(
      `malformed handshake JSON: ${String(err)} — first stdout line was: ${redactSidecarLogLine(line).slice(0, 200)}`
    );
  }

  if (!isObject(parsed)) {
    throw new Error("malformed handshake JSON: expected object");
  }

  const v = parsed.v;
  if (v !== SUPPORTED_HANDSHAKE_VERSION) {
    throw new Error(`unsupported handshake version: ${String(v)} (this Electron expects ${SUPPORTED_HANDSHAKE_VERSION})`);
  }

  const port = parsed.port;
  if (typeof port !== "number" || !Number.isInteger(port) || port < 1 || port > 65535) {
    throw new Error(`invalid port in handshake: ${String(port)}`);
  }

  const pid = parsed.pid;
  if (typeof pid !== "number" || !Number.isInteger(pid) || pid < 1) {
    throw new Error(`invalid pid in handshake: ${String(pid)}`);
  }

  const startedAt = parsed.started_at;
  if (
    typeof startedAt !== "string" ||
    startedAt.length === 0 ||
    Number.isNaN(Date.parse(startedAt))
  ) {
    throw new Error("invalid started_at in handshake");
  }

  const sidecarVersion = parsed.sidecar_version;
  if (typeof sidecarVersion !== "string" || sidecarVersion.length === 0) {
    throw new Error("invalid sidecar_version in handshake");
  }

  return {
    v,
    port,
    pid,
    started_at: startedAt,
    sidecar_version: sidecarVersion,
  };
}

function isObject(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}
