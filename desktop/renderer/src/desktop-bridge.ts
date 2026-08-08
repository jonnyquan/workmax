import type {
  LoginPasswordInput,
  LoginTransactionResult,
} from "./login-types";

export const DESKTOP_BRIDGE_VERSION = "1.0.0-alpha.7" as const;

export interface LegacyBridgeResponse {
  ok: boolean;
  status: number;
  statusText: string;
  url: string;
  headers: Record<string, string>;
  body: { getReader: () => ReadableStreamDefaultReader<Uint8Array> } | null;
  text: () => Promise<string>;
  json: () => Promise<unknown>;
}

export interface DesktopBridgeRuntime {
  appVersion: string;
  sidecarVersion: string;
  // A plain string, not NodeJS.Platform: the renderer is a web page now, and
  // the value is informational — nothing branches on it being one of Node's
  // known constants.
  platform: string;
}

export interface DesktopBridgeSuccess<T> {
  ok: true;
  status: number;
  statusText: string;
  headers: Record<string, string>;
  data: T | null;
}

export interface DesktopBridgeFailure {
  ok: false;
  status: number;
  statusText: string;
  headers: Record<string, string>;
  error: unknown;
}

export type DesktopBridgeResult<T> =
  | DesktopBridgeSuccess<T>
  | DesktopBridgeFailure;

export interface AuthStatus {
  state: "authenticated" | "unauthenticated" | "expired";
  user_id?: string;
  tier?: string;
  updated_at: string;
}

export interface AuthUserInfo {
  user_id: string;
  email: string;
  display_name: string;
  avatar_url?: string;
  tier: string;
  tier_expires_at?: string;
  quota: {
    month_used: number;
    month_limit: number;
  };
}

export interface AuthLogout {
  ok: boolean;
  revoke_status: string;
}

export interface LocalThread {
  uuid: string;
  name: string;
  agent_mode: string;
  message_count: number;
  updated_at: string;
  cloud_sync_state: string;
}

export interface LocalMessage {
  uuid: string;
  user_text: string;
  ai_text: string;
  chat_mode: string;
  streaming_state: string;
  created_at: string;
  updated_at: string;
}

export interface LocalThreadList {
  items: LocalThread[];
  count: number;
}

export interface LocalMessageList {
  items: LocalMessage[];
  count: number;
}

export interface SidecarHealth {
  status: string;
  version: string;
  build: string;
  uptime_seconds: number;
  sqlite: string;
  auth: string;
}

export interface SidecarDiagnostics {
  sidecar: {
    version: string;
    uptime_seconds: number;
    heap_alloc_bytes: number;
    heap_sys_bytes: number;
    num_goroutine: number;
    data_dir?: string;
    db_path?: string;
    backup_path?: string;
    integrity_check?: string;
    applied_migrations?: string[];
  };
  threads_syncer: {
    configured: boolean;
    running?: boolean;
    consecutive_fail?: number;
    last_tick_at?: string;
    last_error?: string;
    total_ticks?: number;
    total_fails?: number;
    last_duration_ms?: number;
  };
  messages_sync: {
    configured: boolean;
    active_threads: number;
    total_triggered: number;
  };
  network_state: {
    configured: boolean;
    state?: string;
    last_probe_at?: string;
  };
  auth: {
    configured: boolean;
    state?: string;
    persistence_state?: "ok" | "degraded" | "unavailable";
  };
}

export interface ServerVersion {
  min_supported: string;
  latest_recommended: string;
  sidecar_version: string;
  release_notes_url?: string;
}

export interface RendererLogInput {
  level: "error" | "warn" | "info";
  message: string;
  context?: Readonly<Record<string, unknown>>;
}

/** preferred_route: local uses user endpoint; official uses workmax.app quota. */
export type ModelPreferredRoute = "local" | "official";

export type LocalModelProtocol =
  | "openai_compatible"
  | "anthropic_compatible";

/** Renderer-safe model route DTO. Never includes api_key. */
export interface ModelRouteSettings {
  preferred_route: ModelPreferredRoute;
  local: {
    protocol: string;
    base_url: string;
    model_id: string;
    api_key_configured: boolean;
  };
  updated_at: string;
}

export interface ModelRouteSettingsPut {
  preferred_route: ModelPreferredRoute;
  local?: {
    protocol: LocalModelProtocol;
    base_url: string;
    model_id: string;
    /** Write-only; omit to leave Keychain unchanged. */
    api_key?: string;
    clear_api_key?: boolean;
  };
}

export interface AgentSkillArtifacts {
  primaryType: string;
  outputTypes: string[];
  previewTypes: string[];
  exportTargets: string[];
  critiqueAnchors: string[];
}

export interface AgentSkill {
  agentMode: string;
  name: string;
  description: string;
  version: string;
  hasQuestionForm: boolean;
  hasDirectionsFallback: boolean;
  hasPostScripts: boolean;
  artifacts?: AgentSkillArtifacts;
  labelKey: string;
  descriptionKey: string;
}

// One attachment already on a thread. Uploads have always persisted these,
// but nothing could read them back until agent.listThreadFiles existed.
export interface AgentThreadFile {
  file_id: number;
  file_name: string;
  file_size: number;
  file_type: string;
  mime_type: string;
  on_disk: boolean;
  created_at: string;
}

export interface AgentThreadFileList {
  items: AgentThreadFile[];
  count: number;
}

export interface AgentSkillCatalog {
  items: AgentSkill[];
  count: number;
  allowed_modes: string[];
}

export interface AgentTurnInput {
  threadUUID: string;
  userText: string;
  chatMode: string;
  fileIDs?: number[];
}

export interface AgentRecoverableTurn {
  turn_uuid: string;
  thread_uuid: string;
  user_text: string;
  chat_mode: string;
  state: "interrupted";
  last_error_kind: string;
  updated_at: string;
}

export interface AgentRecoverableTurnList {
  items: AgentRecoverableTurn[];
  count: number;
}

export interface AgentCreateThreadInput {
  threadUUID: string;
  name: string;
  agentMode: string;
}

