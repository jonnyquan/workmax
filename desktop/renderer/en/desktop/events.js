// The turn stream: what a frame is allowed to be, and what happens when one
// arrives.
//
// Validation and dispatch sit together on purpose. Every field the renderer
// reads out of an event is checked here first (parseAgentTurnEvent), and the
// only consumer of that check is a few hundred lines below it, so a field
// added to one half without the other is visible in a single file.
//
// It also owns the two things that make a long answer cheap: the rAF batch
// that turns N deltas per frame into one DOM append, and the streaming commit
// boundary that parses completed Markdown blocks as they close instead of
// re-parsing the whole answer on every token.
import {
  MAX_TURN_TEXT_BYTES,
  hasExactKeys,
  hasWellFormedUTF16,
  isNonNegativeInteger,
  isRecord,
  isSafeLocalHistoryUUID,
  isSafeProtocolString,
  utf8ByteLength,
} from "./protocol.js";
import {
  MARKDOWN_FENCE,
  MARKDOWN_MAX_CHARS,
  isClosingFence,
  parseMarkdownBlocks,
  renderMarkdownInto,
} from "./markdown.js";
import { scrollMessagesToEnd } from "./transcript.js";
import { removeRecoverableTurn } from "./composer.js";
import {
  paintReasoningCaption,
  presentApprovalRequest,
  recordReasoningDelta,
  recordToolActivity,
  recordToolResult,
  setRetrievedContext,
} from "./context-panel.js";
import { noteMindActivity } from "./mind.js";
import {
  failActiveTurnProtocol,
  finishActiveTurn,
  finishActiveTurnWithError,
  handleInitialTurnBusy,
  handleSessionChanged,
  isCurrentTurn,
  isThreadBusyResult,
  keepRecoverableTurnForRetry,
  recoveredTurnErrorMessage,
  state,
} from "./renderer.js";

