const state = {
  auth: null,
  threads: [],
  selectedThreadUUID: null,
  skills: [],
  allowedModes: [],
  selectedMode: "",
  skillsLoading: false,
  skillsDegraded: false,
  agentAvailable: false,
  createAvailable: false,
  createFormOpen: false,
  creatingThread: false,
  createGeneration: 0,
  createDraft: null,
  sessionGeneration: 0,
  selectionGeneration: 0,
  turnGeneration: 0,
  activeTurn: null,
  cancelConfirmationTurnID: null,
  recoverableTurns: [],
  recoveryLoading: false,
  recoveryGeneration: 0,
  resumingTurn: false,
  dismissingRecovery: false,
  recoveryFeedback: "",
  recoveryFeedbackKind: "default",
  recoveringSession: false,
  pendingFiles: [],
  uploadGeneration: 0,
};

const AUTH_POLL_INTERVAL_MS = 1000;
const AUTH_POLL_TIMEOUT_MS = 5 * 60 * 1000;
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
const LOGIN_ERROR_MESSAGES = Object.freeze({
  busy: "Another sign-in action is already in progress. Check the current sign-in state.",
  invalid_request: "The sign-in request was rejected. Check your details and try again.",
  invalid_credentials: "The email or password is incorrect. Try again.",
  expired: "This sign-in session expired. Start a new sign-in.",
  unavailable: "The sign-in service is temporarily unavailable. Try again shortly.",
  canceled: "Sign-in was canceled.",
});
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
  "unknown",
  "done",
  "proxy_error",
  "canceled",
  "protocol_error",
]);
const MAX_CHAT_TEXT_BYTES = 65_536;
const MAX_THREAD_NAME_BYTES = 200;
const MAX_EVENT_TEXT_BYTES = 262_144;
const MAX_TURN_TEXT_BYTES = 4 * 1024 * 1024;
const CANONICAL_V4_UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/u;
let loginOperationGeneration = 0;

const statusCard = document.querySelector("#status-card");
const runtimeLabel = document.querySelector("#runtime-label");
const refreshButton = document.querySelector("#refresh-button");
const modelsButton = document.querySelector("#models-button");
const modelSettingsForm = document.querySelector("#model-settings-form");
const modelPreferredRoute = document.querySelector("#model-preferred-route");
const modelLocalFields = document.querySelector("#model-local-fields");
const modelProtocol = document.querySelector("#model-protocol");
const modelBaseURL = document.querySelector("#model-base-url");
const modelID = document.querySelector("#model-id");
const modelAPIKey = document.querySelector("#model-api-key");
const modelClearAPIKey = document.querySelector("#model-clear-api-key");
const modelKeyStatus = document.querySelector("#model-key-status");
const modelSettingsError = document.querySelector("#model-settings-error");
const modelSettingsSubmitButton = document.querySelector("#model-settings-submit-button");
const modelSettingsCancelButton = document.querySelector("#model-settings-cancel-button");
const loginButton = document.querySelector("#login-button");
const loginForm = document.querySelector("#login-form");
const loginEmail = document.querySelector("#login-email");
const loginPassword = document.querySelector("#login-password");
const loginSubmitButton = document.querySelector("#login-submit-button");
const loginCancelButton = document.querySelector("#login-cancel-button");
const newThreadButton = document.querySelector("#new-thread-button");
const newThreadForm = document.querySelector("#new-thread-form");
const newThreadName = document.querySelector("#new-thread-name");
const newThreadMode = document.querySelector("#new-thread-mode");
const newThreadError = document.querySelector("#new-thread-error");
const newThreadSubmitButton = document.querySelector("#new-thread-submit-button");
const newThreadCancelButton = document.querySelector("#new-thread-cancel-button");
const threadList = document.querySelector("#thread-list");
const emptyState = document.querySelector("#empty-state");
const emptyTitle = document.querySelector("#empty-title");
const emptyDescription = document.querySelector("#empty-description");
const emptyNewThreadButton = document.querySelector("#empty-new-thread-button");
const threadPanel = document.querySelector("#thread-panel");
const threadTitle = document.querySelector("#thread-title");
const threadMeta = document.querySelector("#thread-meta");
const messageList = document.querySelector("#message-list");
const messageViewport = document.querySelector("#message-viewport");
const turnRecoveryCard = document.querySelector("#turn-recovery-card");
const turnRecoveryDescription = document.querySelector("#turn-recovery-description");
const turnRecoveryPrompt = document.querySelector("#turn-recovery-prompt");
const turnRecoveryFeedback = document.querySelector("#turn-recovery-feedback");
const turnRecoveryResumeButton = document.querySelector("#turn-recovery-resume-button");
const turnRecoveryDismissButton = document.querySelector("#turn-recovery-dismiss-button");
const chatForm = document.querySelector("#chat-form");
const agentMode = document.querySelector("#agent-mode");
const composerStatus = document.querySelector("#composer-status");
const chatInput = document.querySelector("#chat-input");
const stopButton = document.querySelector("#stop-button");
const sendButton = document.querySelector("#send-button");
const turnState = document.querySelector("#turn-state");
const fileInput = document.querySelector("#file-input");
const attachButton = document.querySelector("#attach-button");
const attachmentChips = document.querySelector("#attachment-chips");

class SessionChangedError extends Error {
  constructor() {
    super("The authenticated session changed");
    this.name = "SessionChangedError";
  }
}

class ThreadCreateFailure extends Error {
  constructor(feedback, statusMessage, retryable) {
    super("Thread creation failed");
    this.name = "ThreadCreateFailure";
    this.feedback = feedback;
    this.statusMessage = statusMessage;
    this.retryable = retryable;
  }
}

function setStatus(message, kind = "default") {
  statusCard.textContent = sanitizeErrorMessage(message);
  statusCard.classList.toggle("error", kind === "error");
}