export type AgentCreateThreadResult =
  | {
      state: "ready";
      created: boolean;
      thread: LocalThread;
    }
  | {
      state: "pending_local_sync";
      thread_uuid: string;
    };

export interface AgentThreadFileUploadResult {
  file_id: number;
  file_name: string;
  mime_type: string;
  file_type: string;
  file_size: number;
}

/**
 * Internal, fixed Sidecar request built from AgentTurnInput. Renderer callers
 * can neither widen this envelope nor supply a URL, RequestInit, stream, UID,
 * token, or cancellation primitive.
 */
export interface AgentSidecarTurnRequest {
  turn_uuid: string;
  thread_uuid: string;
  user_text: string;
  chat_mode: string;
  payload: {
    stream: true;
    files?: number[];
  };
}

export type AgentSidecarTurnStartRequest = Omit<
  AgentSidecarTurnRequest,
  "turn_uuid"
>;

export interface AgentTurnOpenResult {
  turnID: string;
}

export interface AgentTurnCancelResult {
  turnID: string;
  canceled: boolean;
}

export interface AgentProxyError {
  kind: string;
  message: string;
  retryable?: boolean;
  retry_after_ms?: number;
  log_id?: string;
}

export interface AgentDoneResult {
  code: string;
  subtype: string;
  is_error: boolean;
}

export type AgentTurnEvent =
  | { type: "text_delta"; turnID: string; delta: string }
  | { type: "unknown"; turnID: string; event: string }
  | { type: "done"; turnID: string; result: AgentDoneResult }
  | { type: "proxy_error"; turnID: string; error: AgentProxyError }
  | { type: "canceled"; turnID: string }
  | {
      type: "protocol_error";
      turnID: string;
      code: string;
      message: string;
    };

export type AgentTurnEventCallback = (event: AgentTurnEvent) => void;

export interface DesktopBridgeCapabilities {
  bridgeVersion: typeof DESKTOP_BRIDGE_VERSION;
  compatibility: {
    legacyGlobal: "workmaxLocal";
    legacyFetch: true;
  };
  namespaces: {
    auth: CapabilityNamespace;
    history: CapabilityNamespace;
    system: CapabilityNamespace;
    agent: CapabilityNamespace;
    settings: CapabilityNamespace;
    artifact: CapabilityNamespace;
  };
}

export interface CapabilityNamespace {
  supported: boolean;
  methods: string[];
  deferred?: string[];
  reason?: string;
}

export interface DesktopBridge {
  version: typeof DESKTOP_BRIDGE_VERSION;
  runtime: DesktopBridgeRuntime;
  capabilities: () => DesktopBridgeCapabilities;
  auth: {
    status: () => Promise<DesktopBridgeResult<AuthStatus>>;
    userInfo: () => Promise<DesktopBridgeResult<AuthUserInfo>>;
    beginLogin: () => Promise<LoginTransactionResult>;
    loginStatus: () => Promise<LoginTransactionResult>;
    submitLoginPassword: (
      input: LoginPasswordInput
    ) => Promise<LoginTransactionResult>;
    cancelLogin: () => Promise<LoginTransactionResult>;
    logout: () => Promise<DesktopBridgeResult<AuthLogout>>;
  };
  history: {
    listThreads: (
      options?: ListThreadsOptions
    ) => Promise<DesktopBridgeResult<LocalThreadList>>;
    listMessages: (
      options: ListMessagesOptions
    ) => Promise<DesktopBridgeResult<LocalMessageList>>;
  };
  agent: {
    listSkills: () => Promise<DesktopBridgeResult<AgentSkillCatalog>>;
    createThread: (
      input: AgentCreateThreadInput
    ) => Promise<DesktopBridgeResult<AgentCreateThreadResult>>;
    listThreadFiles: (
      threadUUID: string
    ) => Promise<DesktopBridgeResult<AgentThreadFileList>>;
    listRecoverableTurns: () => Promise<
      DesktopBridgeResult<AgentRecoverableTurnList>
    >;
    startTurn: (
      input: AgentTurnInput,
      callback: AgentTurnEventCallback
    ) => AgentTurnOpenResult;
    resumeTurn: (
      turnUUID: string,
      callback: AgentTurnEventCallback
    ) => AgentTurnOpenResult;
    cancelTurn: (turnID: string) => Promise<AgentTurnCancelResult>;
    uploadThreadFile: (
      threadUUID: string,
      file: File
    ) => Promise<DesktopBridgeResult<AgentThreadFileUploadResult>>;
  };
  system: {
    health: () => Promise<DesktopBridgeResult<SidecarHealth>>;
    diagnostics: () => Promise<DesktopBridgeResult<SidecarDiagnostics>>;
    serverVersion: () => Promise<DesktopBridgeResult<ServerVersion>>;
    triggerSync: (
      options?: TriggerSyncOptions
    ) => Promise<DesktopBridgeResult<null>>;
    writeLog: (
      entry: RendererLogInput
    ) => Promise<DesktopBridgeResult<null>>;
    revealDataDir: () => Promise<{
      ok: boolean;
      path?: string;
      error?: string;
    }>;
  };
  settings: {
    getModelRoute: () => Promise<DesktopBridgeResult<ModelRouteSettings>>;
    putModelRoute: (
      input: ModelRouteSettingsPut
    ) => Promise<DesktopBridgeResult<ModelRouteSettings>>;
  };
}

export interface ListThreadsOptions {
  limit?: number;
  includePaused?: boolean;
}

export interface ListMessagesOptions {
  threadUUID: string;
  limit?: number;
}

export interface TriggerSyncOptions {
  threadUUID?: string;
}