const PROXY_ERROR_KINDS = new Set([
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
const AGENT_EVENT_TYPES = new Set([
  "text_delta",
  "reasoning_delta",
  "turn_meta",
  "retrieval",
  "tool_use",
  "tool_denied",
  "tool_result",
  "approval_request",
  "unknown",
  "done",
  "proxy_error",
  "canceled",
  "protocol_error",
]);
// Bounds for the retrieval provenance list. The shim already applies its own;
// these are not a duplicate of that check but the same check at the other end
// of the bridge, which is the boundary this file is responsible for. The
// renderer trusts no bridge — that is why parseAgentTurnEvent exists at all.
const MAX_RETRIEVED_SOURCES = 12;
const MAX_RETRIEVED_LABEL_CHARS = 120;
const MAX_RETRIEVED_SNIPPET_CHARS = 400;
const RETRIEVED_SOURCE_KINDS = new Set(["file", "conversation"]);
const MAX_EVENT_TEXT_BYTES = 262_144;

function parseProxyError(value) {
  if (!isRecord(value)) {
    throw new Error("Malformed agent turn event");
  }
  const allowedKeys = new Set([
    "kind",
    "message",
    "retryable",
    "retry_after_ms",
    "log_id",
  ]);
  if (Object.keys(value).some((key) => !allowedKeys.has(key))) {
    throw new Error("Malformed agent turn event");
  }
  if (
    !isSafeProtocolString(value.kind, 64) ||
    !PROXY_ERROR_KINDS.has(value.kind) ||
    typeof value.message !== "string" ||
    !hasWellFormedUTF16(value.message) ||
    utf8ByteLength(value.message) > 4096 ||
    (value.retryable !== undefined && typeof value.retryable !== "boolean") ||
    (value.retry_after_ms !== undefined &&
      (!isNonNegativeInteger(value.retry_after_ms) || value.retry_after_ms > 86_400_000)) ||
    (value.log_id !== undefined && !isSafeProtocolString(value.log_id, 256, true))
  ) {
    throw new Error("Malformed agent turn event");
  }
  const error = {
    kind: value.kind,
    message: value.message,
  };
  if (typeof value.retryable === "boolean") error.retryable = value.retryable;
  if (value.retry_after_ms !== undefined) error.retry_after_ms = value.retry_after_ms;
  if (value.log_id !== undefined) error.log_id = value.log_id;
  return error;
}

function parseSafeDoneResult(value) {
  if (
    !hasExactKeys(value, ["code", "subtype", "is_error"]) ||
    !isSafeProtocolString(value.code, 128, true) ||
    !isSafeProtocolString(value.subtype, 128, true) ||
    typeof value.is_error !== "boolean"
  ) {
    throw new Error("Malformed agent turn event");
  }
  return {
    code: value.code,
    subtype: value.subtype,
    isError: value.is_error,
  };
}

function parseSafeRetrievedSource(value) {
  if (
    !hasExactKeys(value, ["kind", "label", "snippet", "score"]) ||
    !RETRIEVED_SOURCE_KINDS.has(value.kind) ||
    !isSafeProtocolString(value.label, MAX_RETRIEVED_LABEL_CHARS) ||
    typeof value.snippet !== "string" ||
    !hasWellFormedUTF16(value.snippet) ||
    value.snippet.length > MAX_RETRIEVED_SNIPPET_CHARS ||
    !(value.score === null || (typeof value.score === "number" && value.score >= 0 && value.score <= 1))
  ) {
    throw new Error("Malformed agent turn event");
  }
  return {
    kind: value.kind,
    label: value.label,
    snippet: value.snippet,
    score: value.score,
  };
}

export function parseAgentTurnEvent(value) {
  if (
    !isRecord(value) ||
    typeof value.type !== "string" ||
    !AGENT_EVENT_TYPES.has(value.type) ||
    !isSafeLocalHistoryUUID(value.turnID)
  ) {
    throw new Error("Malformed agent turn event");
  }
  switch (value.type) {
    case "text_delta":
      if (
        !hasExactKeys(value, ["type", "turnID", "delta"]) ||
        typeof value.delta !== "string" ||
        !hasWellFormedUTF16(value.delta) ||
        utf8ByteLength(value.delta) > MAX_EVENT_TEXT_BYTES
      ) {
        throw new Error("Malformed agent turn event");
      }
      return { type: value.type, turnID: value.turnID, delta: value.delta };
    case "reasoning_delta":
      if (
        !hasExactKeys(value, ["type", "turnID", "delta"]) ||
        typeof value.delta !== "string" ||
        !hasWellFormedUTF16(value.delta) ||
        utf8ByteLength(value.delta) > MAX_EVENT_TEXT_BYTES
      ) {
        throw new Error("Malformed agent turn event");
      }
      return { type: value.type, turnID: value.turnID, delta: value.delta };
    case "turn_meta":
      // Provenance. The engine name is a closed vocabulary the sidecar owns,
      // so it is held to the strict guard; the model is whatever the user
      // typed into settings, so it is checked for shape and length and not
      // against a vocabulary nobody owns. Empty model is a real answer — the
      // engine chose its own default — not a missing one.
      if (
        !hasExactKeys(value, ["type", "turnID", "engine", "model"]) ||
        !isSafeProtocolString(value.engine, 32) ||
        !isSafeProtocolString(value.model, 80, true)
      ) {
        throw new Error("Malformed agent turn event");
      }
      return {
        type: value.type,
        turnID: value.turnID,
        engine: value.engine,
        model: value.model,
      };
    case "approval_request":
      if (
        !hasExactKeys(value, ["type", "turnID", "id", "name", "target"]) ||
        !isSafeProtocolString(value.id, 32) ||
        !isSafeProtocolString(value.name, 64) ||
        typeof value.target !== "string" ||
        !hasWellFormedUTF16(value.target) ||
        value.target.length > 80
      ) {
        throw new Error("Malformed agent turn event");
      }
      return { type: value.type, turnID: value.turnID, id: value.id, name: value.name, target: value.target };
    case "tool_use":
      if (
        !hasExactKeys(value, ["type", "turnID", "name", "target"]) ||
        !isSafeProtocolString(value.name, 64) ||
        typeof value.target !== "string" ||
        !hasWellFormedUTF16(value.target) ||
        value.target.length > 80
      ) {
        throw new Error("Malformed agent turn event");
      }
      return { type: value.type, turnID: value.turnID, name: value.name, target: value.target };
    case "tool_result":
      if (
        !hasExactKeys(value, ["type", "turnID", "name", "target", "isError"]) ||
        !isSafeProtocolString(value.name, 64) ||
        typeof value.target !== "string" ||
        !hasWellFormedUTF16(value.target) ||
        value.target.length > 80 ||
        typeof value.isError !== "boolean"
      ) {
        throw new Error("Malformed agent turn event");
      }
      return {
        type: value.type,
        turnID: value.turnID,
        name: value.name,
        target: value.target,
        isError: value.isError,
      };
    case "tool_denied":
      if (
        !hasExactKeys(value, ["type", "turnID", "name", "target", "reason"]) ||
        !isSafeProtocolString(value.name, 64) ||
        typeof value.target !== "string" ||
        value.target.length > 80 ||
        typeof value.reason !== "string" ||
        !hasWellFormedUTF16(value.reason) ||
        value.reason.length > 300
      ) {
        throw new Error("Malformed agent turn event");
      }
      return { type: value.type, turnID: value.turnID, name: value.name, target: value.target, reason: value.reason };
    case "retrieval":
      if (
        !hasExactKeys(value, ["type", "turnID", "sources"]) ||
        !Array.isArray(value.sources) ||
        value.sources.length > MAX_RETRIEVED_SOURCES
      ) {
        throw new Error("Malformed agent turn event");
      }
      return {
        type: value.type,
        turnID: value.turnID,
        sources: value.sources.map(parseSafeRetrievedSource),
      };
    case "unknown":
      if (
        !hasExactKeys(value, ["type", "turnID", "event"]) ||
        !isSafeProtocolString(value.event, 256, true)
      ) {
        throw new Error("Malformed agent turn event");
      }
      return { type: value.type, turnID: value.turnID, event: value.event };
    case "done":
      if (!hasExactKeys(value, ["type", "turnID", "result"])) {
        throw new Error("Malformed agent turn event");
      }
      return {
        type: value.type,
        turnID: value.turnID,
        result: parseSafeDoneResult(value.result),
      };
    case "proxy_error":
      if (!hasExactKeys(value, ["type", "turnID", "error"])) {
        throw new Error("Malformed agent turn event");
      }
      return {
        type: value.type,
        turnID: value.turnID,
        error: parseProxyError(value.error),
      };
    case "canceled":
      if (!hasExactKeys(value, ["type", "turnID"])) {
        throw new Error("Malformed agent turn event");
      }
      return { type: value.type, turnID: value.turnID };
    case "protocol_error":
      if (
        !hasExactKeys(value, ["type", "turnID", "code", "message"]) ||
        !isSafeProtocolString(value.code, 128) ||
        typeof value.message !== "string" ||
        !hasWellFormedUTF16(value.message) ||
        utf8ByteLength(value.message) > 4096
      ) {
        throw new Error("Malformed agent turn event");
      }
      return {
        type: value.type,
        turnID: value.turnID,
        message: value.message,
      };
  }
  throw new Error("Malformed agent turn event");
}

export function handleRawTurnEvent(activeTurn, rawEvent) {
  if (!isCurrentTurn(activeTurn)) return;
  let event;
  try {
    event = parseAgentTurnEvent(rawEvent);
  } catch {
    failActiveTurnProtocol(activeTurn, "The Agent stream returned an invalid event.");
    return;
  }
  handleParsedTurnEvent(activeTurn, event);
}

// The typing indicator ends the moment there is anything better to show —
// the first token, or any terminal outcome.
export function clearPendingIndicator(activeTurn) {
  activeTurn.assistantBubble?.parentNode?.classList?.remove("pending");
}

// --- Streamed-frame batching ------------------------------------------------
//
// Token deltas arrive far faster than a screen can show them. Painting each
// one meant, per token: a full-string textContent reset (quadratic over the
// answer), a selector walk, and a forced layout for the scroll write. The
// batcher makes the DOM a per-frame concern instead: deltas accumulate in a
// buffer and one rAF flush appends the merged chunk to a dedicated Text node
// — appendData only ever adds, so the cost of a delta no longer grows with
// everything that came before it.
//
// Causal order is a discipline, not an accident: every non-delta event
// (tool_use, approval, done, errors) drains the buffer synchronously before
// it is handled, so nothing the model said can appear after something it did.

// The behavior suite runs this file in a bare VM with no rAF; a microtask is
// the closest honest stand-in — still strictly after the current burst of
// synchronous deltas, still before the test's next settle().
const scheduleStreamFrame =
  typeof globalThis.requestAnimationFrame === "function"
    ? (callback) => globalThis.requestAnimationFrame(callback)
    : (callback) => {
        void Promise.resolve().then(callback);
      };

// Module state rather than renderer `state`: this is paint plumbing, and it
// must never ride along when session-change hygiene serializes `state`.
export const streamBatch = {
  turn: null,
  text: "",
  reasoningDirty: false,
  scheduled: false,
};

export function scheduleStreamFlush(activeTurn) {
  // A new turn's first delta can arrive before a superseded turn's buffer
  // ever flushed. Its words belong to a bubble that is gone; drop them
  // rather than letting them lead another turn's answer.
  if (streamBatch.turn !== activeTurn) {
    streamBatch.turn = activeTurn;
    streamBatch.text = "";
    streamBatch.reasoningDirty = false;
  }
  if (streamBatch.scheduled) return;
  // The sentinel prevents one frame from being scheduled per delta; the
  // frame that runs picks up everything queued behind it.
  streamBatch.scheduled = true;
  scheduleStreamFrame(flushStreamBatch);
}

function flushStreamBatch() {
  // Snapshot, then reset, then apply: a handler that enqueues during the
  // apply phase re-schedules cleanly instead of corrupting this flush.
  const turn = streamBatch.turn;
  const text = streamBatch.text;
  const reasoningDirty = streamBatch.reasoningDirty;
  streamBatch.turn = null;
  streamBatch.text = "";
  streamBatch.reasoningDirty = false;
  streamBatch.scheduled = false;
  // A superseded turn's buffered words are dropped, not painted: its bubble
  // may already belong to another thread's transcript.
  if (!turn || !isCurrentTurn(turn)) return;
  if (text !== "") {
    appendStreamText(turn, text);
    // At most one scroll write per flush — and only for a reader who is
    // already following; the jump affordance handles everyone else.
    scrollMessagesToEnd();
  }
  if (reasoningDirty) paintReasoningCaption(turn);
}

// drainStreamBatch is the causal fence. Any code about to reflect a
// non-delta fact into the DOM calls this first so buffered text lands ahead
// of it, in order.
export function drainStreamBatch() {
  if (streamBatch.text === "" && !streamBatch.reasoningDirty) return;
  flushStreamBatch();
}

// --- Streaming Markdown commit ----------------------------------------------
//
// While a turn streams, the bubble is two zones: blocks that are finished and
// already typeset, then a raw pre-wrap tail holding the text still being
// written. A block is only committed once a blank line outside any open code
// fence closes it — before that its final shape is ambiguous, and committing
// early is how streamed answers visibly change shape. The tail is a real Text
// node so the per-frame append stays an appendData, never a re-parse.

// Past this many characters, commits happen every few frames instead of every
// frame: the boundary scan re-reads the tail, and a very long unbroken tail
// should not be re-scanned 60 times a second.
const STREAM_COMMIT_EAGER_CHARS = 8192;
const STREAM_COMMIT_FRAME_STRIDE = 4;

function ensureStreamTail(activeTurn) {
  const bubble = activeTurn.assistantBubble;
  const tail = activeTurn.streamTail;
  if (tail && tail.wrap.parentNode === bubble) return tail;
  // First streamed text: replace whatever placeholder the bubble held.
  bubble.textContent = "";
  const textNode = document.createTextNode("");
  const wrap = document.createElement("span");
  wrap.className = "md-stream-tail";
  wrap.appendChild(textNode);
  bubble.appendChild(wrap);
  activeTurn.streamTail = {
    wrap,
    textNode,
    committedChars: 0,
    framesSinceCommit: 0,
  };
  return activeTurn.streamTail;
}

function appendStreamText(activeTurn, chunk) {
  const tail = ensureStreamTail(activeTurn);
  tail.textNode.appendData(chunk);
  tail.framesSinceCommit += 1;
  const throttled =
    activeTurn.assistantText.length > STREAM_COMMIT_EAGER_CHARS &&
    tail.framesSinceCommit < STREAM_COMMIT_FRAME_STRIDE;
  if (!throttled) commitStreamBlocks(activeTurn);
}

// The last position in tailText (an offset just past a newline) up to which
// the text parses to exactly the blocks a final full parse would produce.
// That is any blank line at fence-depth zero: this parser never lets a block
// other than a fence span one. Inside an open fence nothing commits — the
// whole unclosed block stays in the tail rather than flickering into a
// half-rendered shape.
function streamCommitBoundary(tailText) {
  const lastNewline = tailText.lastIndexOf("\n");
  if (lastNewline < 0) return 0;
  const lines = tailText.slice(0, lastNewline).split("\n");
  let openFence = null;
  let boundary = 0;
  let offset = 0;
  for (const line of lines) {
    const lineEnd = offset + line.length + 1;
    if (openFence) {
      if (isClosingFence(line, openFence)) openFence = null;
    } else {
      const fence = MARKDOWN_FENCE.exec(line);
      if (fence) {
        openFence = fence[1];
      } else if (line.trim() === "") {
        boundary = lineEnd;
      }
    }
    offset = lineEnd;
  }
  return boundary;
}

function commitStreamBlocks(activeTurn) {
  const tail = activeTurn.streamTail;
  if (!tail) return;
  tail.framesSinceCommit = 0;
  // Past the formatting bound the final render is plain text; committing
  // more typeset blocks would diverge from it.
  if (activeTurn.assistantText.length > MARKDOWN_MAX_CHARS) return;
  const tailText = activeTurn.assistantText.slice(tail.committedChars);
  const boundary = streamCommitBoundary(tailText);
  if (boundary === 0) return;
  const bubble = activeTurn.assistantBubble;
  bubble.classList.add("markdown");
  for (const block of parseMarkdownBlocks(tailText.slice(0, boundary).split(/\r\n|\r|\n/u))) {
    bubble.insertBefore(block, tail.wrap);
  }
  tail.textNode.textContent = tailText.slice(boundary);
  tail.committedChars += boundary;
}

// The turn's terminal formatting step. With a streaming tail in place only
// the uncommitted remainder is parsed — the spike that used to re-parse the
// whole answer at the finish line is gone. Without one (recovered bubbles,
// oversized answers) this falls back to the one-shot full render.
export function finalizeStreamedAssistant(activeTurn) {
  const bubble = activeTurn.assistantBubble;
  const text = activeTurn.assistantText;
  const tail = activeTurn.streamTail;
  activeTurn.streamTail = null;
  if (!tail || tail.wrap.parentNode !== bubble || text.length > MARKDOWN_MAX_CHARS) {
    renderMarkdownInto(bubble, text);
    return;
  }
  const remaining = text.slice(tail.committedChars);
  if (remaining !== "") {
    for (const block of parseMarkdownBlocks(remaining.split(/\r\n|\r|\n/u))) {
      bubble.insertBefore(block, tail.wrap);
    }
  }
  bubble.classList.add("markdown");
  tail.wrap.remove();
}

export function handleParsedTurnEvent(activeTurn, event) {
  if (!isCurrentTurn(activeTurn)) return;
  if (event.turnID !== activeTurn.turnID) {
    failActiveTurnProtocol(activeTurn, "The Agent stream returned an invalid turn identifier.");
    return;
  }

  // Deltas queue for the next frame; everything else is a fact the DOM must
  // not show ahead of the words that preceded it — drain first.
  if (event.type !== "text_delta" && event.type !== "reasoning_delta") {
    drainStreamBatch();
  }

  switch (event.type) {
    case "text_delta": {
      const deltaBytes = utf8ByteLength(event.delta);
      if (activeTurn.assistantTextBytes + deltaBytes > MAX_TURN_TEXT_BYTES) {
        failActiveTurnProtocol(activeTurn, "The Agent response exceeded the display limit.");
        return;
      }
      // Bookkeeping stays synchronous — limits and terminals read it — while
      // the paint itself waits for the frame.
      activeTurn.assistantText += event.delta;
      activeTurn.assistantTextBytes += deltaBytes;
      clearPendingIndicator(activeTurn);
      // Schedule first: it re-keys the buffer to this turn before the append.
      scheduleStreamFlush(activeTurn);
      streamBatch.text += event.delta;
      return;
    }
    case "reasoning_delta":
      recordReasoningDelta(activeTurn, event.delta);
      // The mind icon's one honest cue: reasoning tokens really are arriving.
      // Re-armed per delta and cheap when already lit — see noteMindActivity.
      noteMindActivity("thinking");
      return;
    case "turn_meta":
      // Recorded, not drawn. The footer belongs under a finished answer, and
      // this frame leads the turn — so it is kept on the turn and read when
      // the answer settles.
      activeTurn.engine = event.engine;
      activeTurn.model = event.model;
      return;
    case "approval_request":
      presentApprovalRequest(activeTurn, event);
      return;
    case "tool_use":
      recordToolActivity({ name: event.name, target: event.target, denied: false }, activeTurn);
      return;
    case "tool_denied":
      recordToolActivity({ name: event.name, target: event.target, denied: true, reason: event.reason }, activeTurn);
      return;
    case "tool_result":
      // The other end of a tool call: the step the log opened is now closed,
      // one way or the other. Nothing new appears — a result with no step to
      // settle names nothing a reader could act on.
      recordToolResult({ name: event.name, target: event.target, isError: event.isError }, activeTurn);
      return;
    case "retrieval":
      // What the local model was given from the knowledge base, announced
      // before the first token. Recorded against the turn so a stale event
      // from a superseded turn cannot repaint the panel — isCurrentTurn above
      // already fenced that, and this keeps the panel's own copy honest.
      setRetrievedContext(event.sources);
      return;
    case "unknown":
      // Preload exposes only the bounded event name for unknown events. There
      // is intentionally no upstream payload to stringify or render.
      return;
    case "done":
      if (isThreadBusyResult(event.result)) {
        if (activeTurn.recoveryTurn) {
          keepRecoverableTurnForRetry(
            activeTurn,
            "This response is still busy. Select Resume again in a moment.",
            "The interrupted response is still busy; no new execution was started."
          );
        } else {
          handleInitialTurnBusy(activeTurn);
        }
        return;
      }
      if (activeTurn.recoveryTurn && event.result.isError) {
        const message = recoveredTurnErrorMessage(event.result);
        state.resumingTurn = false;
        removeRecoverableTurn(activeTurn.recoveryTurn.turn_uuid);
        finishActiveTurnWithError(activeTurn, message);
        return;
      }
      if (activeTurn.recoveryTurn) {
        state.resumingTurn = false;
        removeRecoverableTurn(activeTurn.recoveryTurn.turn_uuid);
      }
      finishActiveTurn(activeTurn, "Done", false);
      return;
    case "canceled":
      if (activeTurn.stopRequested) {
        activeTurn.localCancelObserved = true;
      }
      if (activeTurn.recoveryTurn) {
        state.resumingTurn = false;
        removeRecoverableTurn(activeTurn.recoveryTurn.turn_uuid);
      }
      finishActiveTurn(activeTurn, "Stopped", true);
      return;
    case "proxy_error":
      if (event.error.kind === "session_changed") {
        void handleSessionChanged();
        return;
      }
      if (activeTurn.recoveryTurn) {
        keepRecoverableTurnForRetry(
          activeTurn,
          "Recovery was interrupted. Select Resume to try the same request again.",
          "The interrupted response could not be recovered yet."
        );
        return;
      }
      finishActiveTurnWithError(activeTurn, event.error.message || "The Agent turn failed.");
      return;
    case "protocol_error":
      if (activeTurn.recoveryTurn) {
        keepRecoverableTurnForRetry(
          activeTurn,
          "Recovery was interrupted. Select Resume to try the same request again.",
          "The interrupted response could not be recovered yet."
        );
        return;
      }
      failActiveTurnProtocol(activeTurn, event.message || "The Agent stream failed.");
  }
}
