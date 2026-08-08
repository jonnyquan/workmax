// preload.ts
//
// Runs once per BrowserWindow, in a privileged Node context, BEFORE the
// renderer's web content has its globals exposed. Uses contextBridge
// to publish the versioned window.desktopBridge API plus the temporary
// window.workmaxLocal compatibility surface used by the bundled Renderer.
//
// The X-Local-Token and sidecar port come in via env vars that the
// main process set BEFORE creating the BrowserWindow. Token is captured
// here in a closure variable; renderer code cannot read it directly.
//
// Design refs:
//   - sidecar-protocol.md §3.3 (X-Local-Token flow)
//   - sidecar-protocol.md §5   (preload contextBridge API surface)

import { contextBridge, ipcRenderer } from "electron";

import {
  createDesktopBridge,
  type AgentDoneResult,
  type AgentProxyError,
  type AgentSidecarTurnRequest,
  type AgentSidecarTurnStartRequest,
  type AgentTurnCancelResult,
  type AgentTurnEvent,
  type AgentTurnEventCallback,
  type AgentTurnOpenResult,
  type LegacyBridgeResponse,
} from "./desktop-bridge";
import type { LoginPasswordInput } from "./login-transaction";

const MAX_SIDECAR_RELATIVE_URL_BYTES = 8 * 1024;
const MAX_PATH_DECODE_PASSES = 8;
const MAX_AGENT_SSE_FRAME_BYTES = 1 << 20;
const MAX_AGENT_HTTP_ERROR_BYTES = 64 * 1024;
const MAX_ACTIVE_AGENT_TURNS = 5;
const MAX_AGENT_PROTOCOL_LABEL_BYTES = 128;
const MAX_AGENT_PROXY_KIND_BYTES = 64;
const MAX_AGENT_PROXY_MESSAGE_BYTES = 4096;
const MAX_AGENT_PROXY_LOG_ID_BYTES = 256;
const MAX_AGENT_PROXY_RETRY_AFTER_MS = 86_400_000;
const AGENT_PROXY_ERROR_KINDS = new Set([
  "network_unavailable",
  "auth_required",
  "auth_expired",
  "service_unavailable",
  "quota_exceeded",
  "rate_limited",
  "payload_too_large",
  "bad_request",
  "session_changed",
  "unknown",
]);

const rawPort = process.env.WORKMAX_LOCAL_PORT ?? "";
const port = parseLoopbackPort(rawPort);
const token = process.env.WORKMAX_LOCAL_TOKEN ?? "";