interface DesktopBridgeDependencies {
  runtime: DesktopBridgeRuntime;
  request: (
    path: string,
    init: RequestInit
  ) => Promise<LegacyBridgeResponse>;
  beginLogin: () => Promise<LoginTransactionResult>;
  loginStatus: () => Promise<LoginTransactionResult>;
  submitLoginPassword: (
    input: LoginPasswordInput
  ) => Promise<LoginTransactionResult>;
  cancelLogin: () => Promise<LoginTransactionResult>;
  startAgentTurn: (
    request: AgentSidecarTurnStartRequest,
    callback: AgentTurnEventCallback
  ) => AgentTurnOpenResult;
  resumeAgentTurn: (
    turnUUID: string,
    callback: AgentTurnEventCallback
  ) => AgentTurnOpenResult;
  cancelAgentTurn: (turnID: string) => Promise<AgentTurnCancelResult>;
  revealDataDir: () => Promise<{
    ok: boolean;
    path?: string;
    error?: string;
  }>;
}

type TypedRequestBody = "none" | "json" | "multipart";

interface TypedRouteSpec {
  bridgeMethod: string;
  routeID: string;
  method: "GET" | "POST" | "PUT";
  path: string;
  body: TypedRequestBody;
  contentType: "application/json" | null;
}

function defineTypedRoute(
  bridgeMethod: string,
  routeID: string,
  method: "GET" | "POST" | "PUT",
  path: string,
  body: TypedRequestBody,
  contentType: "application/json" | null
): TypedRouteSpec {
  return { bridgeMethod, routeID, method, path, body, contentType };
}

// Kept as direct declarations so the boundary checker can compare every typed
// facade method with the authoritative Desktop route manifest.
const ROUTES = {
  authStatus: defineTypedRoute(
    "auth.status", "auth.status", "GET", "/auth/status", "none", null
  ),
  authUserInfo: defineTypedRoute(
    "auth.userInfo", "auth.userinfo", "GET", "/auth/userinfo", "none", null
  ),
  authLogout: defineTypedRoute(
    "auth.logout", "auth.logout", "POST", "/auth/logout", "none", null
  ),
  historyListThreads: defineTypedRoute(
    "history.listThreads", "agent.threads", "GET", "/agent/threads", "none", null
  ),
  historyListMessages: defineTypedRoute(
    "history.listMessages",
    "agent.thread-messages",
    "GET",
    "/agent/threads/:uuid/messages",
    "none",
    null
  ),
  agentListSkills: defineTypedRoute(
    "agent.listSkills",
    "agent.skills.catalog",
    "GET",
    "/agent/skills/catalog",
    "none",
    null
  ),
  agentCreateThread: defineTypedRoute(
    "agent.createThread",
    "agent.thread-put",
    "PUT",
    "/agent/threads/:uuid",
    "json",
    "application/json"
  ),
  agentStartTurn: defineTypedRoute(
    "agent.startTurn",
    "agent.chat",
    "POST",
    "/agent/chat",
    "json",
    "application/json"
  ),
  agentListRecoverableTurns: defineTypedRoute(
    "agent.listRecoverableTurns",
    "agent.turns-recoverable",
    "GET",
    "/agent/turns/recoverable",
    "none",
    null
  ),
  agentResumeTurn: defineTypedRoute(
    "agent.resumeTurn",
    "agent.turn-replay",
    "POST",
    "/agent/turns/:uuid/replay",
    "none",
    null
  ),
  agentCancelTurn: defineTypedRoute(
    "agent.cancelTurn",
    "agent.turn-cancel",
    "POST",
    "/agent/turns/:uuid/cancel",
    "none",
    null
  ),
  systemHealth: defineTypedRoute(
    "system.health", "health.get", "GET", "/health", "none", null
  ),
  systemDiagnostics: defineTypedRoute(
    "system.diagnostics",
    "system.diagnostics",
    "GET",
    "/system/diagnostics",
    "none",
    null
  ),
  systemServerVersion: defineTypedRoute(
    "system.serverVersion",
    "system.server-version",
    "GET",
    "/system/server-version",
    "none",
    null
  ),
  systemTriggerSync: defineTypedRoute(
    "system.triggerSync",
    "system.trigger-sync",
    "POST",
    "/system/trigger-sync",
    "none",
    null
  ),
  systemWriteLog: defineTypedRoute(
    "system.writeLog",
    "system.log",
    "POST",
    "/system/log",
    "json",
    "application/json"
  ),
  settingsGetModelRoute: defineTypedRoute(
    "settings.getModelRoute",
    "settings.model-route.get",
    "GET",
    "/settings/model-route",
    "none",
    null
  ),
  settingsPutModelRoute: defineTypedRoute(
    "settings.putModelRoute",
    "settings.model-route.put",
    "PUT",
    "/settings/model-route",
    "json",
    "application/json"
  ),
  agentUploadThreadFile: defineTypedRoute(
    "agent.uploadThreadFile",
    "agent.thread-file-upload",
    "POST",
    "/agent/threads/:uuid/files",
    "multipart",
    null
  ),
  agentListThreadFiles: defineTypedRoute(
    "agent.listThreadFiles",
    "agent.thread-file-list",
    "GET",
    "/agent/threads/:uuid/files",
    "none",
    null
  ),
} as const;

const JSON_BODY_LIMITS = {
  "agent.createThread": 4 << 10,
  "agent.startTurn": 1 << 20,
  "system.writeLog": 65_536,
  "settings.putModelRoute": 8 << 10,
} as const;

