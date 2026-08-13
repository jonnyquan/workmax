// Wire-format validation: everything that turns bytes the sidecar sent into
// values the rest of the renderer is allowed to believe.
//
// Pure by construction — no DOM, no state, no imports from the app. The
// renderer treats the sidecar as untrusted input (it is a local HTTP surface
// other processes can reach), so these functions are the only place a response
// is admitted, and each one answers null/false rather than throwing on the
// shapes it does not recognise.
//
// Turn-stream events are deliberately NOT here: they live with their
// dispatcher in events.js, because the validation and the handling of a frame
// are one contract and splitting them is how the two drift.
export const AUTH_POLL_INTERVAL_MS = 1000;
export const AUTH_POLL_TIMEOUT_MS = 5 * 60 * 1000;
const AUTH_STATES = new Set(["authenticated", "unauthenticated", "expired"]);
const MESSAGE_STREAMING_STATES = new Set(["complete", "partial", "streaming"]);
const LOGIN_TRANSACTION_STATES = new Set([
  "idle",
  "awaiting_password",
  "submitting",
  "authenticated",
]);
const LOGIN_TRANSACTION_ERRORS = new Set([
  "busy",
  "invalid_request",
  "invalid_credentials",
  "expired",
  "unavailable",
  "canceled",
]);
export const LOGIN_ERROR_MESSAGES = Object.freeze({
  busy: "Another sign-in action is already in progress. Check the current sign-in state.",
  invalid_request: "The sign-in request was rejected. Check your details and try again.",
  invalid_credentials: "The email or password is incorrect. Try again.",
  expired: "This sign-in session expired. Start a new sign-in.",
  unavailable: "The sign-in service is temporarily unavailable. Try again shortly.",
  canceled: "Sign-in was canceled.",
});
export const MAX_CHAT_TEXT_BYTES = 65_536;
export const MAX_THREAD_NAME_BYTES = 200;
export const MAX_TURN_TEXT_BYTES = 4 * 1024 * 1024;
export const CANONICAL_V4_UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/u;