if (port === null || !isValidLocalToken(token)) {
  // Don't expose workmaxLocal at all if the env wasn't wired properly.
  // The renderer will see `window.workmaxLocal === undefined` and can
  // surface a clear error rather than fetching to a nonsense port.
  // eslint-disable-next-line no-console
  console.error(
    `[preload] missing or invalid WORKMAX_LOCAL_PORT (${process.env.WORKMAX_LOCAL_PORT}) or WORKMAX_LOCAL_TOKEN; workmaxLocal not exposed`
  );
} else {
  const fetchSidecarResponse = async (
    path: string,
    init?: RequestInit
  ): Promise<Response> => {
    const url = buildSidecarURL(path);
    const headers = new Headers(init?.headers);
    headers.set("X-Local-Token", token);
    if (!headers.has("X-Request-ID")) {
      headers.set("X-Request-ID", cryptoRandomULID());
    }
    return fetch(url, {
      ...init,
      credentials: "omit",
      redirect: "error",
      headers,
    });
  };
  const sidecarFetch = async (
    path: string,
    init?: RequestInit
  ): Promise<LegacyBridgeResponse> => {
    const url = buildSidecarURL(path);
    if (isPrivilegedLoginTransactionURL(url)) {
      throw new TypeError(
        "workmaxLocal.fetch cannot access the privileged sign-in transaction route"
      );
    }
    if (isTypedAgentOnlyRequest(url, init)) {
      throw new TypeError(
        "workmaxLocal.fetch cannot access typed Agent routes"
      );
    }
    if (isTypedSettingsOnlyRequest(url)) {
      throw new TypeError(
        "workmaxLocal.fetch cannot access typed settings routes"
      );
    }
    const response = await fetchSidecarResponse(path, init);
    return bridgeResponse(response);
  };
  const typedSidecarFetch = async (
    path: string,
    init: RequestInit
  ): Promise<LegacyBridgeResponse> => {
    return bridgeResponse(await fetchSidecarResponse(path, init));
  };
  const activeAgentTurns = new Map<string, ActiveAgentTurn>();
  const startAgentTurn = (
    request: AgentSidecarTurnStartRequest,
    callback: AgentTurnEventCallback
  ): AgentTurnOpenResult => {
    return openAgentTurn(
      activeAgentTurns,
      fetchSidecarResponse,
      request,
      callback
    );
  };
  const resumeAgentTurn = (
    turnUUID: string,
    callback: AgentTurnEventCallback
  ): AgentTurnOpenResult => {
    return resumeOpenAgentTurn(
      activeAgentTurns,
      fetchSidecarResponse,
      turnUUID,
      callback
    );
  };
  const cancelAgentTurn = (
    turnID: string
  ): Promise<AgentTurnCancelResult> => {
    return cancelOpenAgentTurn(
      activeAgentTurns,
      fetchSidecarResponse,
      turnID
    );
  };
  const beginLogin = () => {
    return ipcRenderer.invoke("auth-begin-login-transaction");
  };
  const loginStatus = () => {
    return ipcRenderer.invoke("auth-login-transaction-status");
  };
  const submitLoginPassword = (input: LoginPasswordInput) => {
    return ipcRenderer.invoke("auth-submit-login-password", input);
  };
  const cancelLogin = () => {
    return ipcRenderer.invoke("auth-cancel-login-transaction");
  };
  const revealDataDir = (): Promise<{
    ok: boolean;
    path?: string;
    error?: string;
  }> => {
    return ipcRenderer.invoke("reveal-data-dir");
  };

  const legacyBridge = {
    /** Sidecar HTTP port (loopback). */
    port,

    /** Sidecar / Electron build version (read from env populated by main). */
    sidecarVersion: process.env.WORKMAX_SIDECAR_VERSION ?? "unknown",
    appVersion: process.env.WORKMAX_APP_VERSION ?? "unknown",

    /** Host platform shorthand. */
    platform: process.platform,

    /**
     * fetch wrapper that targets the local sidecar and auto-injects
     * X-Local-Token + X-Request-ID.
     *
     * Usage from renderer:
     *   const res = await window.workmaxLocal.fetch('/health');
     *   const body = await res.json();
     */
    fetch: sidecarFetch,

    /**
     * Reveal the per-user data directory in the platform file
     * browser (Finder on macOS, Explorer on Windows, default file
     * manager on Linux). Used by the diagnostics panel's "Open logs"
     * button so users can hand-attach a log file to a support email
     * without hunting through the filesystem.
     *
     * Returns the resolved path on success, or an error string when
     * the platform's openPath rejects (rare; some Linux DEs don't
     * register the file:// protocol for unfamiliar paths).
     */
    revealDataDir,
  };

  // Compatibility is deliberate: the current bundled Renderer continues to
  // use workmaxLocal.fetch while new Desktop surfaces can migrate namespace by
  // namespace to the versioned, fixed-route facade.
  contextBridge.exposeInMainWorld("workmaxLocal", legacyBridge);
  contextBridge.exposeInMainWorld(
    "desktopBridge",
    createDesktopBridge({
      runtime: {
        sidecarVersion: process.env.WORKMAX_SIDECAR_VERSION ?? "unknown",
        appVersion: process.env.WORKMAX_APP_VERSION ?? "unknown",
        platform: process.platform,
      },
      request: typedSidecarFetch,
      beginLogin,
      loginStatus,
      submitLoginPassword,
      cancelLogin,
      startAgentTurn,
      resumeAgentTurn,
      cancelAgentTurn,
      revealDataDir,
    })
  );
}

function bridgeResponse(response: Response): LegacyBridgeResponse {
  let textCache: Promise<string> | null = null;
  const readText = (): Promise<string> => {
    if (!textCache) {
      textCache = response.text();
    }
    return textCache;
  };
  return {
    ok: response.ok,
    status: response.status,
    statusText: response.statusText,
    url: response.url,
    headers: Object.fromEntries(response.headers.entries()),
    body: response.body
      ? {
          getReader: () => response.body!.getReader(),
        }
      : null,
    text: readText,
    json: async () => {
      const text = await readText();
      return text === "" ? null : JSON.parse(text);
    },
  };
}

type SidecarResponseFetcher = (
  path: string,
  init: RequestInit
) => Promise<Response>;