export function createDesktopBridge(
  deps: DesktopBridgeDependencies
): DesktopBridge {
  return {
    version: DESKTOP_BRIDGE_VERSION,
    runtime: { ...deps.runtime },
    capabilities: buildCapabilities,
    auth: {
      status: () => execute<AuthStatus>(deps, ROUTES.authStatus),
      userInfo: () => execute<AuthUserInfo>(deps, ROUTES.authUserInfo),
      beginLogin: () => deps.beginLogin(),
      loginStatus: () => deps.loginStatus(),
      submitLoginPassword: (input) => deps.submitLoginPassword(input),
      cancelLogin: () => deps.cancelLogin(),
      logout: () => execute<AuthLogout>(deps, ROUTES.authLogout),
    },
    history: {
      listThreads: async (options) => {
        const path = buildListThreadsPath(options);
        return execute<LocalThreadList>(deps, ROUTES.historyListThreads, path);
      },
      listMessages: async (options) => {
        const path = buildListMessagesPath(options);
        return execute<LocalMessageList>(deps, ROUTES.historyListMessages, path);
      },
    },
    agent: {
      listSkills: () =>
        execute<AgentSkillCatalog>(deps, ROUTES.agentListSkills),
      createThread: async (input) => {
        const request = buildAgentCreateThreadRequest(input);
        const path = ROUTES.agentCreateThread.path.replace(
          ":uuid",
          encodeURIComponent(request.threadUUID)
        );
        const result = await execute<unknown>(
          deps,
          ROUTES.agentCreateThread,
          path,
          request.body
        );
        return validateAgentCreateThreadResult(result, request);
      },
      uploadThreadFile: async (threadUUID, file) => {
        const uuid = validateCanonicalV4UUID(
          threadUUID,
          "agent.uploadThreadFile threadUUID"
        );
        if (!(file instanceof File)) {
          throw new TypeError("agent.uploadThreadFile file must be a File");
        }
        const path = ROUTES.agentUploadThreadFile.path.replace(
          ":uuid",
          encodeURIComponent(uuid)
        );
        const formData = new FormData();
        formData.append("file", file, file.name);
        const result = await executeMultipart<unknown>(
          deps,
          ROUTES.agentUploadThreadFile,
          path,
          formData
        );
        return validateAgentThreadFileUploadResult(result);
      },
      // The attachments already on a thread. Uploads persist, but until this
      // existed nothing could read them back, so reopening a thread showed no
      // sources even though the files were on disk and still usable as model
      // context.
      listThreadFiles: async (threadUUID) => {
        const uuid = validateCanonicalV4UUID(
          threadUUID,
          "agent.listThreadFiles threadUUID"
        );
        const path = ROUTES.agentListThreadFiles.path.replace(
          ":uuid",
          encodeURIComponent(uuid)
        );
        return execute<AgentThreadFileList>(deps, ROUTES.agentListThreadFiles, path);
      },
      listRecoverableTurns: async () => {
        const result = await execute<unknown>(
          deps,
          ROUTES.agentListRecoverableTurns
        );
        return validateAgentRecoverableTurnListResult(result);
      },
      startTurn: (input, callback) => {
        const request = buildAgentSidecarTurnRequest(input, callback);
        const body = JSON.stringify(request);
        if (
          new TextEncoder().encode(body).byteLength >
          JSON_BODY_LIMITS["agent.startTurn"]
        ) {
          throw new RangeError(
            `agent.startTurn request body exceeds ${JSON_BODY_LIMITS["agent.startTurn"]} bytes`
          );
        }
        return deps.startAgentTurn(request, callback);
      },
      resumeTurn: (turnUUID, callback) => {
        if (typeof callback !== "function") {
          throw new TypeError("agent.resumeTurn callback must be a function");
        }
        return deps.resumeAgentTurn(
          validateCanonicalV4UUID(turnUUID, "agent.resumeTurn turnUUID"),
          callback
        );
      },
      cancelTurn: (turnID) => deps.cancelAgentTurn(validateTurnID(turnID)),
    },
    system: {
      health: () => execute<SidecarHealth>(deps, ROUTES.systemHealth),
      diagnostics: () =>
        execute<SidecarDiagnostics>(deps, ROUTES.systemDiagnostics),
      serverVersion: () =>
        execute<ServerVersion>(deps, ROUTES.systemServerVersion),
      triggerSync: async (options) => {
        const path = buildTriggerSyncPath(options);
        return execute<null>(deps, ROUTES.systemTriggerSync, path);
      },
      writeLog: async (entry) => {
        validateRendererLogInput(entry);
        return execute<null>(deps, ROUTES.systemWriteLog, undefined, entry);
      },
      revealDataDir: () => deps.revealDataDir(),
    },
    settings: {
      getModelRoute: async () => {
        const result = await execute<unknown>(
          deps,
          ROUTES.settingsGetModelRoute
        );
        return validateModelRouteSettingsResult(result);
      },
      putModelRoute: async (input) => {
        const body = buildModelRouteSettingsPut(input);
        const result = await execute<unknown>(
          deps,
          ROUTES.settingsPutModelRoute,
          undefined,
          body
        );
        return validateModelRouteSettingsResult(result);
      },
    },
  };
}

function buildCapabilities(): DesktopBridgeCapabilities {
  return {
    bridgeVersion: DESKTOP_BRIDGE_VERSION,
    compatibility: {
      legacyGlobal: "workmaxLocal",
      legacyFetch: true,
    },
    namespaces: {
      auth: {
        supported: true,
        methods: [
          "status",
          "userInfo",
          "beginLogin",
          "loginStatus",
          "submitLoginPassword",
          "cancelLogin",
          "logout",
        ],
      },
      history: {
        supported: true,
        methods: ["listThreads", "listMessages"],
      },
      system: {
        supported: true,
        methods: [
          "health",
          "diagnostics",
          "serverVersion",
          "triggerSync",
          "writeLog",
          "revealDataDir",
        ],
        deferred: ["networkState"],
      },
      agent: {
        supported: true,
        methods: [
          "listSkills",
          "createThread",
          "uploadThreadFile",
          "listRecoverableTurns",
          "startTurn",
          "resumeTurn",
          "cancelTurn",
        ],
        deferred: ["artifact"],
      },
      settings: {
        supported: true,
        methods: ["getModelRoute", "putModelRoute"],
      },
      artifact: {
        supported: false,
        methods: [],
        reason: "artifact-routes-unavailable",
      },
    },
  };
}

