// shim.js — the renderer's half of the Wails shell.
//
// Electron gave the renderer its bridge through a preload script running in a
// separate world. Wails has no preload, so the same two globals are assembled
// here, in the page, and backed by same-origin requests instead of IPC:
//
//   window.workmaxLocal  — the legacy surface (renderer.js uses only .fetch)
//   window.desktopBridge — the typed facade, built from the SAME
//                          desktop-bridge source the Electron build uses
//                          (lib/desktop-bridge.js is generated from it), so
//                          the contract cannot drift between the two shells.
//
// What is genuinely different from preload, and why:
//
//   - No token. The Go UI server proxies /api/* to the sidecar and injects
//     X-Local-Token itself, so nothing here can read or leak it. Under
//     Electron the token lived in a preload closure; here it never enters the
//     page at all.
//   - Relative URLs. The page is served under an unguessable capability path,
//     so "api/health" resolves correctly and a caller who does not already
//     know the page's own URL cannot reach the API.
//   - Login goes through the /login/ gate rather than the sidecar's login
//     routes, which the proxy refuses. That gate is the analogue of Electron's
//     Main-only IPC handler.
//
// Design: ProjectDocs/wails-migration-evaluation-2026-08.md §12, §0.5.9-0.5.13.

// Loaded as a classic script, in order, before renderer.js — same as the
// bridge library it depends on. A module would defer past renderer.js and
// would not load at all over file://, which the Electron build still uses.
(function () {
"use strict";

// Under Electron the preload already installed both globals with an IPC-backed
// implementation. Installing this same-origin version on top of it would break
// that shell, so stand down instead. Deleted along with Electron.
if (window.workmaxLocal) return;

const createDesktopBridge = window.__workmaxDesktopBridge && window.__workmaxDesktopBridge.createDesktopBridge;
if (typeof createDesktopBridge !== "function") {
  console.error("[shim] lib/desktop-bridge.js must load before shim.js");
  return;
}

const API_PREFIX = "api";
const LOGIN_PREFIX = "login";
const OPEN_EXTERNAL_PATH = "open-external";

// Streams live for the length of a model turn, so a frame that is merely large
// must not be mistaken for a stream that has gone wrong. These mirror the
// preload limits exactly.
const MAX_AGENT_SSE_FRAME_BYTES = 1 << 20;
const MAX_AGENT_HTTP_ERROR_BYTES = 64 << 10;
const MAX_ACTIVE_AGENT_TURNS = 8;

// resolve turns a bridge route path ("/agent/threads") into a URL relative to
// the capability base the page was loaded under. document.baseURI is the
// anchor: it already carries the capability segment.
function resolve(prefix, path) {
  return new URL(prefix + (path.startsWith("/") ? path : "/" + path), document.baseURI).toString();
}

function utf8ByteLength(value) {
  return new TextEncoder().encode(value).byteLength;
}

function isRecord(value) {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function randomV4UUID() {
  if (crypto.randomUUID) return crypto.randomUUID();
  const b = crypto.getRandomValues(new Uint8Array(16));
  b[6] = (b[6] & 0x0f) | 0x40;
  b[8] = (b[8] & 0x3f) | 0x80;
  const hex = [...b].map((x) => x.toString(16).padStart(2, "0")).join("");
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
}

// ---------------------------------------------------------------------------
// Request adapter
// ---------------------------------------------------------------------------

// bridgeResponse mirrors preload's LegacyBridgeResponse. The body is exposed
// as a getReader thunk rather than the raw stream because that is the only
// part of it the typed bridge and renderer ever use, and it keeps the shape
// identical to what renderer.js already handles.
function bridgeResponse(response) {
  let textCache = null;
  const readText = () => {
    if (!textCache) textCache = response.text();
    return textCache;
  };
  return {
    ok: response.ok,
    status: response.status,
    statusText: response.statusText,
    url: response.url,
    headers: Object.fromEntries(response.headers.entries()),
    body: response.body ? { getReader: () => response.body.getReader() } : null,
    text: readText,
    json: async () => {
      const text = await readText();
      return text === "" ? null : JSON.parse(text);
    },
  };
}

async function fetchSidecarResponse(path, init) {
  const headers = new Headers(init && init.headers);
  if (!headers.has("X-Request-ID")) headers.set("X-Request-ID", randomV4UUID());
  return fetch(resolve(API_PREFIX, path), {
    ...init,
    credentials: "omit",
    // A redirect would move the request off the path the privileged-route
    // check examined, so refuse rather than follow. The server refuses to
    // issue them too; this is the second half of that agreement.
    redirect: "error",
    headers,
  });
}

async function sidecarFetch(path, init) {
  return bridgeResponse(await fetchSidecarResponse(path, init));
}

// ---------------------------------------------------------------------------
// Login gate
// ---------------------------------------------------------------------------

// The renderer never chooses a method or a path: it names one of four verbs.
// Everything privileged about the flow — which sidecar route that becomes, the
// flow ID, the credential handling — stays in Go.
async function loginVerb(verb, payload) {
  try {
    const response = await fetch(resolve(LOGIN_PREFIX, "/" + verb), {
      method: "POST",
      credentials: "omit",
      redirect: "error",
      headers: payload ? { "Content-Type": "application/json" } : undefined,
      body: payload ? JSON.stringify(payload) : undefined,
    });
    const text = await response.text();
    const parsed = text === "" ? null : JSON.parse(text);
    if (isRecord(parsed) && typeof parsed.state === "string") return parsed;
    return { state: "idle", error: "unavailable" };
  } catch {
    return { state: "idle", error: "unavailable" };
  }
}

// submitLoginPassword takes a fresh copy of exactly the two expected fields
// and drops the caller's object, so a renderer bug cannot smuggle extra keys
// into the credential body.
function submitLoginPassword(input) {
  if (!isRecord(input) || typeof input.email !== "string" || typeof input.password !== "string") {
    return Promise.resolve({ state: "idle", error: "invalid_request" });
  }
  return loginVerb("password", { email: input.email, password: input.password });
}

// ---------------------------------------------------------------------------
// Agent turn streaming
// ---------------------------------------------------------------------------

// AgentSSEParser is a line-exact port of the preload parser. Its behaviour was
// verified byte-for-byte against a Go control through WKWebView before this
// file existed (design doc §0.5.9): multi-byte UTF-8 and surrogate pairs split
// across chunks, embedded newlines, and a literal "data:" inside a payload all
// survive.
class AgentSSEParser {
  constructor(active) {
    this.active = active;
    this.decoder = new TextDecoder("utf-8", { fatal: true });
    this.buffer = "";
    this.frameBytes = 0;
    this.eventName = "";
    this.dataLines = [];
  }

  push(chunk) {
    if (this.active.terminal) return;
    try {
      this.buffer += this.decoder.decode(chunk, { stream: true });
    } catch {
      this.fail("invalid_utf8", "Agent stream contains invalid UTF-8");
      return;
    }
    this.drain(false);
  }

  finish() {
    if (this.active.terminal) return;
    try {
      this.buffer += this.decoder.decode();
    } catch {
      this.fail("invalid_utf8", "Agent stream ended with invalid UTF-8");
      return;
    }
    this.drain(true);
  }

  drain(final) {
    while (!this.active.terminal) {
      const ending = this.findLineEnding(final);
      if (!ending) break;
      const line = this.buffer.slice(0, ending.index);
      const delimiter = this.buffer.slice(ending.index, ending.index + ending.length);
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

    if (this.frameBytes + utf8ByteLength(this.buffer) > MAX_AGENT_SSE_FRAME_BYTES) {
      this.fail("frame_too_large", `Agent SSE frame exceeds ${MAX_AGENT_SSE_FRAME_BYTES} bytes`);
      return;
    }

    // Tolerate a final record without the conventional blank line, while still
    // requiring a terminal semantic event at the stream level.
    if (final && this.dataLines.length > 0) {
      this.dispatchFrame();
      this.resetFrame();
    }
  }

  // A lone \r at the very end of a chunk is held back: the next chunk decides
  // whether it was a line ending or the first half of \r\n.
  findLineEnding(final) {
    for (let index = 0; index < this.buffer.length; index += 1) {
      const char = this.buffer[index];
      if (char === "\n") return { index, length: 1 };
      if (char !== "\r") continue;
      if (index + 1 === this.buffer.length && !final) return null;
      return { index, length: this.buffer[index + 1] === "\n" ? 2 : 1 };
    }
    return null;
  }

  consumeLine(line, delimiter) {
    this.frameBytes += utf8ByteLength(line) + utf8ByteLength(delimiter);
    if (this.frameBytes > MAX_AGENT_SSE_FRAME_BYTES) {
      this.fail("frame_too_large", `Agent SSE frame exceeds ${MAX_AGENT_SSE_FRAME_BYTES} bytes`);
      return;
    }
    if (line === "") {
      if (this.dataLines.length > 0) this.dispatchFrame();
      this.resetFrame();
      return;
    }
    if (line.startsWith(":")) return;

    const colon = line.indexOf(":");
    const field = colon < 0 ? line : line.slice(0, colon);
    let value = colon < 0 ? "" : line.slice(colon + 1);
    if (value.startsWith(" ")) value = value.slice(1);
    if (field === "event") this.eventName = value;
    else if (field === "data") this.dataLines.push(value);
  }

  dispatchFrame() {
    dispatchAgentSSEFrame(
      this.active,
      this.eventName === "" ? "message" : this.eventName,
      this.dataLines.join("\n")
    );
  }

  resetFrame() {
    this.frameBytes = 0;
    this.eventName = "";
    this.dataLines = [];
  }

  fail(code, message) {
    this.buffer = "";
    this.resetFrame();
    emitAgentProtocolError(this.active, code, message);
  }
}

function emit(active, event) {
  if (active.delivered) return;
  try {
    active.callback(event);
  } catch {
    // A throwing renderer callback must not abort the stream loop or leave the
    // turn registered forever.
  }
}

function emitAgentTerminal(active, event) {
  if (active.terminal) return;
  active.terminal = true;
  emit(active, event);
}

function emitAgentProtocolError(active, code, message) {
  emitAgentTerminal(active, {
    type: "protocol_error",
    turnID: active.turnID,
    error: { code, message },
  });
}

function parseAgentJSON(raw) {
  try {
    return { ok: true, value: JSON.parse(raw) };
  } catch {
    return { ok: false, value: null };
  }
}

// Bounds on the provenance list. The sidecar is on the same machine and is
// trusted to be correct, not trusted to stay within limits forever: these keep
// a future top-k change from rendering an unbounded list into the panel.
const MAX_RETRIEVAL_SOURCES = 12;
const MAX_RETRIEVAL_LABEL_CHARS = 120;
const MAX_RETRIEVAL_SNIPPET_CHARS = 400;

// parseRetrievalSources returns a normalized list, or null if the payload is
// not the shape this renderer understands. Every field is re-derived rather
// than passed through, so nothing reaches the DOM that was not checked here.
function parseRetrievalSources(raw) {
  if (!Array.isArray(raw)) return null;
  const out = [];
  for (const entry of raw.slice(0, MAX_RETRIEVAL_SOURCES)) {
    if (!isRecord(entry)) return null;
    if (typeof entry.label !== "string" || entry.label === "") return null;
    if (entry.kind !== "file" && entry.kind !== "conversation") return null;
    const score = typeof entry.score === "number" && Number.isFinite(entry.score)
      ? Math.min(1, Math.max(0, entry.score))
      : null;
    out.push({
      kind: entry.kind,
      label: entry.label.slice(0, MAX_RETRIEVAL_LABEL_CHARS),
      snippet: typeof entry.snippet === "string" ? entry.snippet.slice(0, MAX_RETRIEVAL_SNIPPET_CHARS) : "",
      score,
    });
  }
  return out;
}

function dispatchAgentSSEFrame(active, eventName, rawData) {
  if (active.terminal) return;

  if (eventName === "text_delta") {
    const parsed = parseAgentJSON(rawData);
    if (!parsed.ok || !isRecord(parsed.value) || typeof parsed.value.delta !== "string") {
      emitAgentProtocolError(active, "invalid_event", "Agent text_delta payload is malformed");
      return;
    }
    emit(active, { type: "text_delta", turnID: active.turnID, delta: parsed.value.delta });
    return;
  }
  if (eventName === "tool_use" || eventName === "tool_denied") {
    const parsed = parseAgentJSON(rawData);
    const value = parsed.ok && isRecord(parsed.value) ? parsed.value : null;
    // Informational, like retrieval: a malformed activity frame costs the
    // narration, never the answer.
    if (!value || typeof value.name !== "string" || value.name === "" || value.name.length > 64) {
      return;
    }
    const event = { type: eventName, turnID: active.turnID, name: value.name };
    event.target = typeof value.target === "string" ? value.target.slice(0, 80) : "";
    if (eventName === "tool_denied") {
      event.reason = typeof value.reason === "string" ? value.reason.slice(0, 300) : "";
    }
    emit(active, event);
    return;
  }
  if (eventName === "retrieval") {
    const parsed = parseAgentJSON(rawData);
    const sources = parsed.ok && isRecord(parsed.value) ? parseRetrievalSources(parsed.value.sources) : null;
    if (!sources) {
      // Non-terminal and purely informational: a malformed payload costs the
      // user the provenance list, not the answer. Failing the turn over it
      // would trade a working reply for a cosmetic guarantee.
      return;
    }
    emit(active, { type: "retrieval", turnID: active.turnID, sources });
    return;
  }
  if (eventName === "done") {
    emitAgentTerminal(active, { type: "done", turnID: active.turnID });
    return;
  }
  if (eventName === "canceled") {
    emitAgentTerminal(active, { type: "canceled", turnID: active.turnID });
    return;
  }
  if (eventName === "proxy_error") {
    const parsed = parseAgentJSON(rawData);
    const value = parsed.ok && isRecord(parsed.value) ? parsed.value : null;
    if (!value) {
      emitAgentProtocolError(active, "invalid_event", "Agent proxy_error payload is malformed");
      return;
    }
    emitAgentTerminal(active, {
      type: "proxy_error",
      turnID: active.turnID,
      error: {
        kind: typeof value.kind === "string" ? value.kind : "unknown",
        message: typeof value.message === "string" ? value.message : "Agent request failed",
        retryable: value.retryable === true,
      },
    });
    return;
  }
  // Unknown event names are surfaced rather than dropped: a server that grew a
  // new terminal event should not look like a stream that simply stopped.
  emit(active, { type: "unknown", turnID: active.turnID, event: eventName });
}

async function consumeAgentTurn(turns, active, source) {
  try {
    const response = await fetchSidecarResponse(source.path, {
      ...source.init,
      signal: active.abortController.signal,
    });
    if (active.terminal) {
      if (response.body) await response.body.cancel().catch(() => {});
      return;
    }
    if (!response.ok) {
      const payload = await readBoundedErrorJSON(response);
      if (isRecord(payload) && payload.error === "session_changed") {
        emitAgentTerminal(active, {
          type: "proxy_error",
          turnID: active.turnID,
          error: { kind: "session_changed", message: "Desktop session changed", retryable: false },
        });
        return;
      }
      emitAgentProtocolError(active, "http_error", `Agent Sidecar returned HTTP ${response.status}`);
      return;
    }
    const contentType = (response.headers.get("content-type") || "").split(";", 1)[0].trim().toLowerCase();
    if (contentType !== "text/event-stream") {
      if (response.body) await response.body.cancel().catch(() => {});
      emitAgentProtocolError(active, "invalid_content_type", "Agent Sidecar response is not text/event-stream");
      return;
    }
    if (!response.body) {
      emitAgentProtocolError(active, "missing_stream", "Agent Sidecar response has no stream body");
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
          emitAgentProtocolError(active, "unexpected_eof", "Agent stream ended without a terminal event");
        }
        break;
      }
      if (chunk.value) parser.push(chunk.value);
    }

    // A terminal frame is a hard fence: stop the source at once so bytes after
    // done/proxy_error cannot become a late renderer callback.
    if (active.terminal && !active.cancelRequested) {
      await reader.cancel("Agent terminal event received").catch(() => {});
    }
  } catch (error) {
    if (active.terminal || active.cancelRequested) return;
    emitAgentProtocolError(
      active,
      "transport_error",
      error && error.name === "AbortError" ? "Agent transport was aborted" : "Agent transport failed"
    );
  } finally {
    if (active.reader) {
      try {
        active.reader.releaseLock();
      } catch {
        // A concurrently cancelled stream may already have released its lock.
      }
    }
    if (turns.get(active.turnID) === active) turns.delete(active.turnID);
  }
}

async function readBoundedErrorJSON(response) {
  if (!response.body) return null;
  const reader = response.body.getReader();
  const chunks = [];
  let total = 0;
  try {
    for (;;) {
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
    return text === "" ? null : JSON.parse(text);
  } catch {
    return null;
  } finally {
    try {
      reader.releaseLock();
    } catch {
      // A size rejection may already have released the lock.
    }
  }
}

const activeAgentTurns = new Map();

function openTurn(request, callback, makeSource) {
  for (const [turnID, active] of activeAgentTurns) {
    if (active.terminal) activeAgentTurns.delete(turnID);
  }
  if (activeAgentTurns.size >= MAX_ACTIVE_AGENT_TURNS) {
    throw new RangeError(`agent turn limit of ${MAX_ACTIVE_AGENT_TURNS} reached`);
  }
  const turnID = request.turnID;
  const active = {
    turnID,
    callback,
    abortController: new AbortController(),
    reader: null,
    terminal: false,
    cancelRequested: false,
  };
  activeAgentTurns.set(turnID, active);
  void consumeAgentTurn(activeAgentTurns, active, makeSource(turnID));
  return { turnID };
}

function startAgentTurn(request, callback) {
  const turnID = randomV4UUID();
  return openTurn({ turnID }, callback, () => ({
    path: "/agent/chat",
    init: {
      method: "POST",
      headers: { "Content-Type": "application/json", Accept: "text/event-stream" },
      body: JSON.stringify({ turn_uuid: turnID, ...request }),
    },
  }));
}

function resumeAgentTurn(turnUUID, callback) {
  return openTurn({ turnID: turnUUID }, callback, () => ({
    path: `/agent/turns/${encodeURIComponent(turnUUID)}/replay`,
    init: { method: "POST", headers: { Accept: "text/event-stream" } },
  }));
}

async function cancelAgentTurn(turnID) {
  const active = activeAgentTurns.get(turnID);
  if (active) {
    active.cancelRequested = true;
    active.abortController.abort();
  }
  let canceled = false;
  try {
    const response = await fetchSidecarResponse(
      `/agent/turns/${encodeURIComponent(turnID)}/cancel`,
      { method: "POST" }
    );
    canceled = response.ok;
  } catch {
    // The local abort above already stopped delivery; a failed server-side
    // cancel only means the turn may finish upstream unobserved.
  }
  if (active) emitAgentTerminal(active, { type: "canceled", turnID });
  return { turnID, canceled };
}

// ---------------------------------------------------------------------------
// Assembly
// ---------------------------------------------------------------------------

const runtime = {
  sidecarVersion: document.documentElement.dataset.sidecarVersion || "unknown",
  appVersion: document.documentElement.dataset.appVersion || "unknown",
  platform: "darwin",
};

// revealDataDir is declared but unused by the renderer (design decision D5).
// It is stubbed rather than dropped because the typed bridge's dependency
// shape requires it; a caller gets an honest refusal, not a broken promise.
const revealDataDir = async () => ({ ok: false, error: "not supported in this shell" });

// --------------------------------------------------------------------------
// External links
// --------------------------------------------------------------------------

// macOS gives this shell no way to cancel a navigation once it starts, so a
// plain <a href="https://..."> would move the window itself — and because the
// origin only serves what is under the capability path, the app would be
// replaced by a remote page with no way back.
//
// The click is therefore stopped here, in the capture phase so a handler that
// stops propagation cannot skip it, and the URL is handed to Go to open in the
// system browser. Go re-validates: nothing the page sends is trusted to be
// external just because this code thinks it is.
function isExternalHref(anchor) {
  const href = anchor.getAttribute("href") || "";
  if (href === "" || href.startsWith("#")) return false;
  let resolved;
  try {
    resolved = new URL(href, document.baseURI);
  } catch {
    return false;
  }
  if (resolved.protocol !== "http:" && resolved.protocol !== "https:") return false;
  return resolved.origin !== location.origin;
}

document.addEventListener(
  "click",
  (event) => {
    const anchor = event.target && event.target.closest && event.target.closest("a[href]");
    if (!anchor || !isExternalHref(anchor)) return;
    event.preventDefault();
    const target = new URL(anchor.getAttribute("href"), document.baseURI).toString();
    // Not resolve(): that helper adds a leading slash to build API paths, and
    // the trailing form it produces would miss the exact-path route.
    fetch(new URL(OPEN_EXTERNAL_PATH, document.baseURI).toString(), {
      method: "POST",
      credentials: "omit",
      redirect: "error",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ url: target }),
    }).catch(() => {
      // Nothing to fall back to: opening it here is the navigation this
      // handler exists to prevent.
    });
  },
  true
);

window.workmaxLocal = {
  sidecarVersion: runtime.sidecarVersion,
  appVersion: runtime.appVersion,
  platform: runtime.platform,
  fetch: sidecarFetch,
  revealDataDir,
};

window.desktopBridge = createDesktopBridge({
  runtime,
  request: sidecarFetch,
  beginLogin: () => loginVerb("begin"),
  loginStatus: () => loginVerb("status"),
  submitLoginPassword,
  cancelLogin: () => loginVerb("cancel"),
  startAgentTurn,
  resumeAgentTurn,
  cancelAgentTurn,
  revealDataDir,
});
})();