interface ActiveAgentTurn {
  turnID: string;
  callback: AgentTurnEventCallback;
  abortController: AbortController;
  reader: ReadableStreamDefaultReader<Uint8Array> | null;
  terminal: boolean;
  cancelRequested: boolean;
}

function openAgentTurn(
  turns: Map<string, ActiveAgentTurn>,
  fetchSidecarResponse: SidecarResponseFetcher,
  request: AgentSidecarTurnStartRequest,
  callback: AgentTurnEventCallback
): AgentTurnOpenResult {
  for (const [turnID, active] of turns) {
    if (active.terminal) turns.delete(turnID);
  }
  if (turns.size >= MAX_ACTIVE_AGENT_TURNS) {
    throw new RangeError(
      `agent.startTurn active turn limit of ${MAX_ACTIVE_AGENT_TURNS} reached`
    );
  }
  const turnID = cryptoRandomV4UUID();
  const sidecarRequest: AgentSidecarTurnRequest = {
    turn_uuid: turnID,
    ...request,
  };
  const active: ActiveAgentTurn = {
    turnID,
    callback,
    abortController: new AbortController(),
    reader: null,
    terminal: false,
    cancelRequested: false,
  };
  turns.set(turnID, active);
  void consumeAgentTurn(turns, fetchSidecarResponse, active, {
    path: "/agent/chat",
    init: {
      method: "POST",
      headers: {
        Accept: "text/event-stream",
        "Content-Type": "application/json",
      },
      body: JSON.stringify(sidecarRequest),
    },
  });
  return { turnID };
}

function resumeOpenAgentTurn(
  turns: Map<string, ActiveAgentTurn>,
  fetchSidecarResponse: SidecarResponseFetcher,
  turnUUID: string,
  callback: AgentTurnEventCallback
): AgentTurnOpenResult {
  for (const [turnID, active] of turns) {
    if (active.terminal) turns.delete(turnID);
  }
  if (turns.has(turnUUID)) {
    throw new RangeError("agent.resumeTurn is already attached to this turn");
  }
  if (turns.size >= MAX_ACTIVE_AGENT_TURNS) {
    throw new RangeError(
      `agent.resumeTurn active turn limit of ${MAX_ACTIVE_AGENT_TURNS} reached`
    );
  }
  const active: ActiveAgentTurn = {
    turnID: turnUUID,
    callback,
    abortController: new AbortController(),
    reader: null,
    terminal: false,
    cancelRequested: false,
  };
  turns.set(turnUUID, active);
  void consumeAgentTurn(turns, fetchSidecarResponse, active, {
    path: `/agent/turns/${encodeURIComponent(turnUUID)}/replay`,
    init: {
      method: "POST",
      headers: { Accept: "text/event-stream" },
    },
  });
  return { turnID: turnUUID };
}

interface AgentTurnStreamSource {
  path: string;
  init: RequestInit;
}

async function cancelOpenAgentTurn(
  turns: Map<string, ActiveAgentTurn>,
  fetchSidecarResponse: SidecarResponseFetcher,
  turnID: string
): Promise<AgentTurnCancelResult> {
  const active = turns.get(turnID);
  let localCanceled = false;
  let readerCancel: Promise<void> | undefined;
  if (active && !active.terminal) {
    localCanceled = true;
    active.cancelRequested = true;
    emitAgentTerminal(active, { type: "canceled", turnID });
    readerCancel = active.reader?.cancel("renderer canceled Agent turn");
    active.abortController.abort();
    if (turns.get(turnID) === active) {
      turns.delete(turnID);
    }
  }

  let response: Response;
  try {
    response = await fetchSidecarResponse(
      `/agent/turns/${encodeURIComponent(turnID)}/cancel`,
      {
        method: "POST",
        headers: { Accept: "application/json" },
      }
    );
  } finally {
    if (readerCancel) {
      try {
        await readerCancel;
      } catch {
        // Local cancellation is already terminal and must not be rolled back
        // if the transport was concurrently closing.
      }
    }
  }
  const payload = await readBoundedAgentErrorJSON(response);
  if (!response.ok) {
    throw new Error(`Agent Sidecar returned HTTP ${response.status}`);
  }
  if (
    !isRecord(payload) ||
    Object.keys(payload).length !== 2 ||
    payload.turn_uuid !== turnID ||
    typeof payload.canceled !== "boolean"
  ) {
    throw new TypeError("agent.cancelTurn response is malformed");
  }

  return { turnID, canceled: localCanceled || payload.canceled };
}