function buildModelRouteSettingsPut(input: ModelRouteSettingsPut): Record<string, unknown> {
  assertPlainObject(input, "settings.putModelRoute input");
  assertAllowedKeys(
    input,
    ["preferred_route", "local"],
    "settings.putModelRoute input"
  );
  const route = input.preferred_route;
  if (route !== "local" && route !== "official") {
    throw new TypeError(
      'settings.putModelRoute preferred_route must be "local" or "official"'
    );
  }
  const body: Record<string, unknown> = { preferred_route: route };
  if (input.local === undefined) {
    return body;
  }
  assertPlainObject(input.local, "settings.putModelRoute local");
  assertAllowedKeys(
    input.local,
    ["protocol", "base_url", "model_id", "api_key", "clear_api_key"],
    "settings.putModelRoute local"
  );
  const protocol = input.local.protocol;
  if (
    protocol !== "openai_compatible" &&
    protocol !== "anthropic_compatible"
  ) {
    throw new TypeError("settings.putModelRoute local.protocol is malformed");
  }
  const baseURL = input.local.base_url;
  if (
    typeof baseURL !== "string" ||
    baseURL === "" ||
    baseURL.trim() !== baseURL ||
    new TextEncoder().encode(baseURL).byteLength > 512
  ) {
    throw new TypeError("settings.putModelRoute local.base_url is malformed");
  }
  try {
    const parsed = new URL(baseURL);
    if (parsed.protocol !== "https:" && parsed.protocol !== "http:") {
      throw new TypeError("settings.putModelRoute local.base_url scheme invalid");
    }
    if (parsed.username || parsed.password || parsed.hash) {
      throw new TypeError(
        "settings.putModelRoute local.base_url must not include credentials or fragment"
      );
    }
    if (parsed.protocol === "http:") {
      const host = parsed.hostname.toLowerCase();
      if (host !== "127.0.0.1" && host !== "localhost" && host !== "::1") {
        throw new TypeError(
          "settings.putModelRoute http base_url is only allowed for loopback"
        );
      }
    }
  } catch (error) {
    if (error instanceof TypeError) {
      throw error;
    }
    throw new TypeError("settings.putModelRoute local.base_url is not a URL");
  }
  const modelID = input.local.model_id;
  if (
    typeof modelID !== "string" ||
    modelID === "" ||
    modelID.trim() !== modelID ||
    modelID.length > 128 ||
    !/^[A-Za-z0-9._\-:/]+$/u.test(modelID)
  ) {
    throw new TypeError("settings.putModelRoute local.model_id is malformed");
  }
  const localBody: Record<string, unknown> = {
    protocol,
    base_url: baseURL,
    model_id: modelID,
  };
  if (input.local.clear_api_key === true) {
    localBody.clear_api_key = true;
  } else if (input.local.clear_api_key !== undefined) {
    throw new TypeError(
      "settings.putModelRoute local.clear_api_key must be true when set"
    );
  }
  if (input.local.api_key !== undefined) {
    if (typeof input.local.api_key !== "string") {
      throw new TypeError("settings.putModelRoute local.api_key must be a string");
    }
    if (input.local.api_key === "") {
      throw new TypeError(
        "settings.putModelRoute local.api_key must not be empty when provided"
      );
    }
    if (
      new TextEncoder().encode(input.local.api_key).byteLength > 4096 ||
      /[\u0000-\u001f\u007f]/u.test(input.local.api_key)
    ) {
      throw new TypeError("settings.putModelRoute local.api_key is malformed");
    }
    localBody.api_key = input.local.api_key;
  }
  body.local = localBody;
  return body;
}

function validateModelRouteSettingsResult(
  result: DesktopBridgeResult<unknown>
): DesktopBridgeResult<ModelRouteSettings> {
  if (!result.ok) {
    return result;
  }
  const data = result.data;
  if (data === null || typeof data !== "object" || Array.isArray(data)) {
    throw new TypeError("settings model-route response is malformed");
  }
  const record = data as Record<string, unknown>;
  if (Object.prototype.hasOwnProperty.call(record, "api_key")) {
    throw new TypeError("settings model-route response must not include api_key");
  }
  assertExactKeys(
    record,
    ["preferred_route", "local", "updated_at"],
    "settings model-route response"
  );
  const route = record.preferred_route;
  if (route !== "local" && route !== "official") {
    throw new TypeError("settings model-route preferred_route is malformed");
  }
  if (
    typeof record.updated_at !== "string" ||
    record.updated_at === "" ||
    Number.isNaN(Date.parse(record.updated_at))
  ) {
    throw new TypeError("settings model-route updated_at is malformed");
  }
  const local = record.local;
  if (local === null || typeof local !== "object" || Array.isArray(local)) {
    throw new TypeError("settings model-route local is malformed");
  }
  const localRecord = local as Record<string, unknown>;
  if (Object.prototype.hasOwnProperty.call(localRecord, "api_key")) {
    throw new TypeError("settings model-route local must not include api_key");
  }
  assertExactKeys(
    localRecord,
    ["protocol", "base_url", "model_id", "api_key_configured"],
    "settings model-route local"
  );
  for (const key of ["protocol", "base_url", "model_id"] as const) {
    if (typeof localRecord[key] !== "string") {
      throw new TypeError(`settings model-route local.${key} is malformed`);
    }
  }
  if (typeof localRecord.api_key_configured !== "boolean") {
    throw new TypeError(
      "settings model-route local.api_key_configured is malformed"
    );
  }
  return {
    ...result,
    data: {
      preferred_route: route,
      local: {
        protocol: localRecord.protocol as string,
        base_url: localRecord.base_url as string,
        model_id: localRecord.model_id as string,
        api_key_configured: localRecord.api_key_configured as boolean,
      },
      updated_at: record.updated_at as string,
    },
  };
}

function buildAgentCreateThreadRequest(input: AgentCreateThreadInput): {
  threadUUID: string;
  body: { name: string; agent_mode: string };
} {
  assertPlainObject(input, "agent.createThread input");
  assertExactKeys(
    input,
    ["threadUUID", "name", "agentMode"],
    "agent.createThread input"
  );
  const threadUUID = validateCanonicalV4ThreadUUID(input.threadUUID);
  const name = validateAgentThreadName(input.name);
  const agentMode = validateAgentMode(
    input.agentMode,
    "agent.createThread agentMode",
    64
  );
  return {
    threadUUID,
    body: { name, agent_mode: agentMode },
  };
}

