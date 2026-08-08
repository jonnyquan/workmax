// main.ts
//
// Electron main process — the long-lived Node program that:
//   1. spawns the Go sidecar (via SidecarManager)
//   2. opens a BrowserWindow that loads the selected trusted Desktop Renderer
//   3. exposes the sidecar port + X-Local-Token to the renderer via
//      env vars consumed by preload.ts
//   4. gracefully tears the sidecar down on app quit
//
// Design refs:
//   - sidecar-protocol.md §1 (process topology)
//   - sidecar-protocol.md §2 (lifecycle)
//   - README.md §2 (overall architecture)

import { app, BrowserWindow, ipcMain, shell, type IpcMainInvokeEvent } from "electron";
import { randomBytes } from "node:crypto";
import { existsSync, mkdirSync, writeFileSync } from "node:fs";
import { homedir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { pathToFileURL } from "node:url";

import { SidecarManager, SidecarRuntime } from "./sidecar-manager";
import {
  assertLoginPasswordIPCArgs,
  assertNoLoginTransactionIPCArgs,
  clearLoginPasswordIPCValue,
  loginTransactionErrorCode,
  loginTransactionFailureResult,
  MainLoginTransactionSession,
  type LoginTransactionDependencies,
  type LoginTransactionResult,
  type LoginTransactionRuntime,
} from "./login-transaction";
import { mainLog } from "./main-log";
import { resolveDesktopRenderer } from "./renderer-loader";
import {
  assertTrustedRendererSenderURL,
  isRendererNavigationAllowed,
  normalizeExternalHTTPURL,
  redactSidecarLogLine,
} from "./security-helpers";
import {
  redactSmokeArtifactSecrets,
  runSmokeDiagnosticsCheck,
  runSmokeLocalTokenRejectionChecks,
  smokeTokenFingerprint,
} from "./smoke-diagnostics";

const APP_VERSION = app.getVersion();
const BUNDLED_RENDERER_ENTRY = join("renderer", "en", "desktop", "index.html");

// Disable Electron's hardware acceleration crash on some Linux setups;
// the current desktop route does not render GPU-intensive content.
// (Comment out if causing issues on macOS dev — defaults are usually fine.)
// app.disableHardwareAcceleration();

let mainWindow: BrowserWindow | null = null;
let sidecar: SidecarManager | null = null;

async function bootSidecar(): Promise<SidecarRuntime> {
  const binaryPath = resolveSidecarBinaryPath();
  if (!existsSync(binaryPath)) {
    throw new Error(
      `sidecar binary not found at ${binaryPath} — run desktop/scripts/dev.sh first`
    );
  }

  const dataDir = process.env.WORKMAX_DESKTOP_DATA_DIR ?? join(homedir(), ".workmax");
  mainLog.info("spawning sidecar", { binaryPath, dataDir });

  sidecar = new SidecarManager({
    binaryPath,
    dataDir,
    onStderr: (line) => {
      const safeLine = redactSidecarLogLine(line);
      process.stderr.write(`[sidecar] ${safeLine}\n`);
      mainLog.info("sidecar output", { line: safeLine });
    },
    onUnexpectedExit: (code, signal) => {
      // Log + leave the window in a degraded state. The renderer can surface
      // sidecar/network health through its diagnostics UI.
      mainLog.error("sidecar died; app restart required to recover", {
        code,
        signal,
      });
    },
  });

  const runtime = await sidecar.start();
  assertVersionMatch(runtime);
  mainLog.info(`sidecar up on 127.0.0.1:${runtime.port}`, {
    pid: runtime.pid,
    version: runtime.sidecarVersion,
  });
  return runtime;
}

function resolveSidecarBinaryPath(): string {
  if (app.isPackaged) {
    // electron-builder copies extraResources to process.resourcesPath.
    // Do not execute from app.asar; binaries inside asar are not normal
    // filesystem executables on macOS.
    return join(process.resourcesPath, "workagent-desktop");
  }
  // Dev binary path: desktop/scripts/dev.sh builds into
  // desktop/electron/bin/workagent-desktop, while compiled main.js
  // runs from desktop/electron/dist.
  return resolve(__dirname, "..", "bin", "workagent-desktop");
}

function resolveRendererURL(): string {
  const bundledRendererEntry = app.isPackaged
    ? join(process.resourcesPath, BUNDLED_RENDERER_ENTRY)
    : resolve(__dirname, "..", "..", BUNDLED_RENDERER_ENTRY);
  const selection = resolveDesktopRenderer({
    isPackaged: app.isPackaged,
    bundledRendererURL: pathToFileURL(bundledRendererEntry).toString(),
    bundledRendererExists: existsSync(bundledRendererEntry),
    configuredRendererURL: process.env.WORKMAX_DESKTOP_RENDERER_URL,
    trustedRendererOrigins: process.env.WORKMAX_DESKTOP_TRUSTED_RENDERER_ORIGINS,
  });
  mainLog.info("selected Desktop renderer", {
    channel: selection.channel,
    url: selection.url,
  });
  return selection.url;
}

function assertVersionMatch(runtime: SidecarRuntime): void {
  if (runtime.sidecarVersion !== APP_VERSION) {
    throw new Error(
      `Electron/sidecar version mismatch: app=${APP_VERSION} sidecar=${runtime.sidecarVersion}`
    );
  }
}

function createWindow(runtime: SidecarRuntime, rendererUrl: string): BrowserWindow {
  // Publish sidecar coordinates into the renderer process's env so
  // preload.ts can read them. Must be set BEFORE BrowserWindow construction.
  process.env.WORKMAX_LOCAL_PORT = String(runtime.port);
  process.env.WORKMAX_LOCAL_TOKEN = runtime.token;
  process.env.WORKMAX_SIDECAR_VERSION = runtime.sidecarVersion;
  process.env.WORKMAX_APP_VERSION = APP_VERSION;

  const win = new BrowserWindow({
    width: 1280,
    height: 800,
    title: "WorkMax Desktop",
    backgroundColor: "#0b0b0e",
    webPreferences: {
      preload: resolve(__dirname, "preload.js"),
      contextIsolation: true,
      nodeIntegration: false,
      sandbox: false, // preload needs Node primitives (crypto, env); ok for local app
    },
  });

  // Packaged builds only load Resources/renderer/en/desktop/index.html.
  // Development must explicitly select the repository bundle, a loopback
  // server, or an HTTPS origin declared in the trusted development allowlist.
  installRendererNavigationGuards(win, rendererUrl);
  installSmokeRendererReporter(win, rendererUrl, runtime);
  void win.loadURL(rendererUrl).catch((err) => {
    mainLog.error(`failed to load renderer URL`, {
      url: rendererUrl,
      error: String(err),
    });
    if (!win.isDestroyed()) {
      win.destroy();
    }
    void shutdownSidecarForExit().finally(() => app.exit(1));
  });
  if (!app.isPackaged || process.env.WORKMAX_DESKTOP_OPEN_DEVTOOLS === "1") {
    win.webContents.openDevTools({ mode: "right" });
  }

  return win;
}

function installSmokeRendererReporter(
  win: BrowserWindow,
  rendererUrl: string,
  runtime: SidecarRuntime
): void {
  const outputPath = process.env.WORKMAX_DESKTOP_SMOKE_RENDERER_INFO;
  if (!outputPath) {
    return;
  }

  let reported = false;
  const writeResultAndQuit = async (status: "loaded" | "failed", error?: string): Promise<void> => {
    if (reported) {
      return;
    }
    reported = true;
    let rendererObservation: unknown = null;
    if (status === "loaded") {
      rendererObservation = await win.webContents.executeJavaScript(
        `(async () => {
          const wait = (predicate, timeoutMs = 5000) => new Promise((resolve) => {
            const started = Date.now();
            const tick = () => {
              if (predicate()) {
                resolve(true);
                return;
              }
              if (Date.now() - started > timeoutMs) {
                resolve(false);
                return;
              }
              setTimeout(tick, 100);
            };
            tick();
          });
          const bridge = globalThis.workmaxLocal;
          const expectCachedHistory = ${process.env.WORKMAX_DESKTOP_SMOKE_EXPECT_CACHED_HISTORY === "1" ? "true" : "false"};
          const expectedThreadText = ${JSON.stringify(process.env.WORKMAX_DESKTOP_SMOKE_EXPECT_THREAD_TEXT ?? "")};
          const expectedMessageText = ${JSON.stringify(process.env.WORKMAX_DESKTOP_SMOKE_EXPECT_MESSAGE_TEXT ?? "")};
          const result = {
            locationHref: globalThis.location.href,
            readyState: document.readyState,
            title: document.title,
            bodyTextLength: (document.body?.innerText || "").trim().length,
            hasBridge: Boolean(bridge),
            bridgePort: bridge?.port ?? null,
            appVersion: bridge?.appVersion ?? null,
            sidecarVersion: bridge?.sidecarVersion ?? null,
            platform: bridge?.platform ?? null,
            cachedHistoryVisible: false,
            cachedMessagesVisible: false,
            expectedThreadVisible: false,
            expectedMessagesVisible: false
          };
          const threadText = expectedThreadText || (expectCachedHistory ? "Smoke Cached Thread" : "");
          const messageText = expectedMessageText || (expectCachedHistory ? "Smoke cached assistant answer" : "");
          if (threadText) {
            result.expectedThreadVisible = await wait(() => document.body?.innerText?.includes(threadText));
            const threadButton = Array.from(document.querySelectorAll("button"))
              .find((button) => button.textContent?.includes(threadText));
            if (threadButton && messageText) {
              threadButton.click();
              result.expectedMessagesVisible = await wait(() =>
                document.body?.innerText?.includes(messageText)
              );
            }
          }
          if (expectCachedHistory) {
            result.cachedHistoryVisible = result.expectedThreadVisible;
            result.cachedMessagesVisible = result.expectedMessagesVisible;
          }
          result.bodyTextLength = (document.body?.innerText || "").trim().length;
          result.bodyText = (document.body?.innerText || "").trim().slice(0, 2000);
          return result;
        })()`,
        true
      );
    }
    const redactedObservation = redactSmokeArtifactSecrets(rendererObservation, runtime.token);
    rendererObservation = redactedObservation.value;
    const smokeLocalTokenLeakDetected = redactedObservation.localTokenRedacted;
    const smokeSensitiveLeakDetected = redactedObservation.sensitiveRedacted;
    const localTokenRejectionChecks =
      status === "loaded" ? await runSmokeLocalTokenRejectionChecks(runtime) : null;
    const diagnosticsCheck =
      status === "loaded" ? await runSmokeDiagnosticsCheck(runtime) : null;

    const result = {
      ok: status === "loaded" && !smokeLocalTokenLeakDetected && !smokeSensitiveLeakDetected,
      status,
      error: smokeLocalTokenLeakDetected
        ? "renderer observation contained the local token"
        : smokeSensitiveLeakDetected
          ? "renderer observation contained sensitive text"
        : error,
      timestamp: new Date().toISOString(),
      appIsPackaged: app.isPackaged,
      expectedRendererUrl: rendererUrl,
      loadedUrl: win.webContents.getURL(),
      cloudBase: process.env.WORKMAX_CLOUD_BASE ?? null,
      rendererObservation,
      smokeLocalTokenLeakDetected,
      smokeSensitiveLeakDetected,
      localTokenRejectionChecks,
      diagnosticsCheck,
      localTokenFingerprint: smokeTokenFingerprint(runtime.token),
    };
    mkdirSync(dirname(outputPath), { recursive: true });
    writeFileSync(outputPath, `${JSON.stringify(result, null, 2)}\n`, "utf8");
    app.quit();
  };

  win.webContents.once("did-finish-load", () => {
    void writeResultAndQuit("loaded").catch((err) => {
      mainLog.error("packaged smoke renderer reporter failed", { error: String(err) });
      app.exit(1);
    });
  });
  win.webContents.on("did-fail-load", (_event, errorCode, errorDescription, validatedURL, isMainFrame) => {
    if (isMainFrame === false) {
      return;
    }
    void writeResultAndQuit(
      "failed",
      `${errorCode}: ${errorDescription} (${validatedURL})`
    ).catch((err) => {
      mainLog.error("packaged smoke renderer reporter failed", { error: String(err) });
      app.exit(1);
    });
  });
}

function installRendererNavigationGuards(win: BrowserWindow, rendererUrl: string): void {
  win.webContents.session.setPermissionRequestHandler(
    (_webContents, permission, callback) => {
      mainLog.warn("blocked renderer permission request", { permission });
      callback(false);
    }
  );

  const guardNavigation = (
    event: { preventDefault: () => void },
    targetURL: string,
    navigationType: "navigation" | "redirect"
  ): void => {
    if (isRendererNavigationAllowed(targetURL, rendererUrl)) {
      return;
    }
    event.preventDefault();
    openExternalHTTPURL(targetURL);
    mainLog.warn(`blocked renderer ${navigationType} outside desktop route`, {
      rendererUrl,
      targetURL,
    });
  };

  win.webContents.on("will-navigate", (event, targetURL) => {
    guardNavigation(event, targetURL, "navigation");
  });
  win.webContents.on("will-redirect", (event, targetURL) => {
    guardNavigation(event, targetURL, "redirect");
  });

  win.webContents.setWindowOpenHandler(({ url }) => {
    openExternalHTTPURL(url);
    mainLog.warn("blocked renderer popup outside desktop window", { url });
    return { action: "deny" };
  });
}

function openExternalHTTPURL(targetURL: string): void {
  const externalURL = normalizeExternalHTTPURL(targetURL);
  if (!externalURL) {
    return;
  }
  void shell.openExternal(externalURL).catch((err) => {
    mainLog.warn("openExternal failed", {
      url: externalURL,
      error: String(err),
    });
  });
}

async function onReady(): Promise<void> {
  try {
    const runtime = await bootSidecar();
    const rendererUrl = resolveRendererURL();
    registerIPC(rendererUrl, runtime);
    mainWindow = createWindow(runtime, rendererUrl);
  } catch (err) {
    mainLog.error("failed to boot sidecar", { error: String(err) });
    await shutdownSidecarForExit();
    app.exit(1);
  }
}

async function shutdownSidecarForExit(): Promise<void> {
  if (!sidecar || !sidecar.runtime) {
    sidecar = null;
    return;
  }
  try {
    await sidecar.shutdown();
  } catch (shutdownErr) {
    mainLog.error("sidecar shutdown error", { error: String(shutdownErr) });
  } finally {
    sidecar = null;
  }
}

// IPC handlers — preload bridge calls these from the renderer.
function registerIPC(rendererUrl: string, runtime: SidecarRuntime): void {
  // Login transaction routes stay Main-only. Renderer receives only the small
  // public state/error envelope and can provide credentials only through the
  // exact password command.
  const loginRuntime: LoginTransactionRuntime = {
    sidecarPort: runtime.port,
    localToken: runtime.token,
  };
  const loginDependencies: LoginTransactionDependencies = {
    request: (input, init) => fetch(input, init),
  };
  const loginSession = new MainLoginTransactionSession(
    loginRuntime,
    loginDependencies,
    () => randomBytes(32).toString("base64url")
  );

  ipcMain.handle(
    "auth-begin-login-transaction",
    async (event, ...args: unknown[]) => {
      return handleLoginTransactionIPC("begin", async () => {
        assertTrustedIpcSender(event, rendererUrl);
        assertNoLoginTransactionIPCArgs(args);
        return loginSession.begin();
      });
    }
  );
  ipcMain.handle(
    "auth-login-transaction-status",
    async (event, ...args: unknown[]) => {
      return handleLoginTransactionIPC("status", async () => {
        assertTrustedIpcSender(event, rendererUrl);
        assertNoLoginTransactionIPCArgs(args);
        return loginSession.status();
      });
    }
  );
  ipcMain.handle(
    "auth-submit-login-password",
    async (event, ...args: unknown[]) => {
      return handleLoginTransactionIPC("password", async () => {
        assertTrustedIpcSender(event, rendererUrl);
        const rawInput = args[0];
        const input = assertLoginPasswordIPCArgs(args);
        try {
          // submitPassword serializes its own validated copy before returning
          // the Promise, so both Main IPC object copies can be cleared now.
          return loginSession.submitPassword(input);
        } finally {
          input.password = "";
          clearLoginPasswordIPCValue(rawInput);
        }
      });
    }
  );
  ipcMain.handle(
    "auth-cancel-login-transaction",
    async (event, ...args: unknown[]) => {
      return handleLoginTransactionIPC("cancel", async () => {
        assertTrustedIpcSender(event, rendererUrl);
        assertNoLoginTransactionIPCArgs(args);
        return loginSession.cancel();
      });
    }
  );

  // DiagnosticsPanel's "Open logs" button — reveal the data dir
  // (which contains logs/) in the platform's file browser. Renderer
  // could shell.openPath via window.location, but Electron's
  // contextIsolation closes that off; routing through IPC keeps the
  // bridge surface minimal and auditable.
  //
  // Returns the resolved path on success so the renderer can show
  // the user a "we opened <path>" toast if the open silently no-op's
  // (rare on macOS but happens on some Linux desktop environments).
  ipcMain.handle(
    "reveal-data-dir",
    async (event): Promise<{ ok: boolean; path?: string; error?: string }> => {
      try {
        assertTrustedIpcSender(event, rendererUrl);
        const dataDir =
          process.env.WORKMAX_DESKTOP_DATA_DIR ?? join(homedir(), ".workmax");
        // openPath returns the empty string on success; non-empty
        // string indicates an OS-level failure ("the file <X> does
        // not exist" etc.).
        const result = await shell.openPath(dataDir);
        if (result !== "") {
          mainLog.warn("reveal-data-dir: openPath returned non-empty", {
            path: dataDir,
            result,
          });
          return { ok: false, error: result };
        }
        return { ok: true, path: dataDir };
      } catch (err) {
        mainLog.error("reveal-data-dir failed", { error: String(err) });
        return { ok: false, error: String(err) };
      }
    }
  );
}

async function handleLoginTransactionIPC(
  operation: "begin" | "status" | "password" | "cancel",
  action: () => Promise<LoginTransactionResult>
): Promise<LoginTransactionResult> {
  try {
    return await action();
  } catch (err) {
    // Never log IPC arguments, response bodies, or credential values.
    mainLog.warn("Desktop sign-in transaction failed", {
      operation,
      code: loginTransactionErrorCode(err),
    });
    return loginTransactionFailureResult(err);
  }
}

function assertTrustedIpcSender(event: IpcMainInvokeEvent, rendererUrl: string): void {
  if (
    mainWindow === null ||
    mainWindow.isDestroyed() ||
    event.sender !== mainWindow.webContents ||
    event.senderFrame === null ||
    event.senderFrame !== event.sender.mainFrame
  ) {
    mainLog.warn("blocked IPC outside the main Desktop frame");
    throw new Error("IPC sender is not the main Desktop frame");
  }
  const senderURL = event.senderFrame.url;
  try {
    assertTrustedRendererSenderURL(senderURL, rendererUrl);
  } catch (err) {
    // A hostile frame can control its own URL. Do not copy it (or a trusted
    // development URL that might carry incidental query data) into logs.
    mainLog.warn("blocked IPC from untrusted renderer URL");
    throw err;
  }
}

function focusMainWindow(): void {
  if (mainWindow === null || mainWindow.isDestroyed()) {
    mainLog.warn("second instance requested but no main window exists");
    return;
  }
  if (mainWindow.isMinimized()) {
    mainWindow.restore();
  }
  mainWindow.show();
  mainWindow.focus();
}

const gotSingleInstanceLock = app.requestSingleInstanceLock();

if (!gotSingleInstanceLock) {
  mainLog.warn("another WorkMax Desktop instance is already running; quitting");
  app.quit();
} else {
  app.on("second-instance", () => {
    focusMainWindow();
  });

  app.whenReady().then(onReady).catch((err) => {
    mainLog.error("whenReady failed", { error: String(err) });
    app.exit(1);
  });

  app.on("window-all-closed", () => {
    // macOS convention: keep app alive when all windows close (dock icon
    // stays). Current Desktop exits so the sidecar is reaped — cleaner
    // dev loop. P2 can revisit the macOS convention.
    app.quit();
  });

  app.on("before-quit", async (event) => {
    if (sidecar && sidecar.runtime) {
      event.preventDefault();
      await shutdownSidecarForExit();
      app.exit(0);
    }
  });

  app.on("activate", () => {
    // With the current "close window quits app" lifecycle this should be
    // rare; keep a diagnostic log if Electron activates without a window.
    if (mainWindow === null || mainWindow.isDestroyed()) {
      mainLog.warn("activate received with no main window");
    }
  });
}

// Catch-all for synchronous throws that escaped main's handlers
// AND unhandled promise rejections. Without this, Electron's
// default behavior is to either exit silently (production) or
// dump to syslog (dev). Either way the user sees nothing actionable;
// a journaled log line gives ops + support a foothold.
process.on("uncaughtException", (err) => {
  mainLog.error("uncaughtException", {
    error: err.message,
    stack: err.stack,
  });
});

process.on("unhandledRejection", (reason) => {
  const message =
    reason instanceof Error ? `unhandledRejection: ${reason.message}` : `unhandledRejection: ${String(reason)}`;
  mainLog.error(message, {
    stack: reason instanceof Error ? reason.stack : undefined,
  });
});