async function consumeAgentTurn(
  turns: Map<string, ActiveAgentTurn>,
  fetchSidecarResponse: SidecarResponseFetcher,
  active: ActiveAgentTurn,
  source: AgentTurnStreamSource
): Promise<void> {
  try {
    const response = await fetchSidecarResponse(source.path, {
      ...source.init,
      signal: active.abortController.signal,
    });
    if (active.terminal) {
      await cancelResponseBody(response);
      return;
    }
    if (!response.ok) {
      const errorPayload = await readBoundedAgentErrorJSON(response);
      const sessionError = sessionChangedHTTPError(errorPayload);
      if (sessionError) {
        emitAgentTerminal(active, {
          type: "proxy_error",
          turnID: active.turnID,
          error: sessionError,
        });
        return;
      }
      emitAgentProtocolError(
        active,
        "http_error",
        `Agent Sidecar returned HTTP ${response.status}`
      );
      return;
    }
    const contentType = response.headers
      .get("content-type")
      ?.split(";", 1)[0]
      .trim()
      .toLowerCase();
    if (contentType !== "text/event-stream") {
      await cancelResponseBody(response);
      emitAgentProtocolError(
        active,
        "invalid_content_type",
        "Agent Sidecar response is not text/event-stream"
      );
      return;
    }
    if (!response.body) {
      emitAgentProtocolError(
        active,
        "missing_stream",
        "Agent Sidecar response has no stream body"
      );
      return;
    }

    const reader = response.body.getReader();
    active.reader = reader;
    const parser = new AgentSSEParser(active);
    while (!active.terminal) {
      const chunk = await reader.read();
      if (chunk.done) {
        parser.finish();
        if (!active.terminal) {
          emitAgentProtocolError(
            active,
            "unexpected_eof",
            "Agent stream ended without a terminal event"
          );
        }
        break;
      }
      if (chunk.value) {
        parser.push(chunk.value);
      }
    }

    // A terminal frame is a hard fence. Stop the source immediately so bytes
    // after done/proxy_error cannot become a late Renderer callback.
    if (active.terminal && !active.cancelRequested) {
      try {
        await reader.cancel("Agent terminal event received");
      } catch {
        // The stream commonly closes at the same time as done; rejection here
        // cannot change the already-delivered terminal state.
      }
    }
  } catch (error) {
    if (active.terminal || active.cancelRequested) {
      return;
    }
    emitAgentProtocolError(
      active,
      "transport_error",
      error instanceof Error && error.name === "AbortError"
        ? "Agent transport was aborted"
        : "Agent transport failed"
    );
  } finally {
    if (active.reader) {
      try {
        active.reader.releaseLock();
      } catch {
        // A concurrently canceled stream may already have released its lock.
      }
    }
    if (turns.get(active.turnID) === active) {
      turns.delete(active.turnID);
    }
  }
}

async function cancelResponseBody(response: Response): Promise<void> {
  if (!response.body) return;
  try {
    await response.body.cancel();
  } catch {
    // Best-effort cleanup for an HTTP/protocol rejection.
  }
}

async function readBoundedAgentErrorJSON(
  response: Response
): Promise<unknown | null> {
  if (!response.body) return null;
  const reader = response.body.getReader();
  const chunks: Uint8Array[] = [];
  let total = 0;
  try {
    while (true) {
      const chunk = await reader.read();
      if (chunk.done) break;
      if (!chunk.value) continue;
      total += chunk.value.byteLength;
      if (total > MAX_AGENT_HTTP_ERROR_BYTES) {
        await reader.cancel("Agent HTTP error body is too large");
        return null;
      }
      chunks.push(chunk.value);
    }
    const body = new Uint8Array(total);
    let offset = 0;
    for (const chunk of chunks) {
      body.set(chunk, offset);
      offset += chunk.byteLength;
    }
    const text = new TextDecoder("utf-8", { fatal: true }).decode(body);
    return text === "" ? null : (JSON.parse(text) as unknown);
  } catch {
    return null;
  } finally {
    try {
      reader.releaseLock();
    } catch {
      // A size rejection may have already released the stream lock.
    }
  }
}