function validateAgentCreateThreadResult(
  result: DesktopBridgeResult<unknown>,
  request: {
    threadUUID: string;
    body: { name: string; agent_mode: string };
  }
): DesktopBridgeResult<AgentCreateThreadResult> {
  if (!result.ok) {
    return result;
  }
  try {
    return {
      ...result,
      data: parseAgentCreateThreadData(result.data, result.status, request),
    };
  } catch {
    throw new TypeError("agent.createThread response is malformed");
  }
}

function parseAgentCreateThreadData(
  value: unknown,
  status: number,
  request: {
    threadUUID: string;
    body: { name: string; agent_mode: string };
  }
): AgentCreateThreadResult {
  assertPlainObject(value, "agent.createThread response");
  if (status === 202) {
    assertExactKeys(
      value,
      ["state", "thread_uuid"],
      "agent.createThread pending response"
    );
    if (
      value.state !== "pending_local_sync" ||
      validateCanonicalV4ThreadUUID(value.thread_uuid) !== request.threadUUID
    ) {
      throw new TypeError("agent.createThread pending response is invalid");
    }
    return {
      state: "pending_local_sync",
      thread_uuid: request.threadUUID,
    };
  }

  if (status !== 200 && status !== 201) {
    throw new TypeError("agent.createThread ready response status is invalid");
  }
  assertExactKeys(
    value,
    ["state", "created", "thread"],
    "agent.createThread ready response"
  );
  if (
    value.state !== "ready" ||
    typeof value.created !== "boolean" ||
    status !== (value.created ? 201 : 200)
  ) {
    throw new TypeError("agent.createThread ready response is invalid");
  }

  assertPlainObject(value.thread, "agent.createThread response thread");
  assertExactKeys(
    value.thread,
    [
      "uuid",
      "name",
      "agent_mode",
      "message_count",
      "updated_at",
      "cloud_sync_state",
    ],
    "agent.createThread response thread"
  );
  const uuid = validateCanonicalV4ThreadUUID(value.thread.uuid);
  const name = validateAgentThreadName(value.thread.name);
  const agentMode = validateAgentMode(
    value.thread.agent_mode,
    "agent.createThread response agent_mode",
    64
  );
  if (
    uuid !== request.threadUUID ||
    (value.created && name !== request.body.name) ||
    (value.created && agentMode !== request.body.agent_mode) ||
    !Number.isSafeInteger(value.thread.message_count) ||
    (value.thread.message_count as number) < 0 ||
    typeof value.thread.updated_at !== "string" ||
    value.thread.updated_at.trim() === "" ||
    !Number.isFinite(Date.parse(value.thread.updated_at)) ||
    (value.thread.cloud_sync_state !== "synced" &&
      value.thread.cloud_sync_state !== "paused")
  ) {
    throw new TypeError("agent.createThread response thread is invalid");
  }
  return {
    state: "ready",
    created: value.created,
    thread: {
      uuid,
      name,
      agent_mode: agentMode,
      message_count: value.thread.message_count as number,
      updated_at: value.thread.updated_at,
      cloud_sync_state: value.thread.cloud_sync_state,
    },
  };
}

function validateAgentRecoverableTurnListResult(
  result: DesktopBridgeResult<unknown>
): DesktopBridgeResult<AgentRecoverableTurnList> {
  if (!result.ok) {
    return result;
  }
  try {
    if (result.status !== 200) {
      throw new TypeError("unexpected status");
    }
    assertPlainObject(result.data, "agent.listRecoverableTurns response");
    assertExactKeys(
      result.data,
      ["items", "count"],
      "agent.listRecoverableTurns response"
    );
    if (
      !Array.isArray(result.data.items) ||
      !Number.isSafeInteger(result.data.count) ||
      (result.data.count as number) !== result.data.items.length
    ) {
      throw new TypeError("invalid recoverable turn list");
    }
    const seenTurnUUIDs = new Set<string>();
    const items = result.data.items.map((item) => {
      assertPlainObject(item, "agent.listRecoverableTurns item");
      assertExactKeys(
        item,
        [
          "turn_uuid",
          "thread_uuid",
          "user_text",
          "chat_mode",
          "state",
          "last_error_kind",
          "updated_at",
        ],
        "agent.listRecoverableTurns item"
      );
      const turnUUID = validateCanonicalV4UUID(
        item.turn_uuid,
        "agent.listRecoverableTurns turn_uuid"
      );
      const threadUUID = validateThreadUUID(item.thread_uuid);
      const userText = validateAgentUserText(item.user_text);
      const chatMode = validateAgentMode(
        item.chat_mode,
        "agent.listRecoverableTurns chat_mode",
        100
      );
      if (
        item.state !== "interrupted" ||
        typeof item.last_error_kind !== "string" ||
        !hasWellFormedUTF16(item.last_error_kind) ||
        /[\u0000-\u001f\u007f]/u.test(item.last_error_kind) ||
        new TextEncoder().encode(item.last_error_kind).byteLength > 128 ||
        typeof item.updated_at !== "string" ||
        item.updated_at.trim() === "" ||
        !Number.isFinite(Date.parse(item.updated_at)) ||
        seenTurnUUIDs.has(turnUUID)
      ) {
        throw new TypeError("invalid recoverable turn item");
      }
      seenTurnUUIDs.add(turnUUID);
      return {
        turn_uuid: turnUUID,
        thread_uuid: threadUUID,
        user_text: userText,
        chat_mode: chatMode,
        state: "interrupted" as const,
        last_error_kind: item.last_error_kind,
        updated_at: item.updated_at,
      };
    });
    return {
      ...result,
      data: { items, count: items.length },
    };
  } catch {
    throw new TypeError("agent.listRecoverableTurns response is malformed");
  }
}

function validateCanonicalV4UUID(value: unknown, label: string): string {
  if (
    typeof value !== "string" ||
    !/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/u.test(
      value
    )
  ) {
    throw new TypeError(`${label} must be a canonical v4 UUID`);
  }
  return value;
}

function validateCanonicalV4ThreadUUID(value: unknown): string {
  return validateCanonicalV4UUID(
    value,
    "agent.createThread threadUUID"
  );
}