function sanitizeErrorMessage(value) {
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

function formatDate(value) {
  if (!value) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleString();
}

function isRecord(value) {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function optionalString(value) {
  return typeof value === "string" ? value : "";
}

function optionalCount(value) {
  if (!isNonNegativeInteger(value)) {
    throw new Error("Malformed /agent/threads response");
  }
  return value;
}

function parseAuthStatus(value) {
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

function parseLoginTransactionResult(value) {
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
  };
}

function parseThreads(value) {
  if (!isRecord(value) || !Array.isArray(value.items)) {
    throw new Error("Malformed /agent/threads response");
  }
  return value.items.map(parseThread);
}

function parseCreatedThread(value, expectedDraft, created) {
  if (
    !hasExactKeys(value, [
      "uuid",
      "name",
      "agent_mode",
      "message_count",
      "updated_at",
      "cloud_sync_state",
    ]) ||
    value.uuid !== expectedDraft.threadUUID ||
    (created && value.name !== expectedDraft.name) ||
    (created && value.agent_mode !== expectedDraft.agentMode) ||
    !isValidThreadName(value.name) ||
    !isSafeAgentMode(value.agent_mode) ||
    !state.allowedModes.includes(value.agent_mode) ||
    !isNonNegativeInteger(value.message_count) ||
    !isParseableTimestamp(value.updated_at) ||
    (value.cloud_sync_state !== "synced" && value.cloud_sync_state !== "paused")
  ) {
    throw new Error("Malformed agent create thread result");
  }
  return {
    uuid: value.uuid,
    name: value.name,
    agent_mode: value.agent_mode,
    message_count: value.message_count,
    updated_at: value.updated_at,
    cloud_sync_state: value.cloud_sync_state,
  };
}

function parseCreateThreadData(value, status, expectedDraft) {
  if (
    hasExactKeys(value, ["state", "created", "thread"]) &&
    value.state === "ready" &&
    typeof value.created === "boolean" &&
    status === (value.created ? 201 : 200)
  ) {
    return {
      state: "ready",
      created: value.created,
      thread: parseCreatedThread(value.thread, expectedDraft, value.created),
    };
  }
  if (
    status === 202 &&
    hasExactKeys(value, ["state", "thread_uuid"]) &&
    value.state === "pending_local_sync" &&
    value.thread_uuid === expectedDraft.threadUUID
  ) {
    return {
      state: "pending_local_sync",
      threadUUID: value.thread_uuid,
    };
  }
  throw new Error("Malformed agent create thread result");
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
    streaming_state: value.streaming_state,
  };
}

function parseMessages(value) {
  if (!isRecord(value) || !Array.isArray(value.items)) {
    throw new Error("Malformed /agent/threads/:uuid/messages response");
  }
  return value.items.map(parseMessage);
}

function parseRecoverableTurns(value) {
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

function hasExactKeys(value, expected) {
  if (!isRecord(value)) return false;
  const actual = Object.keys(value).sort();
  const wanted = [...expected].sort();
  return (
    actual.length === wanted.length &&
    actual.every((key, index) => key === wanted[index])
  );
}

function isSafeProtocolString(value, maxBytes, allowEmpty = false) {
  return (
    typeof value === "string" &&
    (allowEmpty || value.length > 0) &&
    hasWellFormedUTF16(value) &&
    utf8ByteLength(value) <= maxBytes &&
    !hasControlCharacter(value)
  );
}

function isSafeAgentMode(value) {
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

function parseSkillsCatalog(value) {
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

function parseDesktopBridgeResult(value, label) {
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

function isSessionChangedPayload(value) {
  return isRecord(value) && value.error === "session_changed";
}

function parseTurnOpenResult(value) {
  if (!hasExactKeys(value, ["turnID"]) || !isSafeLocalHistoryUUID(value.turnID)) {
    throw new Error("Malformed agent turn open result");
  }
  return { turnID: value.turnID };
}

function parseTurnCancelResult(value) {
  if (
    !hasExactKeys(value, ["turnID", "canceled"]) ||
    !isSafeLocalHistoryUUID(value.turnID) ||
    typeof value.canceled !== "boolean"
  ) {
    throw new Error("Malformed agent turn cancel result");
  }
  return { turnID: value.turnID, canceled: value.canceled };
}

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

function parseAgentTurnEvent(value) {
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

function isSafeLocalHistoryUUID(value) {
  return (
    typeof value === "string" &&
    value.length > 0 &&
    utf8ByteLength(value) <= 200 &&
    value.trim() === value &&
    !hasControlCharacter(value)
  );
}

function isNonNegativeInteger(value) {
  return Number.isInteger(value) && value >= 0;
}

function isParseableTimestamp(value) {
  return typeof value === "string" && value.trim() !== "" && Number.isFinite(Date.parse(value));
}

function hasControlCharacter(value) {
  for (let i = 0; i < value.length; i += 1) {
    const code = value.charCodeAt(i);
    if (code < 0x20 || code === 0x7f) return true;
  }
  return false;
}

function utf8ByteLength(value) {
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

function bridge() {
  return window.workmaxLocal || null;
}

function desktopAuthBridge() {
  const desktop = window.desktopBridge;
  if (!isRecord(desktop) || !isRecord(desktop.auth)) {
    return null;
  }
  const auth = desktop.auth;
  if (
    typeof auth.beginLogin !== "function" ||
    typeof auth.loginStatus !== "function" ||
    typeof auth.submitLoginPassword !== "function" ||
    typeof auth.cancelLogin !== "function"
  ) {
    return null;
  }
  return auth;
}

function desktopAgentBridge() {
  const desktop = window.desktopBridge;
  if (!isRecord(desktop) || !isRecord(desktop.agent)) {
    return null;
  }
  const agent = desktop.agent;
  if (
    typeof agent.listSkills !== "function" ||
    typeof agent.startTurn !== "function" ||
    typeof agent.cancelTurn !== "function" ||
    typeof agent.uploadThreadFile !== "function"
  ) {
    return null;
  }
  return agent;
}

// uploadThreadFile uploads one file to the selected thread via the typed bridge
// and tracks it as a pending attachment (chip). Only "ready" attachments are
// sent with the next turn (fileIDs); "uploading" is excluded, "error" flagged.
function uploadThreadFile(file) {
  const agent = desktopAgentBridge();
  if (!agent) {
    setStatus("File upload unavailable", "error");
    return;
  }
  const threadUUID = state.selectedThreadUUID;
  if (!threadUUID) {
    return;
  }
  const entry = { id: 0, name: file.name, size: file.size, status: "uploading" };
  state.pendingFiles.push(entry);
  renderAttachments();
  const generation = ++state.uploadGeneration;
  agent.uploadThreadFile(threadUUID, file).then((result) => {
    if (generation !== state.uploadGeneration || !isRecord(result)) return;
    if (result.ok && result.data && typeof result.data.file_id === "number") {
      entry.id = result.data.file_id;
      entry.status = "ready";
    } else {
      entry.status = "error";
    }
    renderAttachments();
  });
}

function renderAttachments() {
  if (!attachmentChips) return;
  attachmentChips.innerHTML = "";
  for (const file of state.pendingFiles) {
    const chip = document.createElement("span");
    chip.className = "attachment-chip";
    chip.textContent =
      file.status === "uploading"
        ? `${file.name}…`
        : file.status === "error"
          ? `${file.name} ✗`
          : file.name;
    attachmentChips.appendChild(chip);
  }
  attachmentChips.hidden = state.pendingFiles.length === 0;
  // The composer tray and the Sources panel show the same files; refreshing
  // here keeps them from disagreeing after an upload completes or a turn
  // clears the tray.
  renderTaskContext();
}

function desktopAgentCreateBridge() {
  const desktop = window.desktopBridge;
  if (
    !isRecord(desktop) ||
    !isRecord(desktop.agent) ||
    typeof desktop.agent.createThread !== "function"
  ) {
    return null;
  }
  return desktop.agent;
}

function desktopAgentRecoveryBridge() {
  const desktop = window.desktopBridge;
  if (
    !isRecord(desktop) ||
    !isRecord(desktop.agent) ||
    typeof desktop.agent.listRecoverableTurns !== "function" ||
    typeof desktop.agent.resumeTurn !== "function" ||
    typeof desktop.agent.cancelTurn !== "function"
  ) {
    return null;
  }
  return desktop.agent;
}

function desktopSettingsBridge() {
  const desktop = window.desktopBridge;
  if (
    !isRecord(desktop) ||
    !isRecord(desktop.settings) ||
    typeof desktop.settings.getModelRoute !== "function" ||
    typeof desktop.settings.putModelRoute !== "function"
  ) {
    return null;
  }
  return desktop.settings;
}

function parseModelRouteSettings(value) {
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
    local: {
      protocol: optionalString(local.protocol) || "",
      base_url: optionalString(local.base_url) || "",
      model_id: optionalString(local.model_id) || "",
      api_key_configured: local.api_key_configured === true,
    },
    updated_at: optionalString(value.updated_at) || "",
  };
}

function setModelSettingsError(message) {
  if (!modelSettingsError) return;
  if (!message) {
    modelSettingsError.hidden = true;
    modelSettingsError.textContent = "";
    return;
  }
  modelSettingsError.hidden = false;
  modelSettingsError.textContent = message;
}

function updateModelLocalFieldsVisibility() {
  if (!modelPreferredRoute || !modelLocalFields) return;
  const local = modelPreferredRoute.value === "local";
  modelLocalFields.hidden = !local;
}

function clearModelAPIKeyField() {
  if (modelAPIKey) modelAPIKey.value = "";
  if (modelClearAPIKey) modelClearAPIKey.checked = false;
}

function fillModelSettingsForm(settings) {
  if (!modelPreferredRoute || !modelProtocol || !modelBaseURL || !modelID) return;
  modelPreferredRoute.value = settings.preferred_route;
  modelProtocol.value =
    settings.local.protocol === "anthropic_compatible"
      ? "anthropic_compatible"
      : "openai_compatible";
  modelBaseURL.value = settings.local.base_url;
  modelID.value = settings.local.model_id;
  clearModelAPIKeyField();
  if (modelKeyStatus) {
    modelKeyStatus.textContent = settings.local.api_key_configured
      ? "API key: stored in Keychain"
      : "API key: not stored";
  }
  updateModelLocalFieldsVisibility();
  setModelSettingsError("");
}

async function openModelSettings() {
  if (!modelSettingsForm) return;
  const settings = desktopSettingsBridge();
  if (!settings) {
    setStatus("Model settings require desktopBridge.settings (alpha.7+).", "error");
    return;
  }
  modelSettingsForm.hidden = false;
  if (modelSettingsSubmitButton) modelSettingsSubmitButton.disabled = true;
  setModelSettingsError("");
  try {
    const result = parseDesktopBridgeResult(
      await settings.getModelRoute(),
      "settings.getModelRoute"
    );
    if (!result.ok) {
      throw new Error(sanitizeErrorMessage(result.error) || "Could not load model settings");
    }
    fillModelSettingsForm(parseModelRouteSettings(result.data));
  } catch (error) {
    setModelSettingsError(String(error.message || error));
  } finally {
    if (modelSettingsSubmitButton) modelSettingsSubmitButton.disabled = false;
  }
}

function closeModelSettings() {
  if (modelSettingsForm) modelSettingsForm.hidden = true;
  clearModelAPIKeyField();
  setModelSettingsError("");
}

async function submitModelSettings(event) {
  event.preventDefault();
  if (!modelPreferredRoute) return;
  const settings = desktopSettingsBridge();
  if (!settings) {
    setModelSettingsError("Model settings bridge unavailable");
    return;
  }
  if (modelSettingsSubmitButton) modelSettingsSubmitButton.disabled = true;
  setModelSettingsError("");
  const preferred = modelPreferredRoute.value;
  /** @type {{ preferred_route: string, local?: Record<string, unknown> }} */
  const body = { preferred_route: preferred };
  if (preferred === "local") {
    body.local = {
      protocol: modelProtocol ? modelProtocol.value : "openai_compatible",
      base_url: modelBaseURL ? modelBaseURL.value.trim() : "",
      model_id: modelID ? modelID.value.trim() : "",
    };
    if (modelClearAPIKey && modelClearAPIKey.checked) {
      body.local.clear_api_key = true;
    } else if (modelAPIKey && modelAPIKey.value !== "") {
      body.local.api_key = modelAPIKey.value;
    }
  }
  try {
    const result = parseDesktopBridgeResult(
      await settings.putModelRoute(body),
      "settings.putModelRoute"
    );
    clearModelAPIKeyField();
    if (!result.ok) {
      throw new Error(sanitizeErrorMessage(result.error) || "Could not save model settings");
    }
    fillModelSettingsForm(parseModelRouteSettings(result.data));
    setStatus(
      preferred === "local"
        ? "Saved local model route (inference wiring still pending)."
        : "Saved official model route preference."
    );
  } catch (error) {
    setModelSettingsError(String(error.message || error));
  } finally {
    if (modelAPIKey) modelAPIKey.value = "";
    if (modelSettingsSubmitButton) modelSettingsSubmitButton.disabled = false;
  }
}

async function sidecarFetch(path, init) {
  const api = bridge();
  if (!api) {
    throw new Error("window.workmaxLocal bridge is unavailable");
  }
  return api.fetch(path, init);
}

async function readSidecarJSON(response, endpoint) {
  let payload;
  try {
    payload = await response.json();
  } catch {
    if (!response.ok) {
      throw new Error(`${endpoint} HTTP ${response.status}`);
    }
    throw new Error(`Malformed ${endpoint} response`);
  }
  if (!response.ok) {
    if (response.status === 409 && isSessionChangedPayload(payload)) {
      throw new SessionChangedError();
    }
    throw new Error(`${endpoint} HTTP ${response.status}`);
  }
  return payload;
}

async function loadAuthStatus(expectedSessionGeneration = state.sessionGeneration) {
  const res = await sidecarFetch("/auth/status");
  const auth = parseAuthStatus(await readSidecarJSON(res, "/auth/status"));
  if (expectedSessionGeneration !== state.sessionGeneration) {
    return null;
  }
  state.auth = auth;
  return state.auth;
}

async function loadThreads(expectedSessionGeneration = state.sessionGeneration) {
  const res = await sidecarFetch("/agent/threads?include_paused=false");
  const threads = parseThreads(await readSidecarJSON(res, "/agent/threads"));
  if (expectedSessionGeneration !== state.sessionGeneration) {
    return false;
  }
  state.threads = threads;
  renderThreads();
  updateSelectedThreadHeading();
  return true;
}

async function loadSkills(expectedSessionGeneration = state.sessionGeneration) {
  const agent = desktopAgentBridge();
  state.agentAvailable = agent !== null;
  state.createAvailable = desktopAgentCreateBridge() !== null;
  if (!agent) {
    if (expectedSessionGeneration === state.sessionGeneration) {
      state.skills = [];
      state.allowedModes = [];
      state.selectedMode = "";
      state.skillsLoading = false;
      state.skillsDegraded = false;
      renderSkillOptions();
      updateComposerState();
    }
    return false;
  }
  state.skillsLoading = true;
  updateComposerState();
  let result;
  try {
    result = parseDesktopBridgeResult(
      await agent.listSkills(),
      "agent skills result"
    );
  } finally {
    if (expectedSessionGeneration === state.sessionGeneration) {
      state.skillsLoading = false;
    }
  }
  if (!result.ok) {
    if (result.status === 409 && isSessionChangedPayload(result.error)) {
      throw new SessionChangedError();
    }
    throw new Error(`Agent skills HTTP ${result.status}`);
  }
  const catalog = parseSkillsCatalog(result.data);
  if (expectedSessionGeneration !== state.sessionGeneration) {
    return false;
  }
  state.skills = catalog.items;
  state.allowedModes = catalog.allowed_modes;
  state.skillsDegraded = catalog.items.length === 0 && catalog.allowed_modes.length > 0;
  renderSkillOptions();
  updateComposerState();
  return true;
}

async function loadRecoverableTurns(
  expectedSessionGeneration = state.sessionGeneration
) {
  const agent = desktopAgentRecoveryBridge();
  if (!agent) {
    if (expectedSessionGeneration === state.sessionGeneration) {
      state.recoverableTurns = [];
      state.recoveryLoading = false;
      renderThreads();
      updateComposerState();
    }
    return false;
  }
  state.recoveryLoading = true;
  updateComposerState();
  let result;
  try {
    result = parseDesktopBridgeResult(
      await agent.listRecoverableTurns(),
      "agent recoverable turns result"
    );
  } finally {
    if (expectedSessionGeneration === state.sessionGeneration) {
      state.recoveryLoading = false;
      updateComposerState();
    }
  }
  if (!result.ok) {
    if (result.status === 409 && isSessionChangedPayload(result.error)) {
      throw new SessionChangedError();
    }
    if (expectedSessionGeneration === state.sessionGeneration) {
      state.recoverableTurns = [];
      renderThreads();
      updateComposerState();
    }
    return false;
  }
  const items = parseRecoverableTurns(result.data);
  if (expectedSessionGeneration !== state.sessionGeneration) {
    return false;
  }
  state.recoverableTurns = items;
  renderThreads();
  updateComposerState();
  return true;
}

function selectionContext(threadUUID = state.selectedThreadUUID) {
  return {
    sessionGeneration: state.sessionGeneration,
    selectionGeneration: state.selectionGeneration,
    turnGeneration: state.turnGeneration,
    threadUUID,
  };
}

function isCurrentSelection(context) {
  return (
    context.sessionGeneration === state.sessionGeneration &&
    context.selectionGeneration === state.selectionGeneration &&
    context.turnGeneration === state.turnGeneration &&
    context.threadUUID === state.selectedThreadUUID
  );
}

async function loadMessagesForSelection(thread, context) {
  const res = await sidecarFetch(`/agent/threads/${encodeURIComponent(thread.uuid)}/messages`);
  const items = parseMessages(
    await readSidecarJSON(res, "/agent/threads/:uuid/messages")
  );
  if (!isCurrentSelection(context)) {
    return false;
  }
  renderCachedMessages(items);
  return true;
}

function renderCachedMessages(items) {
  messageList.textContent = "";
  if (items.length === 0) {
    messageList.appendChild(renderNotice("No cached messages for this thread yet."));
    return;
  }
  for (const item of items) {
    if (item.user_text) {
      messageList.appendChild(renderMessage("user", item.user_text));
    }
    if (item.ai_text || item.streaming_state !== "complete") {
      messageList.appendChild(
        renderMessage(
          "assistant",
          item.ai_text || "Response interrupted before text was cached.",
          item.streaming_state
        )
      );
    }
  }
  scrollMessagesToEnd();
}

function selectThread(thread) {
  if (state.activeTurn && thread.uuid === state.selectedThreadUUID) {
    return;
  }
  if (state.createFormOpen) {
    if (state.createDraft?.attempted) {
      const retryable = state.createDraft.retryable === true;
      setStatus(
        retryable
          ? "Retry or cancel the current thread draft before switching."
          : "Cancel the current thread draft before switching.",
        "error"
      );
      (retryable ? newThreadSubmitButton : newThreadCancelButton).focus();
      return;
    }
    cancelNewThreadDraft(false);
  }
  if (state.activeTurn) {
    invalidateActiveTurn(true);
  }
  state.selectionGeneration += 1;
  state.selectedThreadUUID = thread.uuid;
  // The context panel follows the selection: its sources belong to a thread,
  // not to the session.
  void loadThreadSources(thread.uuid);
  state.recoveryGeneration += 1;
  state.resumingTurn = false;
  state.dismissingRecovery = false;
  state.recoveryFeedback = "";
  state.recoveryFeedbackKind = "default";
  const context = selectionContext(thread.uuid);
  renderThreads();
  emptyState.hidden = true;
  threadPanel.hidden = false;
  updateSelectedThreadHeading();
  chooseModeForThread(thread);
  renderSkillOptions();
  updateComposerState();
  turnState.textContent = selectedRecoverableTurn() ? "Interrupted" : "Ready";
  messageList.textContent = "";
  messageList.appendChild(renderNotice("Loading cached messages..."));
  void loadMessagesForSelection(thread, context).catch((error) => {
    void handleScopedError(error, context);
  });
}

function renderNotice(text) {
  const node = document.createElement("p");
  node.className = "status-card";
  node.textContent = text;
  return node;
}

function renderMessage(role, text, streamingState = "complete") {
  const wrapper = document.createElement("article");
  wrapper.className = `message ${role}`;
  wrapper.classList.toggle(
    "partial",
    role === "assistant" && streamingState !== "complete"
  );
  const label = document.createElement("div");
  label.className = "message-role";
  label.textContent = role;
  const bubble = document.createElement("div");
  bubble.className = "bubble";
  bubble.textContent = text;
  wrapper.append(label, bubble);
  return wrapper;
}

function scrollMessagesToEnd() {
  messageViewport.scrollTop = messageViewport.scrollHeight;
}

function updateSelectedThreadHeading() {
  if (!state.selectedThreadUUID) return;
  const thread = state.threads.find(
    (candidate) => candidate.uuid === state.selectedThreadUUID
  );
  if (!thread) return;
  threadTitle.textContent = thread.name || "Untitled thread";
  threadMeta.textContent = `${thread.agent_mode || "agent"} · ${thread.message_count || 0} messages · ${formatDate(thread.updated_at)}`;
}

function chooseModeForThread(thread) {
  if (state.allowedModes.includes(thread.agent_mode)) {
    state.selectedMode = thread.agent_mode;
    return;
  }
  if (!state.allowedModes.includes(state.selectedMode)) {
    state.selectedMode = state.allowedModes[0] || "";
  }
}

function renderSkillOptions() {
  const selectedThread = state.threads.find(
    (thread) => thread.uuid === state.selectedThreadUUID
  );
  if (selectedThread) {
    chooseModeForThread(selectedThread);
  } else if (!state.allowedModes.includes(state.selectedMode)) {
    state.selectedMode = state.allowedModes[0] || "";
  }
  agentMode.textContent = "";
  newThreadMode.textContent = "";
  if (state.allowedModes.length === 0) {
    for (const select of [agentMode, newThreadMode]) {
      const option = document.createElement("option");
      option.value = "";
      option.textContent = state.skillsLoading ? "Loading..." : "Unavailable";
      select.appendChild(option);
      select.value = "";
    }
    agentMode.value = "";
    updateNewThreadState();
    return;
  }
  for (const mode of state.allowedModes) {
    const skill = state.skills.find((item) => item.agentMode === mode);
    for (const select of [agentMode, newThreadMode]) {
      const option = document.createElement("option");
      option.value = mode;
      option.textContent = skill?.name || mode.toUpperCase();
      select.appendChild(option);
    }
  }
  agentMode.value = state.selectedMode;
  const draftMode = state.createDraft?.agentMode;
  newThreadMode.value = state.allowedModes.includes(draftMode)
    ? draftMode
    : state.selectedMode || state.allowedModes[0];
  updateNewThreadState();
}

function isValidChatText(value) {
  if (typeof value !== "string" || !hasWellFormedUTF16(value)) return false;
  const trimmed = value.trim();
  return trimmed.length > 0 && utf8ByteLength(trimmed) <= MAX_CHAT_TEXT_BYTES;
}

function isValidThreadName(value) {
  return (
    typeof value === "string" &&
    value.length > 0 &&
    value.trim() === value &&
    hasWellFormedUTF16(value) &&
    !hasControlCharacter(value) &&
    utf8ByteLength(value) <= MAX_THREAD_NAME_BYTES
  );
}

function selectedRecoverableTurn() {
  return state.recoverableTurns.find(
    (turn) => turn.thread_uuid === state.selectedThreadUUID
  ) || null;
}

function removeRecoverableTurn(turnUUID) {
  state.recoverableTurns = state.recoverableTurns.filter(
    (turn) => turn.turn_uuid !== turnUUID
  );
  state.recoveryFeedback = "";
  state.recoveryFeedbackKind = "default";
  renderThreads();
  updateRecoveryState();
}

function recoveryPromptPreview(value) {
  const characters = Array.from(value);
  const preview = characters.slice(0, 120).join("");
  return `Prompt: ${preview}${characters.length > 120 ? "…" : ""}`;
}

function updateRecoveryState() {
  const recoverable = selectedRecoverableTurn();
  const visible = recoverable !== null;
  const busy =
    state.recoveryLoading || state.resumingTurn || state.dismissingRecovery;
  turnRecoveryCard.hidden = !visible;
  turnRecoveryCard.setAttribute("aria-busy", String(visible && busy));
  turnRecoveryResumeButton.disabled =
    !visible ||
    busy ||
    state.activeTurn !== null ||
    state.recoveringSession;
  turnRecoveryDismissButton.disabled =
    !visible || busy || state.activeTurn !== null || state.recoveringSession;
  turnRecoveryResumeButton.textContent = state.resumingTurn
    ? "Resuming..."
    : state.recoveryLoading
      ? "Checking..."
      : "Resume";
  turnRecoveryDismissButton.textContent = state.dismissingRecovery
    ? "Dismissing..."
    : "Dismiss";
  turnRecoveryDescription.textContent = state.resumingTurn
    ? "Retrying the interrupted request safely."
    : "Retry this interrupted response using the same request.";
  turnRecoveryPrompt.textContent = recoverable
    ? recoveryPromptPreview(recoverable.user_text)
    : "";
  turnRecoveryFeedback.textContent = state.recoveryFeedback;
  turnRecoveryFeedback.classList.toggle(
    "error",
    state.recoveryFeedbackKind === "error"
  );
}

function updateComposerState() {
  const authenticated = state.auth?.state === "authenticated";
  const hasThread = Boolean(state.selectedThreadUUID);
  const hasMode = state.allowedModes.includes(state.selectedMode);
  const active = state.activeTurn !== null;
  const cancelConfirmationPending = state.cancelConfirmationTurnID !== null;
  const recoverable = selectedRecoverableTurn();
  const ready =
    authenticated &&
    hasThread &&
    state.agentAvailable &&
    !state.skillsLoading &&
    !state.recoveryLoading &&
    hasMode &&
    !active &&
    !cancelConfirmationPending &&
    !recoverable &&
    !state.createFormOpen &&
    !state.recoveringSession;
  agentMode.disabled = !ready;
  chatInput.disabled = !ready;
  sendButton.disabled = !ready || !isValidChatText(chatInput.value);
  stopButton.hidden = !active;
  stopButton.disabled = !active || state.activeTurn?.stopRequested === true;

  if (state.recoveringSession) {
    composerStatus.textContent = "Your signed-in account changed. Select a thread again.";
  } else if (!authenticated) {
    composerStatus.textContent = "Sign in to continue a synced thread.";
  } else if (!hasThread) {
    composerStatus.textContent = "Select a synced thread to continue.";
  } else if (!state.agentAvailable) {
    composerStatus.textContent = "Agent streaming is unavailable in this Desktop build.";
  } else if (state.skillsLoading) {
    composerStatus.textContent = "Loading available skills...";
  } else if (state.recoveryLoading) {
    composerStatus.textContent = "Checking interrupted responses...";
  } else if (!hasMode) {
    composerStatus.textContent = "No Desktop skill is currently available.";
  } else if (recoverable) {
    composerStatus.textContent = state.resumingTurn
      ? "Resuming the interrupted response..."
      : "Resume or dismiss the interrupted response before sending another prompt.";
  } else if (cancelConfirmationPending) {
    composerStatus.textContent = "Confirming persistent cancellation...";
  } else if (active) {
    composerStatus.textContent = state.activeTurn.stopRequested
      ? "Stopping this turn..."
      : "WorkMax Agent is responding...";
  } else if (state.skillsDegraded) {
    composerStatus.textContent = `${state.selectedMode.toUpperCase()} is available; live skill details are offline.`;
  } else {
    composerStatus.textContent = `Continue with ${state.selectedMode.toUpperCase()}.`;
  }
  updateRecoveryState();
  updateNewThreadState();
}

function canGenerateThreadUUID() {
  return typeof globalThis.crypto?.randomUUID === "function";
}

function generateThreadUUID() {
  if (!canGenerateThreadUUID()) {
    throw new Error("Secure thread identity generation is unavailable.");
  }
  const value = globalThis.crypto.randomUUID();
  if (!CANONICAL_V4_UUID.test(value)) {
    throw new Error("Secure thread identity generation returned an invalid value.");
  }
  return value;
}

function canOpenNewThread() {
  return (
    state.auth?.state === "authenticated" &&
    state.agentAvailable &&
    state.createAvailable &&
    canGenerateThreadUUID() &&
    !state.skillsLoading &&
    state.allowedModes.length > 0 &&
    !state.activeTurn &&
    !state.creatingThread &&
    !state.recoveringSession &&
    !state.createFormOpen
  );
}

function renderEmptyState() {
  const authenticated = state.auth?.state === "authenticated";
  if (!authenticated) {
    emptyTitle.textContent = "Sign in to use WorkMax Agent";
    emptyDescription.textContent = "Sign in to sync presentation threads and continue from local history.";
  } else if (state.threads.length === 0 && state.createAvailable) {
    emptyTitle.textContent = "Start a presentation thread";
    emptyDescription.textContent = "Create a synced thread, then describe the deck you want to build.";
  } else if (state.threads.length === 0) {
    emptyTitle.textContent = "No synced threads yet";
    emptyDescription.textContent = "This Desktop build can continue existing threads after they appear in local history.";
  } else {
    emptyTitle.textContent = "Continue from local history";
    emptyDescription.textContent = "Select a synced thread to read its cached messages and continue the conversation.";
  }
  emptyNewThreadButton.hidden = !authenticated || !state.createAvailable;
}

function setCreateFeedback(message, kind = "error") {
  newThreadError.textContent = message;
  newThreadError.hidden = message === "";
  newThreadError.classList.toggle("pending", kind === "pending");
}

function hasAttemptedCreateDraft() {
  return state.createFormOpen && state.createDraft?.attempted === true;
}

function createFailureCode(value) {
  if (
    !isRecord(value) ||
    typeof value.error !== "string" ||
    !isSafeProtocolString(value.error, 128)
  ) {
    return "";
  }
  return value.error;
}

function classifyCreateThreadFailure(result) {
  const code = createFailureCode(result.error);
  if (result.status === 401 || code === "authentication_required") {
    return new ThreadCreateFailure(
      "Authentication is required. Cancel this draft, then sign in again.",
      "Authentication is required before creating a thread.",
      false
    );
  }
  if (code === "thread_uuid_conflict") {
    return new ThreadCreateFailure(
      "This thread identity is already owned elsewhere. Cancel and start a new draft.",
      "The generated thread identity cannot be used.",
      false
    );
  }
  if (code === "local_identity_conflict") {
    return new ThreadCreateFailure(
      "Local history has an identity conflict. Cancel this draft before refreshing.",
      "Local history could not safely accept this thread.",
      false
    );
  }
  if (
    result.status >= 500 &&
    result.status <= 599 &&
    isRecord(result.error) &&
    result.error.retry_with_same_uuid === true
  ) {
    return new ThreadCreateFailure(
      "This thread could not be completed. Retry keeps the same identity.",
      "The presentation thread could not be completed.",
      true
    );
  }
  return new ThreadCreateFailure(
    "Thread creation was rejected. Cancel this draft before continuing.",
    "The presentation thread cannot be retried.",
    false
  );
}

function updateNewThreadState() {
  const authenticated = state.auth?.state === "authenticated";
  const hasMode = state.allowedModes.includes(newThreadMode.value);
  const attempted = state.createDraft?.attempted === true;
  const pending = state.createDraft?.pending === true;
  const retryable = state.createDraft?.retryable === true;
  const validName = isValidThreadName(newThreadName.value.trim());
  const canSubmit =
    state.createFormOpen &&
    authenticated &&
    state.agentAvailable &&
    state.createAvailable &&
    !state.skillsLoading &&
    hasMode &&
    validName &&
    (!attempted || retryable) &&
    !state.activeTurn &&
    !state.creatingThread &&
    !state.recoveringSession;

  newThreadButton.disabled = !canOpenNewThread();
  emptyNewThreadButton.disabled = !canOpenNewThread();
  refreshButton.disabled =
    state.creatingThread || state.recoveringSession || hasAttemptedCreateDraft();
  refreshButton.title = hasAttemptedCreateDraft()
    ? "Cancel or complete the current thread draft before refreshing"
    : "Refresh local history";
  newThreadButton.title = state.createAvailable
    ? "Create a synced presentation thread"
    : "Thread creation is unavailable in this Desktop build";
  newThreadForm.hidden = !state.createFormOpen;
  newThreadForm.setAttribute("aria-busy", String(state.creatingThread));
  newThreadName.disabled = state.creatingThread || attempted;
  newThreadMode.disabled = state.creatingThread || attempted || state.allowedModes.length === 0;
  newThreadSubmitButton.disabled = !canSubmit;
  newThreadCancelButton.disabled = false;
  newThreadSubmitButton.textContent = state.creatingThread
    ? "Creating..."
    : pending
      ? "Retry sync"
      : attempted && retryable
        ? "Retry"
        : attempted
          ? "Cannot retry"
        : "Create";
  renderEmptyState();
}

function openNewThreadForm() {
  if (!canOpenNewThread()) {
    updateNewThreadState();
    return;
  }
  let threadUUID;
  try {
    threadUUID = generateThreadUUID();
  } catch {
    setStatus("Secure thread identity generation is unavailable.", "error");
    updateNewThreadState();
    return;
  }
  const mode = state.allowedModes.includes(state.selectedMode)
    ? state.selectedMode
    : state.allowedModes[0];
  state.createGeneration += 1;
  state.createFormOpen = true;
  state.createDraft = {
    threadUUID,
    name: "Untitled presentation",
    agentMode: mode,
    attempted: false,
    pending: false,
    retryable: false,
  };
  newThreadName.value = state.createDraft.name;
  newThreadMode.value = mode;
  setCreateFeedback("");
  updateComposerState();
  newThreadName.focus();
  if (typeof newThreadName.select === "function") {
    newThreadName.select();
  }
}

function cancelNewThreadDraft(restoreFocus = true) {
  const wasAttempted = state.createDraft?.attempted === true;
  state.createGeneration += 1;
  state.createFormOpen = false;
  state.creatingThread = false;
  state.createDraft = null;
  newThreadName.value = "Untitled presentation";
  setCreateFeedback("");
  updateComposerState();
  if (!restoreFocus) return;
  setStatus(wasAttempted ? "Thread creation canceled. A late result will be ignored." : "New thread canceled.");
  if (state.selectedThreadUUID && !chatInput.disabled) {
    chatInput.focus();
  } else {
    newThreadButton.focus();
  }
}

function isCurrentCreate(context) {
  return (
    context.sessionGeneration === state.sessionGeneration &&
    context.createGeneration === state.createGeneration &&
    context.threadUUID === state.createDraft?.threadUUID &&
    state.createFormOpen
  );
}

function upsertCreatedThread(thread) {
  state.threads = [
    thread,
    ...state.threads.filter((candidate) => candidate.uuid !== thread.uuid),
  ].sort((left, right) => Date.parse(right.updated_at) - Date.parse(left.updated_at));
}

async function submitNewThread(event) {
  event.preventDefault();
  if (state.creatingThread || !state.createFormOpen || !state.createDraft) {
    return;
  }
  const agent = desktopAgentCreateBridge();
  const name = newThreadName.value.trim();
  const mode = newThreadMode.value;
  if (
    state.auth?.state !== "authenticated" ||
    !agent ||
    !state.agentAvailable ||
    state.skillsLoading ||
    !isValidThreadName(name) ||
    !state.allowedModes.includes(mode) ||
    state.activeTurn ||
    state.recoveringSession
  ) {
    setCreateFeedback("Enter a valid name and choose an available skill.");
    updateNewThreadState();
    return;
  }
  if (state.createDraft.attempted && state.createDraft.retryable !== true) {
    updateNewThreadState();
    return;
  }

  if (!state.createDraft.attempted) {
    state.createDraft = {
      ...state.createDraft,
      name,
      agentMode: mode,
      attempted: true,
      pending: false,
      retryable: false,
    };
    newThreadName.value = name;
  }
  const draft = {
    threadUUID: state.createDraft.threadUUID,
    name: state.createDraft.name,
    agentMode: state.createDraft.agentMode,
  };
  state.createGeneration += 1;
  const context = {
    sessionGeneration: state.sessionGeneration,
    createGeneration: state.createGeneration,
    threadUUID: draft.threadUUID,
  };
  state.creatingThread = true;
  setCreateFeedback("");
  setStatus("Creating a synced presentation thread...");
  updateComposerState();

  try {
    const result = parseDesktopBridgeResult(
      await agent.createThread(draft),
      "agent create thread result"
    );
    if (!isCurrentCreate(context)) return;
    if (!result.ok) {
      if (result.status === 409 && isSessionChangedPayload(result.error)) {
        throw new SessionChangedError();
      }
      throw classifyCreateThreadFailure(result);
    }
    const data = parseCreateThreadData(result.data, result.status, draft);
    if (!isCurrentCreate(context)) return;
    if (data.state === "pending_local_sync") {
      state.createDraft = {
        ...state.createDraft,
        pending: true,
        retryable: true,
      };
      setCreateFeedback(
        "The cloud thread is ready. Retry sync with the same identity, or cancel this draft.",
        "pending"
      );
      setStatus("Thread created. Local history is still syncing.");
      return;
    }
    if (data.thread.cloud_sync_state === "paused") {
      throw new ThreadCreateFailure(
        "This existing thread is paused and cannot be continued here. Cancel this draft before continuing.",
        "The existing presentation thread is paused.",
        false
      );
    }

    upsertCreatedThread(data.thread);
    state.creatingThread = false;
    state.createFormOpen = false;
    state.createDraft = null;
    setCreateFeedback("");
    renderThreads();
    selectThread(data.thread);
    chatInput.focus();
    setStatus(data.created ? "Thread created. Ready for the first prompt." : "Thread recovered. Ready for the first prompt.");
  } catch (error) {
    if (error instanceof SessionChangedError) {
      await handleSessionChanged();
      return;
    }
    if (!isCurrentCreate(context)) return;
    const failure = error instanceof ThreadCreateFailure
      ? error
      : new ThreadCreateFailure(
          "This thread could not be completed. Retry keeps the same identity, or cancel this draft.",
          "The presentation thread could not be completed.",
          true
        );
    state.createDraft = {
      ...state.createDraft,
      pending: false,
      retryable: failure.retryable,
    };
    setCreateFeedback(failure.feedback);
    setStatus(failure.statusMessage, "error");
  } finally {
    if (isCurrentCreate(context)) {
      state.creatingThread = false;
      updateComposerState();
    }
  }
}

function renderThreads() {
  threadList.textContent = "";
  if (state.threads.length === 0) {
    const item = document.createElement("li");
    item.appendChild(renderNotice("No cached threads yet."));
    threadList.appendChild(item);
    renderEmptyState();
    return;
  }
  for (const thread of state.threads) {
    const item = document.createElement("li");
    item.className = "thread-item";
    const button = document.createElement("button");
    button.type = "button";
    button.className = "thread-button";
    button.classList.toggle("active", thread.uuid === state.selectedThreadUUID);
    button.innerHTML = "";
    const title = document.createElement("strong");
    title.textContent = thread.name || "Untitled thread";
    const meta = document.createElement("p");
    meta.textContent = `${thread.message_count || 0} messages · ${formatDate(thread.updated_at)}`;
    button.append(title, meta);
    if (
      state.recoverableTurns.some(
        (turn) => turn.thread_uuid === thread.uuid
      )
    ) {
      const badge = document.createElement("span");
      badge.className = "thread-recovery-badge";
      badge.textContent = "Interrupted";
      button.appendChild(badge);
    }
    button.addEventListener("click", () => {
      selectThread(thread);
    });
    item.appendChild(button);
    threadList.appendChild(item);
  }
  renderEmptyState();
}

function isTurnContextCurrent(activeTurn) {
  return (
    activeTurn.sessionGeneration === state.sessionGeneration &&
    activeTurn.selectionGeneration === state.selectionGeneration &&
    activeTurn.turnGeneration === state.turnGeneration &&
    activeTurn.threadUUID === state.selectedThreadUUID
  );
}

function isCurrentTurn(activeTurn) {
  return state.activeTurn === activeTurn && isTurnContextCurrent(activeTurn);
}

function requestTurnCancellation(turnID) {
  const agent = desktopAgentBridge();
  if (!agent || !turnID) return;
  void agent
    .cancelTurn(turnID)
    .then((result) => {
      parseTurnCancelResult(result);
    })
    .catch(() => {});
}

function invalidateActiveTurn(requestCancellation) {
  const activeTurn = state.activeTurn;
  if (!activeTurn) return;
  state.activeTurn = null;
  state.turnGeneration += 1;
  if (requestCancellation) {
    requestTurnCancellation(activeTurn.turnID);
  }
  updateComposerState();
}

async function handleScopedError(error, context) {
  if (error instanceof SessionChangedError) {
    await handleSessionChanged();
    return;
  }
  if (!isCurrentSelection(context)) {
    return;
  }
  setStatus(String(error), "error");
}

async function handleGlobalError(error, expectedSessionGeneration) {
  if (error instanceof SessionChangedError) {
    await handleSessionChanged();
    return;
  }
  if (expectedSessionGeneration !== state.sessionGeneration) {
    return;
  }
  setStatus(String(error), "error");
}

function clearWorkbenchForSessionChange() {
  invalidateActiveTurn(true);
  state.cancelConfirmationTurnID = null;
  state.recoveryGeneration += 1;
  state.recoverableTurns = [];
  state.recoveryLoading = false;
  state.resumingTurn = false;
  state.dismissingRecovery = false;
  state.recoveryFeedback = "";
  state.recoveryFeedbackKind = "default";
  state.createGeneration += 1;
  state.createFormOpen = false;
  state.creatingThread = false;
  state.createDraft = null;
  newThreadName.value = "Untitled presentation";
  setCreateFeedback("");
  state.selectionGeneration += 1;
  state.selectedThreadUUID = null;
  state.threads = [];
  state.skills = [];
  state.allowedModes = [];
  state.selectedMode = "";
  state.skillsLoading = false;
  state.skillsDegraded = false;
  chatInput.value = "";
  messageList.textContent = "";
  renderThreads();
  renderSkillOptions();
  emptyState.hidden = false;
  threadPanel.hidden = true;
}

async function handleSessionChanged() {
  if (state.recoveringSession) {
    return;
  }
  state.recoveringSession = true;
  state.sessionGeneration += 1;
  loginOperationGeneration += 1;
  const generation = state.sessionGeneration;
  clearWorkbenchForSessionChange();
  turnState.textContent = "Session changed";
  renderTaskContext();
  setStatus(
    "Your signed-in account changed. Select a thread again; the previous prompt was not resent and thread creation was not replayed.",
    "error"
  );
  updateComposerState();

  try {
    const auth = await loadAuthStatus(generation);
    if (generation !== state.sessionGeneration || !auth) return;
    loginButton.hidden = auth.state === "authenticated";
    if (auth.state === "authenticated") {
      const results = await Promise.allSettled([
        loadThreads(generation),
        loadSkills(generation),
        loadRecoverableTurns(generation),
      ]);
      for (const result of results) {
        if (
          result.status === "rejected" &&
          !(result.reason instanceof SessionChangedError)
        ) {
          // Keep the closed session-change notice. A refresh can retry an
          // independently failed history or catalog request.
          break;
        }
      }
    }
  } catch {
    if (generation === state.sessionGeneration) {
      state.auth = null;
      loginButton.hidden = false;
    }
  } finally {
    if (generation === state.sessionGeneration) {
      state.recoveringSession = false;
      renderSkillOptions();
      updateComposerState();
      setStatus(
        "Your signed-in account changed. Select a thread again; the previous prompt was not resent and thread creation was not replayed.",
        "error"
      );
    }
  }
}

function appendOptimisticTurn(userText) {
  const notices = Array.from(messageList.children).filter(
    (node) => node.classList?.contains("status-card")
  );
  for (const notice of notices) {
    notice.remove();
  }
  const userNode = renderMessage("user", userText);
  const assistantNode = renderMessage("assistant", "");
  messageList.append(userNode, assistantNode);
  scrollMessagesToEnd();
  return { userNode, assistantNode, assistantBubble: assistantNode.children[1] };
}

function failTurnOpen(activeTurn, userText, message) {
  if (!isCurrentTurn(activeTurn)) return;
  state.activeTurn = null;
  state.turnGeneration += 1;
  activeTurn.assistantBubble.textContent = message;
  chatInput.value = userText;
  turnState.textContent = "Error";
  renderTaskContext();
  updateComposerState();
  setStatus(message, "error");
}

function submitChat(event) {
  event.preventDefault();
  if (state.activeTurn) {
    return;
  }
  const agent = desktopAgentBridge();
  const thread = state.threads.find(
    (candidate) => candidate.uuid === state.selectedThreadUUID
  );
  const userText = chatInput.value.trim();
  if (
    state.auth?.state !== "authenticated" ||
    !agent ||
    !thread ||
    !state.allowedModes.includes(state.selectedMode) ||
    state.createFormOpen ||
    !isValidChatText(userText)
  ) {
    updateComposerState();
    return;
  }

  state.turnGeneration += 1;
  const optimistic = appendOptimisticTurn(userText);
  const activeTurn = {
    turnID: "",
    threadUUID: thread.uuid,
    userText,
    chatMode: state.selectedMode,
    sessionGeneration: state.sessionGeneration,
    selectionGeneration: state.selectionGeneration,
    turnGeneration: state.turnGeneration,
    assistantBubble: optimistic.assistantBubble,
    assistantText: "",
    assistantTextBytes: 0,
    pendingEvents: [],
    stopRequested: false,
    localCancelObserved: false,
    recoveryTurn: null,
  };
  state.activeTurn = activeTurn;
  chatInput.value = "";
  // Read the attachments before clearing the tray: startTurn below needs the
  // ids, and clearing first meant every turn was sent with an empty file list
  // — the attachment feature looked like it worked and silently dropped every
  // file.
  const fileIDs = state.pendingFiles
    .filter((file) => file.status === "ready")
    .map((file) => file.id);
  state.pendingFiles = [];
  renderAttachments();
  turnState.textContent = "Working";
  renderTaskContext();
  updateComposerState();

  try {
    const openResult = parseTurnOpenResult(
      agent.startTurn(
        {
          threadUUID: thread.uuid,
          userText,
          chatMode: state.selectedMode,
          fileIDs,
        },
        (rawEvent) => {
          if (!isCurrentTurn(activeTurn)) return;
          if (!activeTurn.turnID) {
            try {
              // Parse before buffering so malformed or legacy-open events
              // never enter renderer state, even transiently.
              activeTurn.pendingEvents.push(parseAgentTurnEvent(rawEvent));
            } catch {
              failActiveTurnProtocol(
                activeTurn,
                "The Agent stream returned an invalid event."
              );
            }
            return;
          }
          handleRawTurnEvent(activeTurn, rawEvent);
        }
      )
    );
    if (!isCurrentTurn(activeTurn)) {
      requestTurnCancellation(openResult.turnID);
      return;
    }
    activeTurn.turnID = openResult.turnID;
    const pendingEvents = activeTurn.pendingEvents;
    activeTurn.pendingEvents = [];
    for (const pendingEvent of pendingEvents) {
      if (!isCurrentTurn(activeTurn)) break;
      handleParsedTurnEvent(activeTurn, pendingEvent);
    }
  } catch {
    failTurnOpen(activeTurn, userText, "The Agent turn could not be started.");
  }
}

function recoveryContext(turnUUID) {
  return {
    sessionGeneration: state.sessionGeneration,
    selectionGeneration: state.selectionGeneration,
    recoveryGeneration: state.recoveryGeneration,
    threadUUID: state.selectedThreadUUID,
    turnUUID,
  };
}

function isCurrentRecovery(context) {
  return (
    context.sessionGeneration === state.sessionGeneration &&
    context.selectionGeneration === state.selectionGeneration &&
    context.recoveryGeneration === state.recoveryGeneration &&
    context.threadUUID === state.selectedThreadUUID &&
    state.recoverableTurns.some(
      (turn) => turn.turn_uuid === context.turnUUID
    )
  );
}

function appendRecoveredAssistant() {
  const notices = Array.from(messageList.children).filter(
    (node) => node.classList?.contains("status-card")
  );
  for (const notice of notices) {
    notice.remove();
  }
  const partial = Array.from(messageList.children)
    .reverse()
    .find(
      (node) =>
        node.classList?.contains("assistant") &&
        node.classList?.contains("partial")
    );
  if (partial?.children?.[1]) {
    partial.children[1].textContent = "";
    return partial.children[1];
  }
  const assistantNode = renderMessage("assistant", "", "streaming");
  messageList.appendChild(assistantNode);
  scrollMessagesToEnd();
  return assistantNode.children[1];
}

function resumeRecoverableTurn() {
  const recoverable = selectedRecoverableTurn();
  const agent = desktopAgentRecoveryBridge();
  if (
    !recoverable ||
    !agent ||
    state.activeTurn ||
    state.resumingTurn ||
    state.dismissingRecovery ||
    state.recoveringSession
  ) {
    updateComposerState();
    return;
  }

  state.recoveryGeneration += 1;
  const context = recoveryContext(recoverable.turn_uuid);
  state.turnGeneration += 1;
  const activeTurn = {
    turnID: "",
    threadUUID: recoverable.thread_uuid,
    userText: recoverable.user_text,
    chatMode: recoverable.chat_mode,
    sessionGeneration: state.sessionGeneration,
    selectionGeneration: state.selectionGeneration,
    turnGeneration: state.turnGeneration,
    assistantBubble: appendRecoveredAssistant(),
    assistantText: "",
    assistantTextBytes: 0,
    pendingEvents: [],
    stopRequested: false,
    localCancelObserved: false,
    recoveryTurn: recoverable,
  };
  state.activeTurn = activeTurn;
  state.resumingTurn = true;
  state.recoveryFeedback = "Retrying the interrupted request safely...";
  state.recoveryFeedbackKind = "default";
  turnState.textContent = "Resuming";
  renderTaskContext();
  updateComposerState();

  try {
    const openResult = parseTurnOpenResult(
      agent.resumeTurn(recoverable.turn_uuid, (rawEvent) => {
        if (!isCurrentTurn(activeTurn)) return;
        if (!activeTurn.turnID) {
          try {
            activeTurn.pendingEvents.push(parseAgentTurnEvent(rawEvent));
          } catch {
            failActiveTurnProtocol(
              activeTurn,
              "The recovered Agent stream returned an invalid event."
            );
          }
          return;
        }
        handleRawTurnEvent(activeTurn, rawEvent);
      })
    );
    if (!isCurrentTurn(activeTurn) || !isCurrentRecovery(context)) {
      return;
    }
    if (openResult.turnID !== recoverable.turn_uuid) {
      failActiveTurnProtocol(
        activeTurn,
        "The recovered Agent stream returned an invalid turn identifier."
      );
      return;
    }
    activeTurn.turnID = openResult.turnID;
    turnState.textContent = "Working";
  renderTaskContext();
    const pendingEvents = activeTurn.pendingEvents;
    activeTurn.pendingEvents = [];
    for (const pendingEvent of pendingEvents) {
      if (!isCurrentTurn(activeTurn)) break;
      handleParsedTurnEvent(activeTurn, pendingEvent);
    }
  } catch {
    if (!isCurrentTurn(activeTurn) || !isCurrentRecovery(context)) return;
    state.activeTurn = null;
    state.turnGeneration += 1;
    state.resumingTurn = false;
    state.recoveryFeedback = "Recovery could not connect. Select Resume to try again.";
    state.recoveryFeedbackKind = "error";
    activeTurn.assistantBubble.textContent = "Response recovery is waiting to retry.";
    turnState.textContent = "Interrupted";
    updateComposerState();
    setStatus("The interrupted response could not be resumed yet.", "error");
    turnRecoveryResumeButton.focus();
  }
}

async function dismissRecoverableTurn() {
  const recoverable = selectedRecoverableTurn();
  const agent = desktopAgentRecoveryBridge();
  if (
    !recoverable ||
    !agent ||
    state.activeTurn ||
    state.resumingTurn ||
    state.dismissingRecovery ||
    state.recoveringSession
  ) {
    return;
  }
  state.recoveryGeneration += 1;
  const context = recoveryContext(recoverable.turn_uuid);
  state.dismissingRecovery = true;
  state.recoveryFeedback = "Canceling the interrupted response...";
  state.recoveryFeedbackKind = "default";
  updateComposerState();
  try {
    const result = parseTurnCancelResult(
      await agent.cancelTurn(recoverable.turn_uuid)
    );
    if (!isCurrentRecovery(context)) return;
    if (result.turnID !== recoverable.turn_uuid) {
      throw new Error("Mismatched recovery cancel result");
    }
    state.dismissingRecovery = false;
    removeRecoverableTurn(recoverable.turn_uuid);
    turnState.textContent = "Ready";
    setStatus(
      result.canceled
        ? "Interrupted response dismissed."
        : "Interrupted response was already dismissed."
    );
    updateComposerState();
    if (!chatInput.disabled) chatInput.focus();
  } catch {
    if (!isCurrentRecovery(context)) return;
    state.dismissingRecovery = false;
    state.recoveryFeedback = "Dismiss failed. Try again when the Sidecar is available.";
    state.recoveryFeedbackKind = "error";
    updateComposerState();
    setStatus("The interrupted response could not be dismissed.", "error");
    turnRecoveryDismissButton.focus();
  }
}

function handleRawTurnEvent(activeTurn, rawEvent) {
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

function keepRecoverableTurnForRetry(activeTurn, feedback, statusMessage) {
  if (!isCurrentTurn(activeTurn) || !activeTurn.recoveryTurn) return;
  state.activeTurn = null;
  state.turnGeneration += 1;
  state.resumingTurn = false;
  state.recoveryFeedback = feedback;
  state.recoveryFeedbackKind = "error";
  if (!activeTurn.assistantText) {
    activeTurn.assistantBubble.textContent = "Response recovery is waiting to retry.";
  }
  turnState.textContent = "Interrupted";
  updateComposerState();
  setStatus(statusMessage, "error");
  turnRecoveryResumeButton.focus();
}

function isThreadBusyResult(result) {
  return result.code === "THREAD_BUSY" || result.subtype === "thread_busy";
}

function localRecoverableTurn(activeTurn, lastErrorKind = "turn_in_progress") {
  return {
    turn_uuid: activeTurn.turnID,
    thread_uuid: activeTurn.threadUUID,
    user_text: activeTurn.userText,
    chat_mode: activeTurn.chatMode,
    state: "interrupted",
    last_error_kind: lastErrorKind,
    updated_at: new Date().toISOString(),
  };
}

function retainLocalRecoverableTurn(recoverable) {
  state.recoverableTurns = [
    recoverable,
    ...state.recoverableTurns.filter(
      (candidate) => candidate.turn_uuid !== recoverable.turn_uuid
    ),
  ];
  renderThreads();
  updateRecoveryState();
}

async function refreshRecoveryAfterInitialBusy(activeTurn, fallback) {
  const expectedSessionGeneration = activeTurn.sessionGeneration;
  try {
    await loadRecoverableTurns(expectedSessionGeneration);
  } catch (error) {
    if (error instanceof SessionChangedError) {
      await handleSessionChanged();
      return;
    }
  }
  if (expectedSessionGeneration !== state.sessionGeneration) return;
  let discovered = state.recoverableTurns.some(
    (turn) => turn.turn_uuid === activeTurn.turnID
  );
  if (!discovered) {
    // The busy terminal is authoritative for this immutable local intent. A
    // temporarily stale list must not reopen the composer and invite a second
    // execution with a new UUID.
    retainLocalRecoverableTurn(fallback);
    discovered = false;
  }
  if (!isTurnContextCurrent(activeTurn)) return;
  state.recoveryFeedback = discovered
    ? "The same request is ready for an explicit recovery attempt."
    : "Persistent recovery was not visible yet; the same request is retained locally.";
  state.recoveryFeedbackKind = discovered ? "default" : "error";
  setStatus(
    discovered
      ? "This request is still busy. Select Resume to retry the same request; nothing was started again."
      : "This request is still busy. Its local recovery intent was retained; select Resume to retry the same request. Nothing was started again.",
    "error"
  );
  updateComposerState();
}

function handleInitialTurnBusy(activeTurn) {
  if (!isCurrentTurn(activeTurn) || activeTurn.recoveryTurn) return;
  const fallback = localRecoverableTurn(activeTurn);
  state.activeTurn = null;
  state.recoveryGeneration += 1;
  retainLocalRecoverableTurn(fallback);
  state.recoveryFeedback =
    "This request is still busy. Checking its persistent recovery state...";
  state.recoveryFeedbackKind = "error";
  if (!activeTurn.assistantText) {
    activeTurn.assistantBubble.textContent =
      "Response interrupted before it produced text.";
  }
  turnState.textContent = "Interrupted";
  setStatus(
    "This request is still busy; no new execution was started. Checking recovery state...",
    "error"
  );
  updateComposerState();
  void refreshRecoveryAfterInitialBusy(activeTurn, fallback);
}

function recoveredTurnErrorMessage(result) {
  const label = [result.code, result.subtype].filter(Boolean).join(" · ");
  return label
    ? `The recovered response failed (${label}).`
    : "The recovered response failed.";
}

function handleParsedTurnEvent(activeTurn, event) {
  if (!isCurrentTurn(activeTurn)) return;
  if (event.turnID !== activeTurn.turnID) {
    failActiveTurnProtocol(activeTurn, "The Agent stream returned an invalid turn identifier.");
    return;
  }

  switch (event.type) {
    case "text_delta": {
      const deltaBytes = utf8ByteLength(event.delta);
      if (activeTurn.assistantTextBytes + deltaBytes > MAX_TURN_TEXT_BYTES) {
        failActiveTurnProtocol(activeTurn, "The Agent response exceeded the display limit.");
        return;
      }
      activeTurn.assistantText += event.delta;
      activeTurn.assistantTextBytes += deltaBytes;
      activeTurn.assistantBubble.textContent = activeTurn.assistantText;
      scrollMessagesToEnd();
      return;
    }
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

function finishActiveTurn(activeTurn, label, canceled) {
  if (!isCurrentTurn(activeTurn)) return;
  state.activeTurn = null;
  if (!activeTurn.assistantText) {
    activeTurn.assistantBubble.textContent = canceled
      ? "Generation stopped."
      : "Response completed without text.";
  }
  turnState.textContent = label;
  updateComposerState();
  const context = selectionContext(activeTurn.threadUUID);
  void reconcileCompletedTurn(activeTurn.threadUUID, context);
}

function finishActiveTurnWithError(activeTurn, message) {
  if (!isCurrentTurn(activeTurn)) return;
  state.activeTurn = null;
  if (activeTurn.recoveryTurn) state.resumingTurn = false;
  const safeMessage = sanitizeErrorMessage(message || "The Agent turn failed.");
  if (!activeTurn.assistantText) {
    activeTurn.assistantBubble.textContent = safeMessage;
  }
  turnState.textContent = "Error";
  renderTaskContext();
  updateComposerState();
  setStatus(safeMessage, "error");
}

function failActiveTurnProtocol(activeTurn, message) {
  if (!isCurrentTurn(activeTurn)) return;
  if (activeTurn.recoveryTurn) {
    keepRecoverableTurnForRetry(
      activeTurn,
      "Recovery returned invalid data. Select Resume to try the same request again.",
      message
    );
    return;
  }
  requestTurnCancellation(activeTurn.turnID);
  finishActiveTurnWithError(activeTurn, message);
}

async function reconcileCompletedTurn(threadUUID, context) {
  const thread = state.threads.find((candidate) => candidate.uuid === threadUUID) || {
    uuid: threadUUID,
  };
  try {
    await Promise.all([
      loadThreads(context.sessionGeneration),
      loadMessagesForSelection(thread, context),
    ]);
  } catch (error) {
    await handleScopedError(error, context);
  }
}

async function refreshRecoveryAfterUnconfirmedCancel(activeTurn) {
  const expectedSessionGeneration = activeTurn.sessionGeneration;
  const fallback = activeTurn.recoveryTurn
    ? {
        ...activeTurn.recoveryTurn,
        last_error_kind: "cancel_unconfirmed",
        updated_at: new Date().toISOString(),
      }
    : localRecoverableTurn(activeTurn, "cancel_unconfirmed");
  try {
    await loadRecoverableTurns(expectedSessionGeneration);
  } catch (error) {
    if (error instanceof SessionChangedError) {
      await handleSessionChanged();
      return;
    }
  }
  if (expectedSessionGeneration !== state.sessionGeneration) return;
  const discovered = state.recoverableTurns.some(
    (turn) => turn.turn_uuid === activeTurn.turnID
  );
  if (!discovered) {
    // The local stream is already fenced, while the persistent cancel outcome
    // is ambiguous. Keep the immutable intent visible until the user makes an
    // explicit follow-up choice; never replay it here.
    retainLocalRecoverableTurn(fallback);
  }
  if (!isTurnContextCurrent(activeTurn)) return;
  state.recoveryFeedback = discovered
    ? "Local stop completed; persistent dismissal was not confirmed."
    : "Persistent dismissal was not confirmed; the request is retained locally.";
  state.recoveryFeedbackKind = "error";
  setStatus(
    "The stream stopped locally, but persistent dismissal was not confirmed. Review the interrupted response; it was not replayed.",
    "error"
  );
  updateComposerState();
}

function clearCancelConfirmation(activeTurn) {
  if (state.cancelConfirmationTurnID === activeTurn.turnID) {
    state.cancelConfirmationTurnID = null;
  }
}

async function stopActiveTurn() {
  const activeTurn = state.activeTurn;
  const agent = desktopAgentBridge();
  if (!activeTurn || !agent || activeTurn.stopRequested) return;
  activeTurn.stopRequested = true;
  state.cancelConfirmationTurnID = activeTurn.turnID;
  turnState.textContent = "Stopping";
  updateComposerState();
  try {
    const result = parseTurnCancelResult(await agent.cancelTurn(activeTurn.turnID));
    if (result.turnID !== activeTurn.turnID) {
      throw new Error("Mismatched Agent cancel result");
    }
    clearCancelConfirmation(activeTurn);
    if (!isCurrentTurn(activeTurn)) {
      updateComposerState();
      return;
    }
    if (result.canceled) {
      finishActiveTurn(activeTurn, "Stopped", true);
      return;
    }
    activeTurn.stopRequested = false;
    turnState.textContent = "Working";
  renderTaskContext();
    updateComposerState();
  } catch {
    clearCancelConfirmation(activeTurn);
    if (
      activeTurn.localCancelObserved &&
      activeTurn.sessionGeneration === state.sessionGeneration
    ) {
      if (isTurnContextCurrent(activeTurn)) {
        turnState.textContent = "Stopped locally";
        setStatus(
          "The stream stopped locally, but persistent dismissal was not confirmed. Checking interrupted responses...",
          "error"
        );
      }
      updateComposerState();
      void refreshRecoveryAfterUnconfirmedCancel(activeTurn);
      return;
    }
    if (!isCurrentTurn(activeTurn)) {
      updateComposerState();
      return;
    }
    activeTurn.stopRequested = false;
    turnState.textContent = "Working";
  renderTaskContext();
    updateComposerState();
    setStatus("The Agent turn could not be stopped yet.", "error");
  }
}

function setLoginFormState(visible, submitting = false) {
  loginForm.hidden = !visible;
  loginButton.hidden = visible || state.auth?.state === "authenticated";
  loginEmail.disabled = submitting;
  loginPassword.disabled = submitting;
  loginSubmitButton.disabled = submitting;
  loginCancelButton.disabled = false;
  if (!visible) {
    loginPassword.value = "";
  }
}

function showLoginFailure(result) {
  const keepForm = result.state === "awaiting_password" || result.state === "submitting";
  setLoginFormState(keepForm, result.state === "submitting");
  setStatus(LOGIN_ERROR_MESSAGES[result.error], "error");
  if (keepForm && result.state === "awaiting_password") {
    loginPassword.focus();
  }
}

async function applyLoginTransactionResult(result, pollSubmitting = false, generation = loginOperationGeneration) {
  if (generation !== loginOperationGeneration) {
    return;
  }
  if (result.error) {
    showLoginFailure(result);
    if (result.error === "busy" && result.state === "submitting" && pollSubmitting) {
      await waitForLoginTransaction(generation);
    }
    return;
  }
  switch (result.state) {
    case "idle":
      setLoginFormState(false);
      setStatus(`Auth state: ${state.auth?.state || "unauthenticated"}. Sign in to sync cloud history.`);
      return;
    case "awaiting_password":
      setLoginFormState(true);
      setStatus("Enter your WorkMax email and password to continue.");
      if (loginEmail.value) {
        loginPassword.focus();
      } else {
        loginEmail.focus();
      }
      return;
    case "submitting":
      setLoginFormState(true, true);
      setStatus("Completing sign-in securely...");
      if (pollSubmitting) {
        await waitForLoginTransaction(generation);
      }
      return;
    case "authenticated":
      setLoginFormState(false);
      setStatus("Sign-in complete. Loading your local history...");
      await refresh();
      return;
  }
}

async function waitForLoginTransaction(generation) {
  const auth = desktopAuthBridge();
  if (!auth) {
    showLoginBridgeUnavailable();
    return;
  }
  const started = Date.now();
  while (
    generation === loginOperationGeneration &&
    Date.now() - started < AUTH_POLL_TIMEOUT_MS
  ) {
    await sleep(AUTH_POLL_INTERVAL_MS);
    let result;
    try {
      result = parseLoginTransactionResult(await auth.loginStatus());
    } catch {
      if (generation === loginOperationGeneration) {
        showLoginBridgeUnavailable();
      }
      return;
    }
    if (generation !== loginOperationGeneration) {
      return;
    }
    if (result.state !== "submitting" || result.error) {
      await applyLoginTransactionResult(result, false, generation);
      return;
    }
  }
  if (generation !== loginOperationGeneration) {
    return;
  }
  setLoginFormState(false);
  setStatus(LOGIN_ERROR_MESSAGES.expired, "error");
}

function showLoginBridgeUnavailable() {
  setLoginFormState(false);
  setStatus(LOGIN_ERROR_MESSAGES.unavailable, "error");
}

async function login() {
  const auth = desktopAuthBridge();
  if (!auth) {
    showLoginBridgeUnavailable();
    return;
  }
  const generation = ++loginOperationGeneration;
  loginButton.disabled = true;
  setStatus("Preparing a secure sign-in session...");
  try {
    const result = parseLoginTransactionResult(await auth.beginLogin());
    await applyLoginTransactionResult(result, true, generation);
  } catch {
    if (generation === loginOperationGeneration) {
      showLoginBridgeUnavailable();
    }
  } finally {
    loginButton.disabled = false;
  }
}

function validLoginCredential(value, minBytes, maxBytes) {
  return (
    typeof value === "string" &&
    hasWellFormedUTF16(value) &&
    !/\p{Cc}/u.test(value) &&
    utf8ByteLength(value) >= minBytes &&
    utf8ByteLength(value) <= maxBytes
  );
}

function validLoginEmail(value) {
  return (
    validLoginCredential(value, 3, 320) &&
    value.trim() === value &&
    value.includes("@")
  );
}

function hasWellFormedUTF16(value) {
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

async function submitLogin(event) {
  event.preventDefault();
  const auth = desktopAuthBridge();
  const email = loginEmail.value.trim();
  let password = loginPassword.value;
  loginPassword.value = "";
  const generation = ++loginOperationGeneration;
  try {
    if (!auth) {
      showLoginBridgeUnavailable();
      return;
    }
    if (!validLoginEmail(email) || !validLoginCredential(password, 1, 1024)) {
      setLoginFormState(true);
      setStatus(LOGIN_ERROR_MESSAGES.invalid_request, "error");
      return;
    }
    loginEmail.value = email;
    setLoginFormState(true, true);
    setStatus("Checking your credentials...");
    let result;
    try {
      result = parseLoginTransactionResult(
        await auth.submitLoginPassword({ email, password })
      );
    } catch {
      if (
        generation === loginOperationGeneration &&
        !(await reconcileAmbiguousLoginOutcome(generation))
      ) {
        showLoginBridgeUnavailable();
      }
      return;
    }
    // Main deliberately collapses transport/protocol failures to the closed
    // public `unavailable` result. The password may nevertheless have reached
    // the Server and committed a session before the local response was lost,
    // so reconcile via the non-secret session status before showing failure.
    // Never replay the credential-bearing command.
    if (
      result.error === "unavailable" &&
      (await reconcileAmbiguousLoginOutcome(generation))
    ) {
      return;
    }
    await applyLoginTransactionResult(result, true, generation);
  } finally {
    password = "";
    loginPassword.value = "";
    if (!loginForm.hidden && !loginSubmitButton.disabled) {
      loginPassword.focus();
    }
  }
}

async function reconcileAmbiguousLoginOutcome(generation) {
  try {
    const sessionGeneration = state.sessionGeneration;
    const current = await loadAuthStatus(sessionGeneration);
    if (
      generation !== loginOperationGeneration ||
      !current ||
      current.state !== "authenticated"
    ) {
      return false;
    }
    setLoginFormState(false);
    setStatus(`Authenticated${current.tier ? ` · ${current.tier}` : ""}. Reading local cache.`);
    await Promise.all([
      loadThreads(sessionGeneration),
      loadSkills(sessionGeneration),
    ]);
    return true;
  } catch {
    return false;
  }
}

async function cancelLogin() {
  const auth = desktopAuthBridge();
  loginPassword.value = "";
  if (!auth) {
    showLoginBridgeUnavailable();
    return;
  }
  const generation = ++loginOperationGeneration;
  loginCancelButton.disabled = true;
  try {
    const result = parseLoginTransactionResult(await auth.cancelLogin());
    if (generation !== loginOperationGeneration) {
      return;
    }
    if (result.error && result.error !== "canceled") {
      showLoginFailure(result);
      return;
    }
    setLoginFormState(false);
    setStatus(LOGIN_ERROR_MESSAGES.canceled);
  } catch {
    if (generation === loginOperationGeneration) {
      showLoginBridgeUnavailable();
    }
  } finally {
    loginPassword.value = "";
    loginCancelButton.disabled = false;
  }
}

async function restoreLoginTransaction() {
  const auth = desktopAuthBridge();
  if (!auth) {
    showLoginBridgeUnavailable();
    return;
  }
  const generation = ++loginOperationGeneration;
  try {
    const result = parseLoginTransactionResult(await auth.loginStatus());
    await applyLoginTransactionResult(result, true, generation);
  } catch {
    if (generation === loginOperationGeneration) {
      showLoginBridgeUnavailable();
    }
  }
}

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

async function refresh() {
  const api = bridge();
  if (!api) {
    state.agentAvailable = false;
    state.createAvailable = false;
    loginButton.hidden = true;
    updateComposerState();
    setStatus("This bundled renderer must run inside WorkMax Desktop.", "error");
    return;
  }
  if (state.recoveringSession) {
    return;
  }
  if (hasAttemptedCreateDraft()) {
    const retryable = state.createDraft.retryable === true;
    setStatus(
      retryable
        ? "Retry or cancel the current thread draft before refreshing."
        : "Cancel the current thread draft before refreshing.",
      "error"
    );
    (retryable ? newThreadSubmitButton : newThreadCancelButton).focus();
    updateNewThreadState();
    return;
  }
  state.sessionGeneration += 1;
  const generation = state.sessionGeneration;
  invalidateActiveTurn(true);
  state.cancelConfirmationTurnID = null;
  state.recoveryGeneration += 1;
  state.recoverableTurns = [];
  state.recoveryLoading = false;
  state.resumingTurn = false;
  state.dismissingRecovery = false;
  state.recoveryFeedback = "";
  state.recoveryFeedbackKind = "default";
  state.createGeneration += 1;
  state.createFormOpen = false;
  state.creatingThread = false;
  state.createDraft = null;
  newThreadName.value = "Untitled presentation";
  setCreateFeedback("");
  state.selectionGeneration += 1;
  state.selectedThreadUUID = null;
  state.skills = [];
  state.allowedModes = [];
  state.selectedMode = "";
  state.skillsLoading = false;
  state.skillsDegraded = false;
  chatInput.value = "";
  messageList.textContent = "";
  renderSkillOptions();
  emptyState.hidden = false;
  threadPanel.hidden = true;
  turnState.textContent = "Ready";
  updateComposerState();
  runtimeLabel.textContent = `sidecar ${api.sidecarVersion || "unknown"} · app ${api.appVersion || "unknown"}`;
  setStatus("Checking auth status...");
  try {
    const auth = await loadAuthStatus(generation);
    if (!auth || generation !== state.sessionGeneration) return;
    loginButton.hidden = auth.state === "authenticated";
    if (auth.state !== "authenticated") {
      state.threads = [];
      renderThreads();
      emptyState.hidden = false;
      threadPanel.hidden = true;
      updateComposerState();
      setStatus(`Auth state: ${auth.state}. Sign in to sync cloud history.`);
      await restoreLoginTransaction();
      return;
    }
    setLoginFormState(false);
    setStatus(`Authenticated${auth.tier ? ` · ${auth.tier}` : ""}. Reading local cache.`);
    await Promise.all([
      loadThreads(generation),
      loadSkills(generation),
      loadRecoverableTurns(generation),
    ]);
  } catch (error) {
    if (error instanceof SessionChangedError) {
      await handleSessionChanged();
      return;
    }
    if (generation === state.sessionGeneration) {
      loginButton.hidden = true;
      setStatus(String(error), "error");
      updateComposerState();
    }
  }
}

refreshButton.addEventListener("click", () => {
  void refresh();
});
if (modelsButton) {
  modelsButton.addEventListener("click", () => {
    void openModelSettings();
  });
}
if (modelPreferredRoute) {
  modelPreferredRoute.addEventListener("change", () => {
    updateModelLocalFieldsVisibility();
  });
}
if (modelSettingsForm) {
  modelSettingsForm.addEventListener("submit", (event) => {
    void submitModelSettings(event);
  });
}
if (modelSettingsCancelButton) {
  modelSettingsCancelButton.addEventListener("click", () => {
    closeModelSettings();
  });
}
loginButton.addEventListener("click", () => {
  void login();
});
loginForm.addEventListener("submit", (event) => {
  void submitLogin(event);
});
loginCancelButton.addEventListener("click", () => {
  void cancelLogin();
});
newThreadButton.addEventListener("click", () => {
  openNewThreadForm();
});
emptyNewThreadButton.addEventListener("click", () => {
  openNewThreadForm();
});
newThreadForm.addEventListener("submit", (event) => {
  void submitNewThread(event);
});
newThreadForm.addEventListener("keydown", (event) => {
  if (event.repeat) return;
  if (event.key === "Escape") {
    event.preventDefault();
    cancelNewThreadDraft();
    return;
  }
  if (event.key === "Enter") {
    event.preventDefault();
    void submitNewThread(event);
  }
});
newThreadCancelButton.addEventListener("click", () => {
  cancelNewThreadDraft();
});
newThreadName.addEventListener("input", () => {
  updateNewThreadState();
});
newThreadMode.addEventListener("change", () => {
  updateNewThreadState();
});
attachButton.addEventListener("click", () => {
  fileInput.click();
});
fileInput.addEventListener("change", () => {
  const files = Array.from(fileInput.files || []);
  fileInput.value = "";
  for (const file of files) {
    uploadThreadFile(file);
  }
});
chatForm.addEventListener("submit", (event) => {
  submitChat(event);
});
chatInput.addEventListener("input", () => {
  updateComposerState();
});
chatInput.addEventListener("keydown", (event) => {
  if (
    event.key === "Enter" &&
    (event.metaKey || event.ctrlKey) &&
    !event.repeat
  ) {
    submitChat(event);
  }
});
agentMode.addEventListener("change", () => {
  if (state.allowedModes.includes(agentMode.value) && !state.activeTurn) {
    state.selectedMode = agentMode.value;
  }
  updateComposerState();
});
stopButton.addEventListener("click", () => {
  void stopActiveTurn();
});
turnRecoveryResumeButton.addEventListener("click", () => {
  resumeRecoverableTurn();
});
turnRecoveryDismissButton.addEventListener("click", () => {
  void dismissRecoverableTurn();
});

void refresh();

// ---------------------------------------------------------------------------
// Task context panel
// ---------------------------------------------------------------------------
//
// The right rail describes the current run: which steps have happened, what
// the agent was given, and what it produced. Every value here is derived from
// state this renderer already holds or reads back from the sidecar.
//
// Deliverables is deliberately an empty state with a reason rather than a
// hidden section. A local turn produces text, not files — the panel says so
// instead of showing an empty box that looks broken.

// Looked up on use rather than bound at module scope. These functions are
// called from code that runs earlier in the file, and a const initialised down
// here would be in its temporal dead zone at that point — a ReferenceError
// that would only appear once an attachment or a turn touched the panel.
function ctxEl(id) {
  return document.querySelector(`#${id}`);
}

const contextState = { sources: [], sourcesThreadUUID: null };

function formatFileSize(bytes) {
  if (!Number.isFinite(bytes) || bytes < 0) return "";
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

// Mirrors the reference implementation's four steps. "Brief captured" is true
// once the thread has any message or a turn is running, because that is the
// point at which the agent has actually been told something.
function buildRunSteps() {
  const running = Boolean(state.activeTurn);
  const messageCount = messageList ? messageList.children.length : 0;
  const sourceCount = contextState.sources.length + state.pendingFiles.length;
  const failed = turnState && turnState.textContent === "Error";

  const brief = messageCount > 0 || running;
  return [
    {
      label: "Brief captured",
      state: brief ? "complete" : "pending",
      detail: brief ? "Complete" : "Waiting",
    },
    {
      label: "Sources",
      // No sources is a valid way to run, so this is never "pending" —
      // reporting it as incomplete would imply the run is blocked on it.
      state: sourceCount > 0 ? "complete" : "neutral",
      detail: sourceCount > 0
        ? `${sourceCount} source${sourceCount === 1 ? "" : "s"}`
        : "None attached",
    },
    {
      label: "Agent execution",
      state: failed ? "failed" : running ? "active" : brief ? "complete" : "pending",
      detail: failed ? "Failed" : running ? "In progress" : brief ? "Complete" : "Waiting",
    },
    {
      label: "Deliverables",
      state: "neutral",
      detail: "Text only",
    },
  ];
}

const RUN_STEP_MARKS = {
  complete: "✓",
  active: "◐",
  failed: "✕",
  pending: "○",
  neutral: "–",
};

function renderRunOverview() {
  if (!ctxEl("run-overview-list")) return;
  const steps = buildRunSteps();
  ctxEl("run-overview-list").innerHTML = "";
  for (const step of steps) {
    const item = document.createElement("li");
    item.className = `run-step is-${step.state}`;

    const mark = document.createElement("span");
    mark.className = "run-step-mark";
    mark.textContent = RUN_STEP_MARKS[step.state] ?? "–";

    const label = document.createElement("span");
    label.className = "run-step-label";
    label.textContent = step.label;

    const detail = document.createElement("span");
    detail.className = "run-step-detail";
    detail.textContent = step.detail;

    item.append(mark, label, detail);
    ctxEl("run-overview-list").appendChild(item);
  }
  const done = steps.filter((s) => s.state === "complete").length;
  if (ctxEl("run-overview-meta")) ctxEl("run-overview-meta").textContent = `${done}/${steps.length}`;
}

function renderSources() {
  if (!ctxEl("sources-list")) return;
  // Files uploaded in this session but not yet reloaded from the sidecar are
  // shown alongside the persisted ones, so a just-attached file appears
  // immediately rather than after the next refresh.
  const pending = state.pendingFiles
    .filter((file) => file.status !== "error")
    .map((file) => ({
      file_id: file.id,
      file_name: file.name,
      file_size: file.size,
      on_disk: true,
      pending: file.status === "uploading",
    }));
  const persistedIds = new Set(contextState.sources.map((f) => f.file_id));
  const items = [
    ...contextState.sources,
    ...pending.filter((f) => !f.file_id || !persistedIds.has(f.file_id)),
  ];

  ctxEl("sources-list").innerHTML = "";
  for (const file of items) {
    const item = document.createElement("li");
    item.className = "context-item" + (file.on_disk === false ? " is-missing" : "");

    const name = document.createElement("span");
    name.className = "context-item-name";
    name.textContent = file.file_name;

    const meta = document.createElement("span");
    meta.className = "context-item-meta";
    meta.textContent = file.on_disk === false
      // The row survives but the bytes do not, which is a different problem
      // from "no attachments" and needs to be visible.
      ? "Missing on disk"
      : file.pending
        ? "Uploading…"
        : formatFileSize(file.file_size);

    item.append(name, meta);
    ctxEl("sources-list").appendChild(item);
  }

  if (ctxEl("sources-empty")) ctxEl("sources-empty").hidden = items.length > 0;
  if (ctxEl("sources-meta")) ctxEl("sources-meta").textContent = String(items.length);
  if (ctxEl("deliverables-meta")) ctxEl("deliverables-meta").textContent = "0";
  if (ctxEl("context-count")) ctxEl("context-count").textContent = String(items.length);
}

function renderTaskContext() {
  renderRunOverview();
  renderSources();
}

// Reads the attachments the sidecar has for this thread. Uploads persisted
// before this route existed, but nothing could read them back, so reopening a
// thread showed an empty Sources panel while the files were still on disk.
async function loadThreadSources(threadUUID) {
  contextState.sourcesThreadUUID = threadUUID;
  contextState.sources = [];
  renderTaskContext();
  if (!threadUUID) return;

  const agent = desktopAgentBridge();
  if (!agent || typeof agent.listThreadFiles !== "function") return;
  try {
    const result = await agent.listThreadFiles(threadUUID);
    // A thread switch mid-flight must not paint the previous thread's files.
    if (contextState.sourcesThreadUUID !== threadUUID) return;
    if (result && result.ok && result.data && Array.isArray(result.data.items)) {
      contextState.sources = result.data.items;
    }
  } catch {
    // The panel degrades to session-only sources; the conversation itself is
    // unaffected, so this must not surface as a turn error.
  }
  renderTaskContext();
}

// Paint the panel once on load. Without this it keeps whatever static markup
// index.html shipped with — which looks like a rendered panel that is simply
// empty, rather than one that never ran.
renderTaskContext();