function sessionChangedHTTPError(value: unknown): AgentProxyError | null {
  if (!isRecord(value)) return null;
  if (value.error === "session_changed") {
    return {
      kind: "session_changed",
      message: "Desktop session changed",
      retryable: false,
    };
  }
  if (isRecord(value.error) && value.error.kind === "session_changed") {
    return normalizeAgentProxyError({
      ...value.error,
      message:
        typeof value.error.message === "string"
          ? value.error.message
          : "Desktop session changed",
    });
  }
  if (value.kind === "session_changed") {
    return normalizeAgentProxyError({
      ...value,
      message:
        typeof value.message === "string"
          ? value.message
          : "Desktop session changed",
    });
  }
  return null;
}

class AgentSSEParser {
  private readonly decoder = new TextDecoder("utf-8", { fatal: true });
  private buffer = "";
  private frameBytes = 0;
  private eventName = "";
  private dataLines: string[] = [];

  constructor(private readonly active: ActiveAgentTurn) {}

  push(chunk: Uint8Array): void {
    if (this.active.terminal) return;
    try {
      this.buffer += this.decoder.decode(chunk, { stream: true });
    } catch {
      this.fail("invalid_utf8", "Agent stream contains invalid UTF-8");
      return;
    }
    this.drain(false);
  }

  finish(): void {
    if (this.active.terminal) return;
    try {
      this.buffer += this.decoder.decode();
    } catch {
      this.fail("invalid_utf8", "Agent stream ended with invalid UTF-8");
      return;
    }
    this.drain(true);
  }

  private drain(final: boolean): void {
    while (!this.active.terminal) {
      const ending = findSSELineEnding(this.buffer, final);
      if (!ending) break;
      const line = this.buffer.slice(0, ending.index);
      const delimiter = this.buffer.slice(
        ending.index,
        ending.index + ending.length
      );
      this.buffer = this.buffer.slice(ending.index + ending.length);
      this.consumeLine(line, delimiter);
    }
    if (this.active.terminal) return;

    if (final && this.buffer !== "") {
      const line = this.buffer;
      this.buffer = "";
      this.consumeLine(line, "");
    }
    if (this.active.terminal) return;

    if (
      this.frameBytes + utf8ByteLength(this.buffer) >
      MAX_AGENT_SSE_FRAME_BYTES
    ) {
      this.fail(
        "frame_too_large",
        `Agent SSE frame exceeds ${MAX_AGENT_SSE_FRAME_BYTES} bytes`
      );
      return;
    }

    // Be tolerant of a final record without the conventional blank line, but
    // still require an eventual terminal semantic event at the stream level.
    if (final && this.dataLines.length > 0) {
      this.dispatchFrame();
      this.resetFrame();
    }
  }

  private consumeLine(line: string, delimiter: string): void {
    this.frameBytes += utf8ByteLength(line) + utf8ByteLength(delimiter);
    if (this.frameBytes > MAX_AGENT_SSE_FRAME_BYTES) {
      this.fail(
        "frame_too_large",
        `Agent SSE frame exceeds ${MAX_AGENT_SSE_FRAME_BYTES} bytes`
      );
      return;
    }
    if (line === "") {
      if (this.dataLines.length > 0) {
        this.dispatchFrame();
      }
      this.resetFrame();
      return;
    }
    if (line.startsWith(":")) {
      return;
    }

    const colon = line.indexOf(":");
    const field = colon < 0 ? line : line.slice(0, colon);
    let value = colon < 0 ? "" : line.slice(colon + 1);
    if (value.startsWith(" ")) value = value.slice(1);
    if (field === "event") {
      this.eventName = value;
    } else if (field === "data") {
      this.dataLines.push(value);
    }
  }

  private dispatchFrame(): void {
    dispatchAgentSSEFrame(
      this.active,
      this.eventName === "" ? "message" : this.eventName,
      this.dataLines.join("\n")
    );
  }

  private resetFrame(): void {
    this.frameBytes = 0;
    this.eventName = "";
    this.dataLines = [];
  }

  private fail(code: string, message: string): void {
    this.buffer = "";
    this.resetFrame();
    emitAgentProtocolError(this.active, code, message);
  }
}

interface SSELineEnding {
  index: number;
  length: 1 | 2;
}

function findSSELineEnding(
  value: string,
  final: boolean
): SSELineEnding | null {
  for (let index = 0; index < value.length; index += 1) {
    const char = value[index];
    if (char === "\n") return { index, length: 1 };
    if (char !== "\r") continue;
    if (index + 1 === value.length && !final) return null;
    return {
      index,
      length: value[index + 1] === "\n" ? 2 : 1,
    };
  }
  return null;
}