function validateAgentThreadName(value: unknown): string {
  if (
    typeof value !== "string" ||
    value === "" ||
    value.trim() !== value ||
    !hasWellFormedUTF16(value) ||
    /[\u0000-\u001f\u007f]/u.test(value)
  ) {
    throw new TypeError("agent.createThread name is malformed");
  }
  if (new TextEncoder().encode(value).byteLength > 200) {
    throw new RangeError("agent.createThread name exceeds 200 bytes");
  }
  return value;
}

function buildAgentSidecarTurnRequest(
  input: AgentTurnInput,
  callback: AgentTurnEventCallback
): AgentSidecarTurnStartRequest {
  assertPlainObject(input, "agent.startTurn input");
  assertAllowedKeys(
    input,
    ["threadUUID", "userText", "chatMode", "fileIDs"],
    "agent.startTurn input"
  );
  if (typeof callback !== "function") {
    throw new TypeError("agent.startTurn callback must be a function");
  }
  const threadUUID = validateThreadUUID(input.threadUUID);
  const userText = validateAgentUserText(input.userText);
  const chatMode = validateAgentChatMode(input.chatMode);
  const fileIDs = validateAgentTurnFileIDs(input.fileIDs);
  const payload: { stream: true; files?: number[] } = { stream: true };
  if (fileIDs.length > 0) {
    payload.files = fileIDs;
  }
  return {
    thread_uuid: threadUUID,
    user_text: userText,
    chat_mode: chatMode,
    payload,
  };
}

// validateAgentTurnFileIDs normalizes the optional fileIDs input on
// agent.startTurn: undefined → empty, otherwise a deduped array of positive
// safe integers with a small cap (aligned with the sidecar payload limit).
function validateAgentTurnFileIDs(value: unknown): number[] {
  if (value === undefined) {
    return [];
  }
  if (!Array.isArray(value)) {
    throw new TypeError("agent.startTurn fileIDs must be an array");
  }
  if (value.length > 10) {
    throw new RangeError("agent.startTurn fileIDs exceeds 10 entries");
  }
  const seen = new Set<number>();
  const out: number[] = [];
  for (const id of value) {
    if (typeof id !== "number" || !Number.isSafeInteger(id) || id <= 0) {
      throw new TypeError("agent.startTurn fileIDs must be positive integers");
    }
    if (seen.has(id)) {
      throw new TypeError("agent.startTurn fileIDs must be unique");
    }
    seen.add(id);
    out.push(id);
  }
  return out;
}

function validateAgentUserText(value: unknown): string {
  if (typeof value !== "string" || value.trim() === "") {
    throw new TypeError("agent.startTurn userText must be a non-empty string");
  }
  if (!hasWellFormedUTF16(value)) {
    throw new TypeError("agent.startTurn userText contains malformed UTF-16");
  }
  if (/\u0000/u.test(value)) {
    throw new TypeError("agent.startTurn userText contains a null character");
  }
  const maxBytes = 256 * 1024;
  if (new TextEncoder().encode(value).byteLength > maxBytes) {
    throw new RangeError(
      `agent.startTurn userText exceeds ${maxBytes} bytes`
    );
  }
  return value;
}

function validateAgentChatMode(value: unknown): string {
  return validateAgentMode(value, "agent.startTurn chatMode", 100);
}

function validateAgentMode(value: unknown, label: string, maxLength: number): string {
  if (
    typeof value !== "string" ||
    value === "" ||
    value.trim() !== value ||
    value.length > maxLength ||
    !/^[A-Za-z][A-Za-z0-9_-]*$/u.test(value)
  ) {
    throw new TypeError(`${label} is malformed`);
  }
  return value;
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
    if (code >= 0xdc00 && code <= 0xdfff) {
      return false;
    }
  }
  return true;
}

function validateTurnID(value: unknown): string {
  return validateCanonicalV4UUID(value, "agent.cancelTurn turnID");
}

async function execute<T>(
  deps: DesktopBridgeDependencies,
  route: TypedRouteSpec,
  pathOverride?: string,
  jsonBody?: unknown
): Promise<DesktopBridgeResult<T>> {
  const headers = new Headers({ Accept: "application/json" });
  const init: RequestInit = {
    method: route.method,
    headers,
  };
  if (route.body === "json") {
    const body = JSON.stringify(jsonBody);
    const limit =
      JSON_BODY_LIMITS[route.bridgeMethod as keyof typeof JSON_BODY_LIMITS];
    if (limit !== undefined && new TextEncoder().encode(body).byteLength > limit) {
      throw new RangeError(`${route.bridgeMethod} request body exceeds ${limit} bytes`);
    }
    headers.set("Content-Type", route.contentType!);
    init.body = body;
  } else if (jsonBody !== undefined) {
    throw new TypeError(`${route.bridgeMethod} does not accept a request body`);
  }

  const response = await deps.request(pathOverride ?? route.path, init);
  const rawBody = await response.text();
  const payload = rawBody === "" ? null : JSON.parse(rawBody);
  const metadata = {
    status: response.status,
    statusText: response.statusText,
    headers: { ...response.headers },
  };
  if (response.ok) {
    return { ok: true, ...metadata, data: payload as T | null };
  }
  return { ok: false, ...metadata, error: payload };
}

// executeMultipart sends a multipart/form-data body (file uploads). Unlike
// execute, it does NOT set Content-Type — fetch derives the boundary from the
// FormData, and setting it manually would corrupt the sidecar's parse.
async function executeMultipart<T>(
  deps: DesktopBridgeDependencies,
  route: TypedRouteSpec,
  path: string,
  formData: FormData
): Promise<DesktopBridgeResult<T>> {
  const init: RequestInit = {
    method: route.method,
    headers: new Headers({ Accept: "application/json" }),
    body: formData,
  };
  const response = await deps.request(path, init);
  const rawBody = await response.text();
  const payload = rawBody === "" ? null : JSON.parse(rawBody);
  const metadata = {
    status: response.status,
    statusText: response.statusText,
    headers: { ...response.headers },
  };
  if (response.ok) {
    return { ok: true, ...metadata, data: payload as T | null };
  }
  return { ok: false, ...metadata, error: payload };
}