export function sanitizeErrorMessage(value) {
  const redacted = String(value)
    .replace(/(https?:\/\/)[^/\s:@]+(?::[^/\s@]*)?@/gi, "$1[REDACTED]@")
    .replace(/(X-Local-Token[:=]\s*)\S+/gi, "$1[REDACTED]")
    .replace(/(Authorization:\s*(?:Bearer|Basic)\s+)\S+/gi, "$1[REDACTED]")
    .replace(/\bBearer\s+[A-Za-z0-9._~+/=-]+/gi, "Bearer [REDACTED]")
    .replace(/\bBasic\s+[A-Za-z0-9._~+/=-]+/gi, "Basic [REDACTED]")
    .replace(/((?:access|refresh|id)_token["']?\s*[:=]\s*["']?)[^"',&\s]+/gi, "$1[REDACTED]")
    .replace(/(api[_-]?key["']?\s*[:=]\s*["']?)[^"',&\s]+/gi, "$1[REDACTED]")
    .replace(/(apikey["']?\s*[:=]\s*["']?)[^"',&\s]+/gi, "$1[REDACTED]")
    .replace(/(client_secret["']?\s*[:=]\s*["']?)[^"',&\s]+/gi, "$1[REDACTED]")
    .replace(/(password["']?\s*[:=]\s*["']?)[^"',&\s]+/gi, "$1[REDACTED]")
    .replace(/(secret["']?\s*[:=]\s*["']?)[^"',&\s]+/gi, "$1[REDACTED]");
  if (redacted.length <= 500) return redacted;
  return `${redacted.slice(0, 500)}...`;
}

export function formatDate(value) {
  if (!value) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleString();
}

export function isRecord(value) {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function optionalString(value) {
  return typeof value === "string" ? value : "";
}

// An optional string the page will display, clipped to a length the layout can
// hold. Anything that is not a string, or that carries a control character,
// reads as absent — this is provenance, and a field that cannot be trusted has
// nothing to say. Slicing by code unit is safe here because the only consumer
// sets it with textContent.
function boundedString(value, maxChars) {
  if (typeof value !== "string" || value === "") return "";
  if (!hasWellFormedUTF16(value) || hasControlCharacter(value)) return "";
  return value.slice(0, maxChars);
}

function optionalCount(value) {
  if (!isNonNegativeInteger(value)) {
    throw new Error("Malformed /agent/threads response");
  }
  return value;
}

export function parseAuthStatus(value) {
  if (
    !isRecord(value) ||
    typeof value.state !== "string" ||
    !AUTH_STATES.has(value.state) ||
    typeof value.updated_at !== "string" ||
    (value.user_id !== undefined && typeof value.user_id !== "string") ||
    (value.tier !== undefined && typeof value.tier !== "string")
  ) {
    throw new Error("Malformed /auth/status response");
  }
  return {
    state: value.state,
    tier: value.tier ?? "",
    updated_at: value.updated_at,
  };
}

export function parseLoginTransactionResult(value) {
  if (!isRecord(value)) {
    throw new Error("Malformed Desktop login transaction response");
  }
  const keys = Object.keys(value).sort();
  if (
    typeof value.state === "string" &&
    LOGIN_TRANSACTION_STATES.has(value.state) &&
    keys.length === 1 &&
    keys[0] === "state"
  ) {
    return { state: value.state };
  }
  if (
    typeof value.state === "string" &&
    LOGIN_TRANSACTION_STATES.has(value.state) &&
    typeof value.error === "string" &&
    LOGIN_TRANSACTION_ERRORS.has(value.error) &&
    keys.length === 2 &&
    keys[0] === "error" &&
    keys[1] === "state"
  ) {
    return { state: value.state, error: value.error };
  }
  throw new Error("Malformed Desktop login transaction response");
}

function parseThread(value) {
  if (
    !isRecord(value) ||
    !isSafeLocalHistoryUUID(value.uuid) ||
    !isParseableTimestamp(value.updated_at)
  ) {
    throw new Error("Malformed /agent/threads response");
  }
  return {
    uuid: value.uuid,
    name: optionalString(value.name),
    agent_mode: optionalString(value.agent_mode),
    message_count: optionalCount(value.message_count),
    updated_at: optionalString(value.updated_at),
    // 'local' marks a thread that exists only on this machine — the one kind
    // the delete affordance is allowed to appear on. Absent or unknown values
    // are treated as synced, which errs on the side of not offering deletion.
    cloud_sync_state: optionalString(value.cloud_sync_state),
    // A local view preference; anything but literal true means unpinned, so
    // an older sidecar that omits the field degrades to the old behaviour.
    pinned: value.pinned === true,
  };
}

export function parseThreads(value) {
  if (!isRecord(value) || !Array.isArray(value.items)) {
    throw new Error("Malformed /agent/threads response");
  }
  return value.items.map(parseThread);
}

function parseMessage(value) {
  if (
    !isRecord(value) ||
    !isSafeLocalHistoryUUID(value.uuid) ||
    typeof value.streaming_state !== "string" ||
    !MESSAGE_STREAMING_STATES.has(value.streaming_state) ||
    !isParseableTimestamp(value.created_at) ||
    !isParseableTimestamp(value.updated_at)
  ) {
    throw new Error("Malformed /agent/threads/:uuid/messages response");
  }
  return {
    user_text: optionalString(value.user_text),
    ai_text: optionalString(value.ai_text),
    // Provenance, from migration 0013. Optional by construction: rows written
    // before it, and turns the sidecar never announced, carry neither — and a
    // row that says nothing must render as no claim rather than as a default.
    // Bounded here as well as at the shim because this path is the OTHER way
    // the value reaches the page, and a guard on one road is not a guard.
    agent_engine: boundedString(value.agent_engine, 32),
    agent_model: boundedString(value.agent_model, 80),
    agent_mind: boundedString(value.agent_mind, 64),
    streaming_state: value.streaming_state,
    // Validated above; carried so the transcript can date its rows.
    created_at: optionalString(value.created_at),
    updated_at: optionalString(value.updated_at),
  };
}

export function parseMessages(value) {
  if (!isRecord(value) || !Array.isArray(value.items)) {
    throw new Error("Malformed /agent/threads/:uuid/messages response");
  }
  return value.items.map(parseMessage);
}

export function parseRecoverableTurns(value) {
  if (
    !hasExactKeys(value, ["items", "count"]) ||
    !Array.isArray(value.items) ||
    !isNonNegativeInteger(value.count) ||
    value.count !== value.items.length
  ) {
    throw new Error("Malformed agent recoverable turns result");
  }
  const seen = new Set();
  const items = value.items.map((item) => {
    if (
      !hasExactKeys(item, [
        "turn_uuid",
        "thread_uuid",
        "user_text",
        "chat_mode",
        "state",
        "last_error_kind",
        "updated_at",
      ]) ||
      !CANONICAL_V4_UUID.test(item.turn_uuid) ||
      !isSafeLocalHistoryUUID(item.thread_uuid) ||
      !isValidChatText(item.user_text) ||
      item.user_text.includes("\u0000") ||
      !isSafeAgentMode(item.chat_mode) ||
      item.state !== "interrupted" ||
      !isSafeProtocolString(item.last_error_kind, 128, true) ||
      !isParseableTimestamp(item.updated_at) ||
      seen.has(item.turn_uuid)
    ) {
      throw new Error("Malformed agent recoverable turns result");
    }
    seen.add(item.turn_uuid);
    return {
      turn_uuid: item.turn_uuid,
      thread_uuid: item.thread_uuid,
      user_text: item.user_text,
      chat_mode: item.chat_mode,
      state: "interrupted",
      last_error_kind: item.last_error_kind,
      updated_at: item.updated_at,
    };
  });
  return items;
}

export function hasExactKeys(value, expected) {
  if (!isRecord(value)) return false;
  const actual = Object.keys(value).sort();
  const wanted = [...expected].sort();
  return (
    actual.length === wanted.length &&
    actual.every((key, index) => key === wanted[index])
  );
}

export function isSafeProtocolString(value, maxBytes, allowEmpty = false) {
  return (
    typeof value === "string" &&
    (allowEmpty || value.length > 0) &&
    hasWellFormedUTF16(value) &&
    utf8ByteLength(value) <= maxBytes &&
    !hasControlCharacter(value)
  );
}

export function isSafeAgentMode(value) {
  return (
    isSafeProtocolString(value, 64) &&
    /^[A-Za-z][A-Za-z0-9_-]{0,63}$/u.test(value)
  );
}

function parseStringArray(value, label) {
  if (!Array.isArray(value)) {
    throw new Error(`Malformed ${label}`);
  }
  const result = [];
  const seen = new Set();
  for (const item of value) {
    if (!isSafeProtocolString(item, 128)) {
      throw new Error(`Malformed ${label}`);
    }
    if (seen.has(item)) {
      throw new Error(`Malformed ${label}`);
    }
    seen.add(item);
    result.push(item);
  }
  return result;
}

function parseSkillArtifacts(value) {
  if (
    !hasExactKeys(value, [
      "primaryType",
      "outputTypes",
      "previewTypes",
      "exportTargets",
      "critiqueAnchors",
    ]) ||
    !isSafeProtocolString(value.primaryType, 128)
  ) {
    throw new Error("Malformed agent skills catalog response");
  }
  return {
    primaryType: value.primaryType,
    outputTypes: parseStringArray(value.outputTypes, "agent skills catalog response"),
    previewTypes: parseStringArray(value.previewTypes, "agent skills catalog response"),
    exportTargets: parseStringArray(value.exportTargets, "agent skills catalog response"),
    critiqueAnchors: parseStringArray(value.critiqueAnchors, "agent skills catalog response"),
  };
}

function parseSkill(value) {
  const expectedKeys = [
    "agentMode",
    "name",
    "description",
    "version",
    "hasQuestionForm",
    "hasDirectionsFallback",
    "hasPostScripts",
    "labelKey",
    "descriptionKey",
  ];
  if (isRecord(value) && value.artifacts !== undefined) {
    expectedKeys.push("artifacts");
  }
  if (
    !hasExactKeys(value, expectedKeys) ||
    !isSafeAgentMode(value.agentMode) ||
    !isSafeProtocolString(value.name, 256, true) ||
    !isSafeProtocolString(value.description, 4096, true) ||
    !isSafeProtocolString(value.version, 128) ||
    typeof value.hasQuestionForm !== "boolean" ||
    typeof value.hasDirectionsFallback !== "boolean" ||
    typeof value.hasPostScripts !== "boolean" ||
    !isSafeProtocolString(value.labelKey, 256) ||
    !isSafeProtocolString(value.descriptionKey, 256)
  ) {
    throw new Error("Malformed agent skills catalog response");
  }
  return {
    agentMode: value.agentMode,
    name: value.name,
    description: value.description,
    version: value.version,
    hasQuestionForm: value.hasQuestionForm,
    hasDirectionsFallback: value.hasDirectionsFallback,
    hasPostScripts: value.hasPostScripts,
    artifacts: value.artifacts === undefined ? null : parseSkillArtifacts(value.artifacts),
    labelKey: value.labelKey,
    descriptionKey: value.descriptionKey,
  };
}

export function parseSkillsCatalog(value) {
  if (
    !hasExactKeys(value, ["items", "count", "allowed_modes"]) ||
    !Array.isArray(value.items) ||
    !isNonNegativeInteger(value.count) ||
    value.count !== value.items.length ||
    !Array.isArray(value.allowed_modes)
  ) {
    throw new Error("Malformed agent skills catalog response");
  }
  const allowedModes = [];
  const allowedSet = new Set();
  for (const mode of value.allowed_modes) {
    if (!isSafeAgentMode(mode) || allowedSet.has(mode)) {
      throw new Error("Malformed agent skills catalog response");
    }
    allowedSet.add(mode);
    allowedModes.push(mode);
  }
  const items = value.items.map(parseSkill);
  const itemModes = new Set();
  for (const item of items) {
    if (!allowedSet.has(item.agentMode) || itemModes.has(item.agentMode)) {
      throw new Error("Malformed agent skills catalog response");
    }
    itemModes.add(item.agentMode);
  }
  return { items, count: items.length, allowed_modes: allowedModes };
}

function parseHeaderRecord(value, label) {
  if (!isRecord(value)) {
    throw new Error(`Malformed ${label}`);
  }
  for (const [name, headerValue] of Object.entries(value)) {
    if (
      !isSafeProtocolString(name, 256) ||
      !isSafeProtocolString(headerValue, 8192, true)
    ) {
      throw new Error(`Malformed ${label}`);
    }
  }
}

export function parseDesktopBridgeResult(value, label) {
  if (
    !isRecord(value) ||
    typeof value.ok !== "boolean" ||
    !Number.isInteger(value.status) ||
    value.status < 100 ||
    value.status > 599 ||
    typeof value.statusText !== "string"
  ) {
    throw new Error(`Malformed ${label}`);
  }
  parseHeaderRecord(value.headers, label);
  if (value.ok) {
    if (!hasExactKeys(value, ["ok", "status", "statusText", "headers", "data"])) {
      throw new Error(`Malformed ${label}`);
    }
  } else if (!hasExactKeys(value, ["ok", "status", "statusText", "headers", "error"])) {
    throw new Error(`Malformed ${label}`);
  }
  return value;
}

export function isSessionChangedPayload(value) {
  return isRecord(value) && value.error === "session_changed";
}

export function parseTurnOpenResult(value) {
  if (!hasExactKeys(value, ["turnID"]) || !isSafeLocalHistoryUUID(value.turnID)) {
    throw new Error("Malformed agent turn open result");
  }
  return { turnID: value.turnID };
}

export function parseTurnCancelResult(value) {
  if (
    !hasExactKeys(value, ["turnID", "canceled"]) ||
    !isSafeLocalHistoryUUID(value.turnID) ||
    typeof value.canceled !== "boolean"
  ) {
    throw new Error("Malformed agent turn cancel result");
  }
  return { turnID: value.turnID, canceled: value.canceled };
}

export function isSafeLocalHistoryUUID(value) {
  return (
    typeof value === "string" &&
    value.length > 0 &&
    utf8ByteLength(value) <= 200 &&
    value.trim() === value &&
    !hasControlCharacter(value)
  );
}

export function isNonNegativeInteger(value) {
  return Number.isInteger(value) && value >= 0;
}

export function isParseableTimestamp(value) {
  return typeof value === "string" && value.trim() !== "" && Number.isFinite(Date.parse(value));
}

function hasControlCharacter(value) {
  for (let i = 0; i < value.length; i += 1) {
    const code = value.charCodeAt(i);
    if (code < 0x20 || code === 0x7f) return true;
  }
  return false;
}

export function utf8ByteLength(value) {
  let bytes = 0;
  for (let i = 0; i < value.length; i += 1) {
    const code = value.charCodeAt(i);
    if (code < 0x80) {
      bytes += 1;
    } else if (code < 0x800) {
      bytes += 2;
    } else if (code >= 0xd800 && code <= 0xdbff && i + 1 < value.length) {
      const next = value.charCodeAt(i + 1);
      if (next >= 0xdc00 && next <= 0xdfff) {
        bytes += 4;
        i += 1;
      } else {
        bytes += 3;
      }
    } else {
      bytes += 3;
    }
  }
  return bytes;
}

export function parseModelRouteSettings(value) {
  if (!isRecord(value)) {
    throw new Error("Malformed model route settings");
  }
  if (Object.prototype.hasOwnProperty.call(value, "api_key")) {
    throw new Error("Model route settings must not include api_key");
  }
  const route = value.preferred_route;
  if (route !== "local" && route !== "official") {
    throw new Error("Malformed preferred_route");
  }
  const local = value.local;
  if (!isRecord(local) || Object.prototype.hasOwnProperty.call(local, "api_key")) {
    throw new Error("Malformed local model profile");
  }
  return {
    preferred_route: route,
    official_model_id: optionalString(value.official_model_id) || "",
    local: {
      protocol: optionalString(local.protocol) || "",
      base_url: optionalString(local.base_url) || "",
      model_id: optionalString(local.model_id) || "",
      api_key_configured: local.api_key_configured === true,
    },
    updated_at: optionalString(value.updated_at) || "",
  };
}

// parseAgentModes mirrors the strictness of every other bridge parser: the
// answer decides whether a signed-out user may drive the agent, so a
// half-understood payload must be an error rather than a permissive default.
export function parseAgentModes(value) {
  if (
    !hasExactKeys(value, ["allowed_modes", "local_route", "tool_loop"]) ||
    !Array.isArray(value.allowed_modes) ||
    typeof value.local_route !== "boolean" ||
    typeof value.tool_loop !== "boolean"
  ) {
    throw new Error("Malformed agent modes response");
  }
  const modes = [];
  const seen = new Set();
  for (const mode of value.allowed_modes) {
    if (!isSafeProtocolString(mode, 64) || seen.has(mode)) {
      throw new Error("Malformed agent modes response");
    }
    seen.add(mode);
    modes.push(mode);
  }
  return { allowed_modes: modes, local_route: value.local_route, tool_loop: value.tool_loop };
}

export function parseLocalAccounts(data) {
  if (!isRecord(data) || !Array.isArray(data.items)) {
    throw new Error("invalid local accounts payload");
  }
  return data.items.map((item) => {
    if (
      !isRecord(item) ||
      !Number.isInteger(item.id) ||
      item.id <= 0 ||
      typeof item.name !== "string" ||
      item.name.length === 0 ||
      typeof item.active !== "boolean"
    ) {
      throw new Error("invalid local account entry");
    }
    return { id: item.id, name: item.name, active: item.active };
  });
}

// The other half of /local/accounts: whether this machine's identity has a
// WorkMax account connected to it. Deliberately lenient about ABSENCE — a
// sidecar that predates the field is simply an unbound one — and strict about
// content, because "bound to …42" is a claim about where work goes.
export function parseCloudBinding(data) {
  const binding = isRecord(data) ? data.binding : null;
  if (!isRecord(binding)) {
    return { state: "unbound", user_id: "" };
  }
  const state =
    binding.state === "bound" || binding.state === "expired" ? binding.state : "unbound";
  const userID =
    typeof binding.user_id === "string" && isSafeProtocolString(binding.user_id, 32)
      ? binding.user_id
      : "";
  return { state, user_id: state === "unbound" ? "" : userID };
}

export function isValidChatText(value) {
  if (typeof value !== "string" || !hasWellFormedUTF16(value)) return false;
  const trimmed = value.trim();
  return trimmed.length > 0 && utf8ByteLength(trimmed) <= MAX_CHAT_TEXT_BYTES;
}

export function isValidThreadName(value) {
  return (
    typeof value === "string" &&
    value.length > 0 &&
    value.trim() === value &&
    hasWellFormedUTF16(value) &&
    !hasControlCharacter(value) &&
    utf8ByteLength(value) <= MAX_THREAD_NAME_BYTES
  );
}

export function validLoginCredential(value, minBytes, maxBytes) {
  return (
    typeof value === "string" &&
    hasWellFormedUTF16(value) &&
    !/\p{Cc}/u.test(value) &&
    utf8ByteLength(value) >= minBytes &&
    utf8ByteLength(value) <= maxBytes
  );
}

export function validLoginEmail(value) {
  return (
    validLoginCredential(value, 3, 320) &&
    value.trim() === value &&
    value.includes("@")
  );
}

export function hasWellFormedUTF16(value) {
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
    if (code >= 0xdc00 && code <= 0xdfff) {
      return false;
    }
  }
  return true;
}

export function parseSearchMatches(data) {
  if (!isRecord(data) || !Array.isArray(data.items)) {
    throw new Error("invalid search payload");
  }
  return data.items.filter(
    (item) =>
      isRecord(item) &&
      isSafeLocalHistoryUUID(item.thread_uuid) &&
      typeof item.snippet === "string" &&
      item.snippet.length > 0 &&
      (item.role === "you" || item.role === "assistant")
  );
}