function dispatchAgentSSEFrame(
  active: ActiveAgentTurn,
  eventName: string,
  rawData: string
): void {
  if (active.terminal) return;

  if (eventName === "proxy_error") {
    const parsed = parseAgentJSON(rawData);
    const error = parsed.ok ? normalizeAgentProxyError(parsed.value) : null;
    if (!error) {
      emitAgentProtocolError(
        active,
        "invalid_event",
        "Agent proxy_error payload is malformed"
      );
      return;
    }
    emitAgentTerminal(active, {
      type: "proxy_error",
      turnID: active.turnID,
      error,
    });
    return;
  }

  if (eventName === "done") {
    const parsed = rawData === "" ? { ok: true as const, value: null } : parseAgentJSON(rawData);
    if (!parsed.ok) {
      emitAgentProtocolError(
        active,
        "invalid_event",
        "Agent done payload is malformed"
      );
      return;
    }
    emitAgentTerminal(active, {
      type: "done",
      turnID: active.turnID,
      result: doneResult(parsed.value),
    });
    return;
  }

  if (eventName === "text" || eventName === "text_delta") {
    const delta = explicitTextDelta(rawData);
    if (delta === null) {
      emitAgentProtocolError(
        active,
        "invalid_event",
        `Agent ${eventName} payload is malformed`
      );
      return;
    }
    emitAgentEvent(active, {
      type: "text_delta",
      turnID: active.turnID,
      delta,
    });
    return;
  }

  const parsed = parseAgentJSON(rawData);
  if (parsed.ok && isRecord(parsed.value)) {
    const envelopeType = parsed.value.type;
    if (envelopeType === "done") {
      emitAgentTerminal(active, {
        type: "done",
        turnID: active.turnID,
        result: doneResult(parsed.value),
      });
      return;
    }
    if (envelopeType === "block") {
      const delta = blockTextDelta(parsed.value.block);
      if (delta !== null) {
        emitAgentEvent(active, {
          type: "text_delta",
          turnID: active.turnID,
          delta,
        });
        return;
      }
    } else if (envelopeType === "text" || envelopeType === "text_delta") {
      const delta = textDeltaFromRecord(parsed.value);
      if (delta !== null) {
        emitAgentEvent(active, {
          type: "text_delta",
          turnID: active.turnID,
          delta,
        });
        return;
      }
    }
  }

  const data = parsed.ok ? parsed.value : rawData;
  const rawEnvelopeEvent =
    isRecord(data) && typeof data.type === "string" ? data.type : eventName;
  emitAgentEvent(active, {
    type: "unknown",
    turnID: active.turnID,
    event: normalizeAgentEventName(rawEnvelopeEvent),
  });
}

function explicitTextDelta(rawData: string): string | null {
  const parsed = parseAgentJSON(rawData);
  if (parsed.ok) {
    if (typeof parsed.value === "string") return parsed.value;
    if (isRecord(parsed.value)) return textDeltaFromRecord(parsed.value);
    return null;
  }
  // Plain text is valid for the legacy explicit event form. A JSON-looking
  // fragment is treated as corruption instead of rendered literally.
  const trimmed = rawData.trimStart();
  return trimmed.startsWith("{") || trimmed.startsWith("[")
    ? null
    : rawData;
}

function blockTextDelta(value: unknown): string | null {
  if (!isRecord(value)) return null;
  if (value.type !== "text" && value.type !== "text_delta") return null;
  return textDeltaFromRecord(value);
}

function textDeltaFromRecord(value: Record<string, unknown>): string | null {
  if (typeof value.delta === "string") return value.delta;
  if (typeof value.text === "string") return value.text;
  return null;
}

function doneResult(value: unknown): AgentDoneResult {
  let result = value;
  if (isRecord(value) && value.type === "done") {
    result = Object.prototype.hasOwnProperty.call(value, "result")
      ? value.result
      : value;
  }
  const record = isRecord(result) ? result : null;
  return {
    code: normalizeAgentProtocolLabel(record?.code),
    subtype: normalizeAgentProtocolLabel(record?.subtype),
    is_error: record?.is_error === true,
  };
}