// validateAgentThreadFileUploadResult narrows an upload response into the typed
// result, or re-throws as a malformed-response TypeError (matching the
// createThread validator's strictness).
function validateAgentThreadFileUploadResult(
  result: DesktopBridgeResult<unknown>
): DesktopBridgeResult<AgentThreadFileUploadResult> {
  if (!result.ok) {
    return result;
  }
  try {
    return {
      ...result,
      data: parseAgentThreadFileUploadData(result.data),
    };
  } catch {
    throw new TypeError("agent.uploadThreadFile response is malformed");
  }
}

function parseAgentThreadFileUploadData(
  value: unknown
): AgentThreadFileUploadResult {
  assertPlainObject(value, "agent.uploadThreadFile response");
  assertExactKeys(
    value,
    ["file_id", "file_name", "mime_type", "file_type", "file_size"],
    "agent.uploadThreadFile response"
  );
  if (
    typeof value.file_id !== "number" ||
    !Number.isSafeInteger(value.file_id) ||
    value.file_id <= 0
  ) {
    throw new TypeError("agent.uploadThreadFile response file_id is invalid");
  }
  if (
    typeof value.file_name !== "string" ||
    typeof value.mime_type !== "string" ||
    typeof value.file_type !== "string"
  ) {
    throw new TypeError(
      "agent.uploadThreadFile response string fields are invalid"
    );
  }
  if (
    typeof value.file_size !== "number" ||
    !Number.isSafeInteger(value.file_size) ||
    value.file_size < 0
  ) {
    throw new TypeError("agent.uploadThreadFile response file_size is invalid");
  }
  return value as unknown as AgentThreadFileUploadResult;
}

function buildListThreadsPath(options?: ListThreadsOptions): string {
  if (options !== undefined) {
    assertPlainObject(options, "history.listThreads options");
    assertAllowedKeys(
      options,
      ["limit", "includePaused"],
      "history.listThreads options"
    );
  }
  const params = new URLSearchParams();
  if (options?.limit !== undefined) {
    params.set(
      "limit",
      String(validateLimit(options.limit, 500, "history.listThreads limit"))
    );
  }
  if (options?.includePaused !== undefined) {
    if (typeof options.includePaused !== "boolean") {
      throw new TypeError("history.listThreads includePaused must be boolean");
    }
    params.set("include_paused", String(options.includePaused));
  }
  return appendQuery(ROUTES.historyListThreads.path, params);
}

function buildListMessagesPath(options: ListMessagesOptions): string {
  assertPlainObject(options, "history.listMessages options");
  assertAllowedKeys(options, ["threadUUID", "limit"], "history.listMessages options");
  const threadUUID = validateThreadUUID(options.threadUUID);
  const params = new URLSearchParams();
  if (options.limit !== undefined) {
    params.set(
      "limit",
      String(validateLimit(options.limit, 1000, "history.listMessages limit"))
    );
  }
  return appendQuery(
    ROUTES.historyListMessages.path.replace(":uuid", encodeURIComponent(threadUUID)),
    params
  );
}

function buildTriggerSyncPath(options?: TriggerSyncOptions): string {
  if (options !== undefined) {
    assertPlainObject(options, "system.triggerSync options");
    assertAllowedKeys(options, ["threadUUID"], "system.triggerSync options");
  }
  const params = new URLSearchParams();
  if (options?.threadUUID !== undefined) {
    params.set("thread", validateThreadUUID(options.threadUUID));
  }
  return appendQuery(ROUTES.systemTriggerSync.path, params);
}

function appendQuery(path: string, params: URLSearchParams): string {
  const query = params.toString();
  return query === "" ? path : `${path}?${query}`;
}

function validateLimit(value: unknown, max: number, label: string): number {
  if (
    !Number.isSafeInteger(value) ||
    (value as number) < 1 ||
    (value as number) > max
  ) {
    throw new RangeError(`${label} must be an integer between 1 and ${max}`);
  }
  return value as number;
}

function validateThreadUUID(value: unknown): string {
  if (typeof value !== "string" || value === "") {
    throw new TypeError("threadUUID must be a non-empty string");
  }
  if (
    value.trim() !== value ||
    /[\u0000-\u001f\u007f/?#\\]/u.test(value) ||
    new TextEncoder().encode(value).byteLength > 200
  ) {
    throw new TypeError("threadUUID is malformed");
  }
  return value;
}

function validateRendererLogInput(value: RendererLogInput): void {
  assertPlainObject(value, "system.writeLog entry");
  assertAllowedKeys(value, ["level", "message", "context"], "system.writeLog entry");
  if (value.level !== "error" && value.level !== "warn" && value.level !== "info") {
    throw new TypeError("system.writeLog level must be error, warn, or info");
  }
  if (typeof value.message !== "string" || value.message === "") {
    throw new TypeError("system.writeLog message must be a non-empty string");
  }
  if (value.context !== undefined) {
    assertPlainObject(value.context, "system.writeLog context");
  }
}

function assertPlainObject(
  value: unknown,
  label: string
): asserts value is Record<string, unknown> {
  if (value === null || typeof value !== "object" || Array.isArray(value)) {
    throw new TypeError(`${label} must be an object`);
  }
}

function assertAllowedKeys(
  value: Record<string, unknown>,
  allowed: string[],
  label: string
): void {
  const allowedSet = new Set(allowed);
  for (const key of Object.keys(value)) {
    if (!allowedSet.has(key)) {
      throw new TypeError(`${label} contains unsupported field ${key}`);
    }
  }
}

function assertExactKeys(
  value: Record<string, unknown>,
  required: string[],
  label: string
): void {
  assertAllowedKeys(value, required, label);
  const actual = Object.keys(value);
  if (
    actual.length !== required.length ||
    required.some((key) => !Object.prototype.hasOwnProperty.call(value, key))
  ) {
    throw new TypeError(`${label} must contain exactly ${required.join(", ")}`);
  }
}