function normalizeAgentProxyError(value: unknown): AgentProxyError | null {
  if (
    !isRecord(value) ||
    !isSafeAgentProtocolString(value.kind, MAX_AGENT_PROXY_KIND_BYTES) ||
    !AGENT_PROXY_ERROR_KINDS.has(value.kind) ||
    typeof value.message !== "string" ||
    !hasWellFormedUTF16(value.message) ||
    utf8ByteLength(value.message) > MAX_AGENT_PROXY_MESSAGE_BYTES
  ) {
    return null;
  }
  const error: AgentProxyError = {
    kind: value.kind,
    message: value.message,
  };
  if (typeof value.retryable === "boolean") error.retryable = value.retryable;
  if (
    typeof value.retry_after_ms === "number" &&
    Number.isSafeInteger(value.retry_after_ms) &&
    value.retry_after_ms >= 0 &&
    value.retry_after_ms <= MAX_AGENT_PROXY_RETRY_AFTER_MS
  ) {
    error.retry_after_ms = value.retry_after_ms;
  }
  if (isSafeAgentProtocolString(value.log_id, MAX_AGENT_PROXY_LOG_ID_BYTES)) {
    error.log_id = value.log_id;
  }
  return error;
}

function normalizeAgentProtocolLabel(value: unknown): string {
  return isSafeAgentProtocolString(
    value,
    MAX_AGENT_PROTOCOL_LABEL_BYTES,
    true
  )
    ? value
    : "";
}

function normalizeAgentEventName(value: unknown): string {
  return isSafeAgentProtocolString(value, 256, true) ? value : "unknown";
}

function isSafeAgentProtocolString(
  value: unknown,
  maxBytes: number,
  allowEmpty = false
): value is string {
  return (
    typeof value === "string" &&
    (allowEmpty || value.length > 0) &&
    hasWellFormedUTF16(value) &&
    utf8ByteLength(value) <= maxBytes &&
    !hasControlCharacter(value)
  );
}

function hasWellFormedUTF16(value: string): boolean {
  for (let index = 0; index < value.length; index += 1) {
    const code = value.charCodeAt(index);
    if (code >= 0xd800 && code <= 0xdbff) {
      const next = value.charCodeAt(index + 1);
      if (!Number.isInteger(next) || next < 0xdc00 || next > 0xdfff) {
        return false;
      }
      index += 1;
      continue;
    }
    if (code >= 0xdc00 && code <= 0xdfff) return false;
  }
  return true;
}

function hasControlCharacter(value: string): boolean {
  for (let index = 0; index < value.length; index += 1) {
    const code = value.charCodeAt(index);
    if (code < 0x20 || code === 0x7f) return true;
  }
  return false;
}

type AgentJSONParseResult =
  | { ok: true; value: unknown }
  | { ok: false };

function parseAgentJSON(value: string): AgentJSONParseResult {
  try {
    return { ok: true, value: JSON.parse(value) as unknown };
  } catch {
    return { ok: false };
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function emitAgentEvent(active: ActiveAgentTurn, event: AgentTurnEvent): void {
  if (active.terminal) return;
  deliverAgentEvent(active, event);
}

function emitAgentTerminal(
  active: ActiveAgentTurn,
  event: AgentTurnEvent
): void {
  if (active.terminal) return;
  active.terminal = true;
  deliverAgentEvent(active, event);
}

function emitAgentProtocolError(
  active: ActiveAgentTurn,
  code: string,
  message: string
): void {
  emitAgentTerminal(active, {
    type: "protocol_error",
    turnID: active.turnID,
    code,
    message,
  });
}

function deliverAgentEvent(
  active: ActiveAgentTurn,
  event: AgentTurnEvent
): void {
  try {
    active.callback(event);
  } catch {
    // Renderer callbacks are outside the transport trust boundary. Stop the
    // request rather than repeatedly invoking a broken callback.
    active.terminal = true;
    active.abortController.abort();
    if (active.reader) {
      void active.reader.cancel("Agent callback failed").catch(() => {});
    }
    // eslint-disable-next-line no-console
    console.error("[preload] Agent turn callback failed");
  }
}

function utf8ByteLength(value: string): number {
  return new TextEncoder().encode(value).byteLength;
}

/** Lightweight ULID-ish id (16 bytes hex, prefix with timestamp).
 *  Good enough for log correlation; not strictly the ULID spec. */
function cryptoRandomULID(): string {
  const ts = Date.now().toString(36);
  const bytes = new Uint8Array(8);
  crypto.getRandomValues(bytes);
  let suffix = "";
  for (const b of bytes) {
    suffix += b.toString(16).padStart(2, "0");
  }
  return `${ts}-${suffix}`;
}

function cryptoRandomV4UUID(): string {
  const value = crypto.randomUUID();
  if (
    !/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/u.test(
      value
    )
  ) {
    throw new Error("crypto.randomUUID returned a non-canonical v4 UUID");
  }
  return value;
}

function parseLoopbackPort(value: string): number | null {
  if (!/^[0-9]+$/u.test(value)) return null;
  const port = Number(value);
  if (!Number.isInteger(port) || port < 1 || port > 65535) return null;
  return port;
}

function isValidLocalToken(value: string): boolean {
  if (value === "" || value.trim() !== value) return false;
  return !/[\u0000-\u001f\u007f]/u.test(value);
}

function buildSidecarURL(path: unknown): string {
  if (typeof path !== "string" || path === "") {
    throw new TypeError("workmaxLocal.fetch path must be a non-empty string");
  }
  if (path.trim() !== path) {
    throw new TypeError("workmaxLocal.fetch path must not include leading or trailing whitespace");
  }
  if (new TextEncoder().encode(path).byteLength > MAX_SIDECAR_RELATIVE_URL_BYTES) {
    throw new TypeError("workmaxLocal.fetch path is too long");
  }
  if (/[\u0000-\u001f\u007f]/u.test(path)) {
    throw new TypeError("workmaxLocal.fetch path contains control characters");
  }
  if (path.includes("#")) {
    throw new TypeError("workmaxLocal.fetch path must not include a fragment");
  }
  if (/^[a-z][a-z0-9+.-]*:/iu.test(path) || path.startsWith("//")) {
    throw new TypeError("workmaxLocal.fetch path must be sidecar-relative");
  }
  return `http://127.0.0.1:${port}${path.startsWith("/") ? path : "/" + path}`;
}

/**
 * Login routes carry either an authorize URL or transient credentials. Keep
 * the complete privileged namespace in Electron main even while the legacy
 * fetch facade remains available for non-privileged Sidecar routes.
 */
function isPrivilegedLoginTransactionURL(rawURL: string): boolean {
  const canonical = canonicalSidecarPathname(rawURL);
  return (
    canonical === "/auth/start" ||
    canonical.startsWith("/auth/start/") ||
    canonical === "/auth/login-transaction" ||
    canonical.startsWith("/auth/login-transaction/")
  );
}

/** Agent streaming and thread mutation have a closed typed facade. The
 * compatibility bridge retains GET history reads but cannot bypass the typed
 * input, idempotency, or event boundaries. */
function isTypedAgentOnlyRequest(
  rawURL: string,
  init?: RequestInit
): boolean {
  const canonical = canonicalSidecarPathname(rawURL);
  const method = String(init?.method ?? "GET").toUpperCase();
  return (
    canonical === "/agent/chat" ||
    canonical.startsWith("/agent/chat/") ||
    canonical === "/agent/skills/catalog" ||
    canonical.startsWith("/agent/skills/catalog/") ||
    canonical === "/agent/turns" ||
    canonical.startsWith("/agent/turns/") ||
    (method === "PUT" && canonical.startsWith("/agent/threads/")) ||
    (method === "POST" &&
      canonical.startsWith("/agent/threads/") &&
      canonical.endsWith("/files"))
  );
}

/** Model route settings carry optional API keys; only the typed facade may call. */
function isTypedSettingsOnlyRequest(rawURL: string): boolean {
  const canonical = canonicalSidecarPathname(rawURL);
  return (
    canonical === "/settings/model-route" ||
    canonical.startsWith("/settings/model-route/")
  );
}

function canonicalSidecarPathname(rawURL: string): string {
  const parsed = new URL(rawURL);
  let pathname = parsed.pathname;
  let decodePass = 0;
  while (pathname.includes("%") && decodePass < MAX_PATH_DECODE_PASSES) {
    let decoded: string;
    try {
      decoded = decodeURIComponent(pathname);
    } catch {
      throw new TypeError("workmaxLocal.fetch path contains invalid URL encoding");
    }
    if (decoded === pathname) {
      break;
    }
    pathname = decoded;
    decodePass += 1;
  }
  if (pathname.includes("%")) {
    throw new TypeError("workmaxLocal.fetch path contains excessive URL encoding");
  }
  return new URL(pathname, parsed.origin).pathname.replace(/\/{2,}/gu, "/");
}
