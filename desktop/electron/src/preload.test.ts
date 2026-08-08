import assert from "node:assert/strict";
import Module from "node:module";
import { afterEach, describe, it } from "node:test";

import type { AgentTurnEvent, DesktopBridge } from "./desktop-bridge";

type ExposedBridge = {
  port: number;
  sidecarVersion: string;
  appVersion: string;
  platform: NodeJS.Platform;
  fetch: (path: string, init?: RequestInit) => Promise<BridgeResponse>;
  revealDataDir: () => Promise<{ ok: boolean; path?: string; error?: string }>;
};

type BridgeResponse = {
  ok: boolean;
  status: number;
  statusText: string;
  url: string;
  headers: Record<string, string>;
  body: { getReader: () => ReadableStreamDefaultReader<Uint8Array> } | null;
  text: () => Promise<string>;
  json: () => Promise<unknown>;
};

type LoadedPreload = Record<string, unknown> & {
  workmaxLocal: ExposedBridge;
  desktopBridge: DesktopBridge;
};

const originalEnv = { ...process.env };
const originalFetch = globalThis.fetch;
const originalConsoleError = console.error;

afterEach(() => {
  process.env = { ...originalEnv };
  globalThis.fetch = originalFetch;
  console.error = originalConsoleError;
  deletePreloadFromRequireCache();
});

describe("preload bridge", () => {
  it("preserves workmaxLocal and exposes only the versioned typed facade", async () => {
    const ipcInvocations: Array<{ channel: string; args: unknown[] }> = [];
    const exposed = loadPreloadWithElectronMock(
      {
        WORKMAX_LOCAL_PORT: "49152",
        WORKMAX_LOCAL_TOKEN: "local-token",
        WORKMAX_SIDECAR_VERSION: "0.1.0-p1-ea",
        WORKMAX_APP_VERSION: "0.1.0-p1-ea",
      },
      ipcInvocations
    );

    assert.deepEqual(Object.keys(exposed).sort(), ["desktopBridge", "workmaxLocal"]);
    const bridge = exposed.workmaxLocal;
    assert.deepEqual(Object.keys(bridge).sort(), [
      "appVersion",
      "fetch",
      "platform",
      "port",
      "revealDataDir",
      "sidecarVersion",
    ]);
    assert.equal(bridge.port, 49152);
    assert.equal(bridge.sidecarVersion, "0.1.0-p1-ea");
    assert.equal(bridge.appVersion, "0.1.0-p1-ea");
    assert.equal(bridge.platform, process.platform);
    assert.equal(Object.prototype.hasOwnProperty.call(bridge, "token"), false);

    const typed = exposed.desktopBridge;
    assert.equal(typed.version, "1.0.0-alpha.7");
    assert.deepEqual(Object.keys(typed).sort(), [
      "agent",
      "auth",
      "capabilities",
      "history",
      "runtime",
      "settings",
      "system",
      "version",
    ]);
    assert.deepEqual(typed.runtime, {
      appVersion: "0.1.0-p1-ea",
      sidecarVersion: "0.1.0-p1-ea",
      platform: process.platform,
    });
    assert.equal(Object.prototype.hasOwnProperty.call(typed.runtime, "port"), false);
    assert.equal(Object.prototype.hasOwnProperty.call(typed, "fetch"), false);

    const revealResult = await bridge.revealDataDir();
    assert.deepEqual(revealResult, { ok: true });
    const loginResult = await typed.auth.beginLogin();
    assert.deepEqual(loginResult, { state: "awaiting_password" });
    const loginStatus = await typed.auth.loginStatus();
    assert.deepEqual(loginStatus, { state: "submitting" });
    const passwordResult = await typed.auth.submitLoginPassword({
      email: "user@example.test",
      password: "local test phrase",
    });
    assert.deepEqual(passwordResult, { state: "authenticated" });
    const cancelResult = await typed.auth.cancelLogin();
    assert.deepEqual(cancelResult, { state: "idle", error: "canceled" });
    const typedRevealResult = await typed.system.revealDataDir();
    assert.deepEqual(typedRevealResult, { ok: true });
    assert.deepEqual(ipcInvocations, [
      { channel: "reveal-data-dir", args: [] },
      { channel: "auth-begin-login-transaction", args: [] },
      { channel: "auth-login-transaction-status", args: [] },
      {
        channel: "auth-submit-login-password",
        args: [
          { email: "user@example.test", password: "local test phrase" },
        ],
      },
      { channel: "auth-cancel-login-transaction", args: [] },
      { channel: "reveal-data-dir", args: [] },
    ]);
    assert.deepEqual(Object.keys(typed.auth).sort(), [
      "beginLogin",
      "cancelLogin",
      "loginStatus",
      "logout",
      "status",
      "submitLoginPassword",
      "userInfo",
    ]);
    assert.deepEqual(Object.keys(typed.agent).sort(), [
      "cancelTurn",
      "createThread",
      "listRecoverableTurns",
      "listSkills",
      "resumeTurn",
      "startTurn",
      "uploadThreadFile",
    ]);
  });

  it("discovers the typed Agent preview without claiming Artifact", () => {
    const typed = loadPreloadWithElectronMock({
      WORKMAX_LOCAL_PORT: "49152",
      WORKMAX_LOCAL_TOKEN: "local-token",
    }).desktopBridge;

    const first = typed.capabilities();
    assert.equal(first.bridgeVersion, "1.0.0-alpha.7");
    assert.deepEqual(first.compatibility, {
      legacyGlobal: "workmaxLocal",
      legacyFetch: true,
    });
    assert.equal(first.namespaces.auth.supported, true);
    assert.deepEqual(first.namespaces.auth.methods, [
      "status",
      "userInfo",
      "beginLogin",
      "loginStatus",
      "submitLoginPassword",
      "cancelLogin",
      "logout",
    ]);
    assert.deepEqual(first.namespaces.history.methods, ["listThreads", "listMessages"]);
    assert.deepEqual(first.namespaces.system.deferred, ["networkState"]);
    assert.deepEqual(first.namespaces.agent, {
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
    });
    assert.deepEqual(first.namespaces.settings, {
      supported: true,
      methods: ["getModelRoute", "putModelRoute"],
    });
    assert.deepEqual(first.namespaces.artifact, {
      supported: false,
      methods: [],
      reason: "artifact-routes-unavailable",
    });

    first.namespaces.auth.methods.push("callerMutation");
    assert.equal(
      typed.capabilities().namespaces.auth.methods.includes("callerMutation"),
      false
    );
  });

  it("pins typed methods to their declared method, path, body, and content type", async () => {
    const typed = loadPreloadWithElectronMock({
      WORKMAX_LOCAL_PORT: "49152",
      WORKMAX_LOCAL_TOKEN: "local-token",
    }).desktopBridge;
    const calls: Array<{ url: string; init?: RequestInit }> = [];
    globalThis.fetch = async (input: string | URL | Request, init?: RequestInit) => {
      calls.push({ url: String(input), init });
      if (String(input).endsWith("/system/trigger-sync?thread=thread-123") ||
          String(input).endsWith("/system/log")) {
        return new Response(null, { status: 204 });
      }
      if (
        String(input).endsWith(
          "/agent/threads/123e4567-e89b-42d3-a456-426614174000"
        )
      ) {
        return new Response(
          JSON.stringify({
            state: "ready",
            created: true,
            thread: {
              uuid: "123e4567-e89b-42d3-a456-426614174000",
              name: "Quarterly review",
              agent_mode: "ppt",
              message_count: 0,
              updated_at: "2026-08-06T00:00:00Z",
              cloud_sync_state: "synced",
            },
          }),
          {
            status: 201,
            headers: { "Content-Type": "application/json", "X-Test": "typed" },
          }
        );
      }
      if (String(input).endsWith("/agent/turns/recoverable")) {
        return new Response(JSON.stringify({ items: [], count: 0 }), {
          status: 200,
          headers: { "Content-Type": "application/json", "X-Test": "typed" },
        });
      }
      if (String(input).endsWith("/settings/model-route")) {
        return new Response(
          JSON.stringify({
            preferred_route: "official",
            local: {
              protocol: "",
              base_url: "",
              model_id: "",
              api_key_configured: false,
            },
            updated_at: "2026-08-08T00:00:00.000Z",
          }),
          {
            status: 200,
            headers: { "Content-Type": "application/json", "X-Test": "typed" },
          }
        );
      }
      return new Response(JSON.stringify({ route: String(input) }), {
        status: 200,
        headers: { "Content-Type": "application/json", "X-Test": "typed" },
      });
    };

    await typed.auth.status();
    await typed.auth.userInfo();
    await typed.auth.logout();
    await typed.agent.listSkills();
    await typed.agent.createThread({
      threadUUID: "123e4567-e89b-42d3-a456-426614174000",
      name: "Quarterly review",
      agentMode: "ppt",
    });
    await typed.agent.listRecoverableTurns();
    await typed.history.listThreads({ limit: 25, includePaused: false });
    await typed.history.listMessages({ threadUUID: "thread-123", limit: 100 });
    await typed.system.health();
    await typed.system.diagnostics();
    await typed.system.serverVersion();
    const syncResult = await typed.system.triggerSync({ threadUUID: "thread-123" });
    const logResult = await typed.system.writeLog({
      level: "info",
      message: "typed bridge ready",
      context: { source: "test" },
    });
    await typed.settings.getModelRoute();
    await typed.settings.putModelRoute({ preferred_route: "official" });

    const observed = calls.map((call) => ({
      url: call.url,
      method: call.init?.method,
      body: call.init?.body,
      accept: new Headers(call.init?.headers).get("Accept"),
      contentType: new Headers(call.init?.headers).get("Content-Type"),
      localToken: new Headers(call.init?.headers).get("X-Local-Token"),
    }));
    assert.deepEqual(
      observed.map(({ url, method }) => ({ url, method })),
      [
        { url: "http://127.0.0.1:49152/auth/status", method: "GET" },
        { url: "http://127.0.0.1:49152/auth/userinfo", method: "GET" },
        { url: "http://127.0.0.1:49152/auth/logout", method: "POST" },
        {
          url: "http://127.0.0.1:49152/agent/skills/catalog",
          method: "GET",
        },
        {
          url: "http://127.0.0.1:49152/agent/threads/123e4567-e89b-42d3-a456-426614174000",
          method: "PUT",
        },
        {
          url: "http://127.0.0.1:49152/agent/turns/recoverable",
          method: "GET",
        },
        {
          url: "http://127.0.0.1:49152/agent/threads?limit=25&include_paused=false",
          method: "GET",
        },
        {
          url: "http://127.0.0.1:49152/agent/threads/thread-123/messages?limit=100",
          method: "GET",
        },
        { url: "http://127.0.0.1:49152/health", method: "GET" },
        { url: "http://127.0.0.1:49152/system/diagnostics", method: "GET" },
        { url: "http://127.0.0.1:49152/system/server-version", method: "GET" },
        {
          url: "http://127.0.0.1:49152/system/trigger-sync?thread=thread-123",
          method: "POST",
        },
        { url: "http://127.0.0.1:49152/system/log", method: "POST" },
        { url: "http://127.0.0.1:49152/settings/model-route", method: "GET" },
        { url: "http://127.0.0.1:49152/settings/model-route", method: "PUT" },
      ]
    );
    for (const item of observed) {
      assert.equal(item.accept, "application/json");
      assert.equal(item.localToken, "local-token");
    }
    const jsonBodyIndexes = new Set([4, 12, 14]);
    for (const [index, item] of observed.entries()) {
      if (jsonBodyIndexes.has(index)) continue;
      assert.equal(item.body, undefined);
      assert.equal(item.contentType, null);
    }
    assert.equal(observed[4].contentType, "application/json");
    assert.equal(
      observed[4].body,
      JSON.stringify({ name: "Quarterly review", agent_mode: "ppt" })
    );
    assert.equal(observed[12].contentType, "application/json");
    assert.equal(
      observed[12].body,
      JSON.stringify({ level: "info", message: "typed bridge ready", context: { source: "test" } })
    );
    assert.equal(observed[14].contentType, "application/json");
    assert.equal(
      observed[14].body,
      JSON.stringify({ preferred_route: "official" })
    );
    assert.deepEqual(syncResult, {
      ok: true,
      status: 204,
      statusText: "",
      headers: {},
      data: null,
    });
    assert.equal(logResult.ok, true);
  });

  it("rejects typed route-shaping and oversized inputs before fetch", async () => {
    const typed = loadPreloadWithElectronMock({
      WORKMAX_LOCAL_PORT: "49152",
      WORKMAX_LOCAL_TOKEN: "local-token",
    }).desktopBridge;
    let calls = 0;
    globalThis.fetch = async () => {
      calls += 1;
      return new Response("{}", { status: 200 });
    };

    await assert.rejects(
      () => typed.history.listThreads({ limit: 0 }),
      /integer between 1 and 500/
    );
    await assert.rejects(
      () => typed.history.listThreads({ includePaused: "false" as unknown as boolean }),
      /must be boolean/
    );
    await assert.rejects(
      () => typed.history.listThreads({ url: "https://evil.example" } as never),
      /unsupported field url/
    );
    for (const threadUUID of ["", " ../system/diagnostics", "a/b", "a?limit=1", "a#fragment", "a\n"] ) {
      await assert.rejects(
        () => typed.history.listMessages({ threadUUID }),
        /threadUUID/
      );
      await assert.rejects(
        () => typed.system.triggerSync({ threadUUID }),
        /threadUUID/
      );
    }
    await assert.rejects(
      () => typed.history.listMessages({ threadUUID: "ok", limit: 1001 }),
      /integer between 1 and 1000/
    );
    await assert.rejects(
      () => typed.system.writeLog({ level: "debug" as "info", message: "no" }),
      /level must be/
    );
    await assert.rejects(
      () => typed.system.writeLog({ level: "info", message: "" }),
      /non-empty string/
    );
    await assert.rejects(
      () => typed.system.writeLog({ level: "info", message: "x".repeat(70_000) }),
      /exceeds 65536 bytes/
    );
    const validCreate = {
      threadUUID: "123e4567-e89b-42d3-a456-426614174000",
      name: "Quarterly review",
      agentMode: "ppt",
    };
    for (const input of [
      { threadUUID: validCreate.threadUUID, name: validCreate.name },
      { threadUUID: validCreate.threadUUID, agentMode: validCreate.agentMode },
      { name: validCreate.name, agentMode: validCreate.agentMode },
      { ...validCreate, url: "https://evil.example" },
      { ...validCreate, uid: 42 },
      { ...validCreate, cloudThreadID: "99" },
      { ...validCreate, projectId: 7 },
      { ...validCreate, threadUUID: "123e4567-e89b-12d3-a456-426614174000" },
      { ...validCreate, threadUUID: "123E4567-E89B-42D3-A456-426614174000" },
      { ...validCreate, threadUUID: "../../agent/chat" },
      { ...validCreate, name: "" },
      { ...validCreate, name: " Quarterly review" },
      { ...validCreate, name: "Quarterly\nreview" },
      { ...validCreate, name: "\ud800" },
      { ...validCreate, name: "x".repeat(201) },
      { ...validCreate, agentMode: "../ppt" },
      { ...validCreate, agentMode: "x".repeat(65) },
    ]) {
      await assert.rejects(
        () => typed.agent.createThread(input as never),
        /agent\.createThread/
      );
    }
    const noop = () => {};
    assert.throws(
      () =>
        typed.agent.startTurn(
          {
            threadUUID: "thread-123",
            userText: "hello",
            chatMode: "ppt",
            url: "https://evil.example",
          } as never,
          noop
        ),
      /unsupported field url/
    );
    for (const input of [
      { threadUUID: "a/b", userText: "hello", chatMode: "ppt" },
      { threadUUID: "thread-123", userText: "   ", chatMode: "ppt" },
      { threadUUID: "thread-123", userText: "broken \ud800", chatMode: "ppt" },
      { threadUUID: "thread-123", userText: "hello", chatMode: "../ppt" },
      {
        threadUUID: "thread-123",
        userText: "x".repeat(256 * 1024 + 1),
        chatMode: "ppt",
      },
    ]) {
      assert.throws(() => typed.agent.startTurn(input, noop), /agent\.startTurn|threadUUID/);
    }
    assert.throws(
      () => typed.agent.startTurn(
        { threadUUID: "thread-123", userText: "hello", chatMode: "ppt" },
        null as never
      ),
      /callback must be a function/
    );
    assert.throws(
      () => typed.agent.cancelTurn("../../foreign-turn"),
      /canonical v4 UUID/
    );
    assert.equal(calls, 0);
  });

  it("returns typed non-2xx error payloads without widening the facade", async () => {
    const typed = loadPreloadWithElectronMock({
      WORKMAX_LOCAL_PORT: "49152",
      WORKMAX_LOCAL_TOKEN: "local-token",
    }).desktopBridge;
    globalThis.fetch = async () =>
      new Response(JSON.stringify({ error: "not configured" }), {
        status: 503,
        statusText: "Service Unavailable",
        headers: { "Content-Type": "application/json" },
      });

    assert.deepEqual(await typed.auth.status(), {
      ok: false,
      status: 503,
      statusText: "Service Unavailable",
      headers: { "content-type": "application/json" },
      error: { error: "not configured" },
    });
    assert.equal(Object.prototype.hasOwnProperty.call(typed, "fetch"), false);
  });

  it("preserves ready, pending, and paused replay DTOs for same-UUID retries", async () => {
    const ipcInvocations: Array<{ channel: string; args: unknown[] }> = [];
    const typed = loadPreloadWithElectronMock(
      {
        WORKMAX_LOCAL_PORT: "49152",
        WORKMAX_LOCAL_TOKEN: "local-token",
      },
      ipcInvocations
    ).desktopBridge;
    const calls: Array<{ url: string; init?: RequestInit }> = [];
    globalThis.fetch = async (input, init) => {
      calls.push({ url: String(input), init });
      if (calls.length === 1) {
        return new Response(
          JSON.stringify({
            state: "ready",
            created: true,
            thread: {
              uuid: "123e4567-e89b-42d3-a456-426614174000",
              name: "Quarterly review",
              agent_mode: "ppt",
              message_count: 0,
              updated_at: "2026-08-06T00:00:00Z",
              cloud_sync_state: "synced",
            },
          }),
          { status: 201, headers: { "Content-Type": "application/json" } }
        );
      }
      if (calls.length === 2) {
        return new Response(
          JSON.stringify({
            state: "pending_local_sync",
            thread_uuid: "123e4567-e89b-42d3-a456-426614174000",
          }),
          { status: 202, headers: { "Content-Type": "application/json" } }
        );
      }
      return new Response(
        JSON.stringify({
          state: "ready",
          created: false,
          thread: {
            uuid: "123e4567-e89b-42d3-a456-426614174000",
            name: "Current cloud title",
            agent_mode: "ppt_revised",
            message_count: 2,
            updated_at: "2026-08-06T00:01:00Z",
            cloud_sync_state: "paused",
          },
        }),
        { status: 200, headers: { "Content-Type": "application/json" } }
      );
    };
    const input = {
      threadUUID: "123e4567-e89b-42d3-a456-426614174000",
      name: "Quarterly review",
      agentMode: "ppt",
    };

    const ready = await typed.agent.createThread(input);
    const pending = await typed.agent.createThread(input);
    const current = await typed.agent.createThread(input);

    assert.equal(ready.ok, true);
    assert.equal(ready.status, 201);
    assert.deepEqual(ready.ok ? ready.data : null, {
      state: "ready",
      created: true,
      thread: {
        uuid: input.threadUUID,
        name: input.name,
        agent_mode: input.agentMode,
        message_count: 0,
        updated_at: "2026-08-06T00:00:00Z",
        cloud_sync_state: "synced",
      },
    });
    assert.equal(pending.ok, true);
    assert.equal(pending.status, 202);
    assert.deepEqual(pending.ok ? pending.data : null, {
      state: "pending_local_sync",
      thread_uuid: input.threadUUID,
    });
    assert.deepEqual(current.ok ? current.data : null, {
      state: "ready",
      created: false,
      thread: {
        uuid: input.threadUUID,
        name: "Current cloud title",
        agent_mode: "ppt_revised",
        message_count: 2,
        updated_at: "2026-08-06T00:01:00Z",
        cloud_sync_state: "paused",
      },
    });
    assert.deepEqual(
      calls.map(({ url, init }) => ({
        url,
        method: init?.method,
        body: init?.body,
      })),
      [
        {
          url: `http://127.0.0.1:49152/agent/threads/${input.threadUUID}`,
          method: "PUT",
          body: JSON.stringify({ name: input.name, agent_mode: input.agentMode }),
        },
        {
          url: `http://127.0.0.1:49152/agent/threads/${input.threadUUID}`,
          method: "PUT",
          body: JSON.stringify({ name: input.name, agent_mode: input.agentMode }),
        },
        {
          url: `http://127.0.0.1:49152/agent/threads/${input.threadUUID}`,
          method: "PUT",
          body: JSON.stringify({ name: input.name, agent_mode: input.agentMode }),
        },
      ]
    );
    assert.deepEqual(ipcInvocations, []);
  });

  it("rejects malformed successful thread PUT DTOs at the typed bridge boundary", async () => {
    const typed = loadPreloadWithElectronMock({
      WORKMAX_LOCAL_PORT: "49152",
      WORKMAX_LOCAL_TOKEN: "local-token",
    }).desktopBridge;
    const input = {
      threadUUID: "123e4567-e89b-42d3-a456-426614174000",
      name: "Quarterly review",
      agentMode: "ppt",
    };
    const ready = (
      root: Record<string, unknown> = {},
      thread: Record<string, unknown> = {}
    ) => ({
      state: "ready",
      created: true,
      thread: {
        uuid: input.threadUUID,
        name: input.name,
        agent_mode: input.agentMode,
        message_count: 0,
        updated_at: "2026-08-06T00:00:00Z",
        cloud_sync_state: "synced",
        ...thread,
      },
      ...root,
    });
    const cases: Array<{ name: string; status: number; body: unknown }> = [
      { name: "null", status: 200, body: null },
      {
        name: "pending extra field",
        status: 202,
        body: {
          state: "pending_local_sync",
          thread_uuid: input.threadUUID,
          cloud_thread_id: "private",
        },
      },
      {
        name: "pending foreign UUID",
        status: 202,
        body: {
          state: "pending_local_sync",
          thread_uuid: "123e4567-e89b-42d3-a456-426614174099",
        },
      },
      {
        name: "pending wrong status",
        status: 200,
        body: { state: "pending_local_sync", thread_uuid: input.threadUUID },
      },
      { name: "created true wrong status", status: 200, body: ready() },
      {
        name: "created false wrong status",
        status: 201,
        body: ready({ created: false }),
      },
      {
        name: "unsupported success status",
        status: 203,
        body: ready(),
      },
      {
        name: "foreign UUID",
        status: 201,
        body: ready({}, { uuid: "123e4567-e89b-42d3-a456-426614174099" }),
      },
      {
        name: "created name mismatch",
        status: 201,
        body: ready({}, { name: "Different title" }),
      },
      {
        name: "created mode mismatch",
        status: 201,
        body: ready({}, { agent_mode: "writer" }),
      },
      { name: "empty name", status: 201, body: ready({}, { name: "" }) },
      {
        name: "invalid mode",
        status: 201,
        body: ready({}, { agent_mode: "../ppt" }),
      },
      {
        name: "negative count",
        status: 201,
        body: ready({}, { message_count: -1 }),
      },
      {
        name: "fractional count",
        status: 201,
        body: ready({}, { message_count: 0.5 }),
      },
      {
        name: "invalid timestamp",
        status: 201,
        body: ready({}, { updated_at: "not-a-time" }),
      },
      {
        name: "unsynced row",
        status: 201,
        body: ready({}, { cloud_sync_state: "pending" }),
      },
      {
        name: "thread extra field",
        status: 201,
        body: ready({}, { cloud_thread_id: "private" }),
      },
    ];

    for (const testCase of cases) {
      globalThis.fetch = async () =>
        new Response(JSON.stringify(testCase.body), {
          status: testCase.status,
          headers: { "Content-Type": "application/json" },
        });
      await assert.rejects(
        () => typed.agent.createThread(input),
        /agent\.createThread response is malformed/,
        testCase.name
      );
    }

    globalThis.fetch = async () =>
      new Response(JSON.stringify({ error: "thread_uuid_conflict" }), {
        status: 409,
        statusText: "Conflict",
        headers: { "Content-Type": "application/json" },
      });
    assert.deepEqual(await typed.agent.createThread(input), {
      ok: false,
      status: 409,
      statusText: "Conflict",
      headers: { "content-type": "application/json" },
      error: { error: "thread_uuid_conflict" },
    });
  });

  it("strictly validates recoverable turns without rejecting legacy thread UUIDs", async () => {
    const typed = loadPreloadWithElectronMock({
      WORKMAX_LOCAL_PORT: "49152",
      WORKMAX_LOCAL_TOKEN: "local-token",
    }).desktopBridge;
    const turnUUID = "123e4567-e89b-42d3-a456-426614174000";
    const validItem = {
      turn_uuid: turnUUID,
      thread_uuid: "legacy-thread-123",
      user_text: "Resume the quarterly deck",
      chat_mode: "ppt",
      state: "interrupted",
      last_error_kind: "transport_error",
      updated_at: "2026-08-06T00:00:00Z",
    };
    globalThis.fetch = async () =>
      new Response(JSON.stringify({ items: [validItem], count: 1 }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    const result = await typed.agent.listRecoverableTurns();
    assert.equal(result.ok, true);
    assert.deepEqual(result.ok ? result.data : null, {
      items: [validItem],
      count: 1,
    });

    for (const body of [
      null,
      { items: [validItem], count: 0 },
      { items: [{ ...validItem, turn_uuid: "turn-legacy" }], count: 1 },
      { items: [{ ...validItem, thread_uuid: "bad/thread" }], count: 1 },
      { items: [{ ...validItem, user_text: "broken \udc00" }], count: 1 },
      { items: [{ ...validItem, state: "running" }], count: 1 },
      { items: [{ ...validItem, uid: 42 }], count: 1 },
      { items: [{ ...validItem, cloud_thread_id: 99 }], count: 1 },
      { items: [{ ...validItem }, { ...validItem }], count: 2 },
    ]) {
      globalThis.fetch = async () =>
        new Response(JSON.stringify(body), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        });
      await assert.rejects(
        () => typed.agent.listRecoverableTurns(),
        /agent\.listRecoverableTurns response is malformed/
      );
    }
  });

  it("resumes canonical idempotent recovery through the fixed replay route", async () => {
    const typed = loadPreloadWithElectronMock({
      WORKMAX_LOCAL_PORT: "49152",
      WORKMAX_LOCAL_TOKEN: "local-token",
    }).desktopBridge;
    const calls: Array<{ url: string; init?: RequestInit }> = [];
    globalThis.fetch = async (input, init) => {
      calls.push({ url: String(input), init });
      return agentStreamResponse([
        new TextEncoder().encode(
          'event: text\ndata: {"text":"replayed"}\n\n' +
            'event: done\ndata: {"code":"OK","subtype":"resume","is_error":false}\n\n'
        ),
      ]);
    };
    const turnUUID = "123e4567-e89b-42d3-a456-426614174000";
    const events: AgentTurnEvent[] = [];
    const open = typed.agent.resumeTurn(turnUUID, (event) => events.push(event));
    await waitFor(() => events.some((event) => event.type === "done"));

    assert.deepEqual(open, { turnID: turnUUID });
    assert.equal(calls.length, 1);
    assert.equal(
      calls[0].url,
      `http://127.0.0.1:49152/agent/turns/${turnUUID}/replay`
    );
    assert.equal(calls[0].init?.method, "POST");
    assert.equal(calls[0].init?.body, undefined);
    assert.equal(new Headers(calls[0].init?.headers).get("Accept"), "text/event-stream");
    assert.deepEqual(events, [
      { type: "text_delta", turnID: turnUUID, delta: "replayed" },
      {
        type: "done",
        turnID: turnUUID,
        result: { code: "OK", subtype: "resume", is_error: false },
      },
    ]);
    assert.throws(
      () => typed.agent.resumeTurn("turn-legacy", () => {}),
      /canonical v4 UUID/
    );
    assert.throws(
      () => typed.agent.resumeTurn(turnUUID, null as never),
      /callback must be a function/
    );
  });

  it("streams data-only Agent envelopes across UTF-8/CRLF chunks and fences late events", async () => {
    const typed = loadPreloadWithElectronMock({
      WORKMAX_LOCAL_PORT: "49152",
      WORKMAX_LOCAL_TOKEN: "local-token",
    }).desktopBridge;
    const calls: Array<{ url: string; init?: RequestInit }> = [];
    const wire = [
      ": connected\r\n\r\n",
      'data: {"type":"block","block":{"type":"text","text":"你🙂"}}\r\n\r\n',
      "event: tool\r\n",
      'data: {"answer":\r\n',
      'data: 42,"opaque_secret":"tool-secret"}\r\n\r\n',
      'data: {"type":"done","result":{"status":"ok","opaque_secret":"done-secret"}}\r\n\r\n',
      'event: text\r\ndata: {"text":"late"}\r\n\r\n',
    ].join("");
    const encoded = new TextEncoder().encode(wire);
    const emoji = new TextEncoder().encode("🙂");
    const emojiOffset = indexOfBytes(encoded, emoji);
    assert.notEqual(emojiOffset, -1);
    const chunks = [
      encoded.slice(0, emojiOffset + 2),
      encoded.slice(emojiOffset + 2, emojiOffset + emoji.byteLength),
      encoded.slice(emojiOffset + emoji.byteLength),
    ];
    globalThis.fetch = async (input, init) => {
      calls.push({ url: String(input), init });
      return agentStreamResponse(chunks);
    };

    const events: AgentTurnEvent[] = [];
    const open = typed.agent.startTurn(
      { threadUUID: "thread-123", userText: "Build slides", chatMode: "ppt" },
      (event) => events.push(event)
    );
    await waitFor(() => events.some((event) => event.type === "done"));

    assert.match(
      open.turnID,
      /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/u
    );
    assert.equal(calls.length, 1);
    assert.equal(calls[0].url, "http://127.0.0.1:49152/agent/chat");
    const headers = new Headers(calls[0].init?.headers);
    assert.equal(calls[0].init?.method, "POST");
    assert.equal(headers.get("Accept"), "text/event-stream");
    assert.equal(headers.get("Content-Type"), "application/json");
    assert.equal(headers.get("X-Local-Token"), "local-token");
    assert.ok(calls[0].init?.signal instanceof AbortSignal);
    assert.deepEqual(JSON.parse(String(calls[0].init?.body)), {
      turn_uuid: open.turnID,
      thread_uuid: "thread-123",
      user_text: "Build slides",
      chat_mode: "ppt",
      payload: { stream: true },
    });
    assert.deepEqual(events, [
      { type: "text_delta", turnID: open.turnID, delta: "你🙂" },
      {
        type: "unknown",
        turnID: open.turnID,
        event: "tool",
      },
      {
        type: "done",
        turnID: open.turnID,
        result: { code: "", subtype: "", is_error: false },
      },
    ]);
    assert.doesNotMatch(JSON.stringify(events), /tool-secret|done-secret/);
  });

  it("normalizes explicit text/text_delta/done Agent events", async () => {
    const typed = loadPreloadWithElectronMock({
      WORKMAX_LOCAL_PORT: "49152",
      WORKMAX_LOCAL_TOKEN: "local-token",
    }).desktopBridge;
    globalThis.fetch = async () =>
      agentStreamResponse([
        new TextEncoder().encode(
          'event: text\ndata: {"text":"hello"}\n\n' +
            'event: text_delta\ndata: {"delta":" world"}\n\n' +
            'event: done\ndata: {"code":"OK","subtype":"already_processed","is_error":false,"opaque_secret":"done-secret"}\n\n'
        ),
      ]);

    const events: AgentTurnEvent[] = [];
    const open = typed.agent.startTurn(
      { threadUUID: "thread-explicit", userText: "go", chatMode: "ppt" },
      (event) => events.push(event)
    );
    await waitFor(() => events.some((event) => event.type === "done"));
    assert.deepEqual(events, [
      { type: "text_delta", turnID: open.turnID, delta: "hello" },
      { type: "text_delta", turnID: open.turnID, delta: " world" },
      {
        type: "done",
        turnID: open.turnID,
        result: { code: "OK", subtype: "already_processed", is_error: false },
      },
    ]);
    assert.doesNotMatch(JSON.stringify(events), /done-secret/);
  });

  it("bounds and sanitizes done metadata before invoking Renderer callbacks", async () => {
    const typed = loadPreloadWithElectronMock({
      WORKMAX_LOCAL_PORT: "49152",
      WORKMAX_LOCAL_TOKEN: "local-token",
    }).desktopBridge;
    const rawDone = JSON.stringify({
      code: "x".repeat(129),
      subtype: "broken \ud800",
      is_error: "true",
      opaque_secret: "done-boundary-secret",
    });
    globalThis.fetch = async () =>
      agentStreamResponse([
        new TextEncoder().encode(`event: done\ndata: ${rawDone}\n\n`),
      ]);

    const events: AgentTurnEvent[] = [];
    const open = typed.agent.startTurn(
      { threadUUID: "thread-done-boundary", userText: "go", chatMode: "ppt" },
      (event) => events.push(event)
    );
    await waitFor(() => events.length === 1);
    assert.deepEqual(events, [
      {
        type: "done",
        turnID: open.turnID,
        result: { code: "", subtype: "", is_error: false },
      },
    ]);
    assert.doesNotMatch(JSON.stringify(events), /done-boundary-secret/);
  });

  it("normalizes a stream proxy_error, including session_changed", async () => {
    const typed = loadPreloadWithElectronMock({
      WORKMAX_LOCAL_PORT: "49152",
      WORKMAX_LOCAL_TOKEN: "local-token",
    }).desktopBridge;
    globalThis.fetch = async () =>
      agentStreamResponse([
        new TextEncoder().encode(
          "event: proxy_error\n" +
            'data: {"kind":"session_changed","message":"Session changed","retryable":false,"retry_after_ms":0,"log_id":"log-1","details":{"opaque_secret":"proxy-secret"},"opaque_top_level":"proxy-top-secret"}\n\n' +
            'event: done\ndata: {"late":true}\n\n'
        ),
      ]);

    const events: AgentTurnEvent[] = [];
    const open = typed.agent.startTurn(
      { threadUUID: "thread-proxy", userText: "go", chatMode: "ppt" },
      (event) => events.push(event)
    );
    await waitFor(() => events.length === 1);
    assert.deepEqual(events, [
      {
        type: "proxy_error",
        turnID: open.turnID,
        error: {
          kind: "session_changed",
          message: "Session changed",
          retryable: false,
          retry_after_ms: 0,
          log_id: "log-1",
        },
      },
    ]);
    assert.doesNotMatch(JSON.stringify(events), /proxy-secret|proxy-top-secret/);
  });

  it("cancels the owned reader exactly once and emits one canceled terminal", async () => {
    const typed = loadPreloadWithElectronMock({
      WORKMAX_LOCAL_PORT: "49152",
      WORKMAX_LOCAL_TOKEN: "local-token",
    }).desktopBridge;
    let readerCancels = 0;
    let cancelCalls = 0;
    const cancelRequests: Array<{ url: string; init?: RequestInit }> = [];
    globalThis.fetch = async (input, init) => {
      if (String(input).endsWith("/cancel")) {
        cancelCalls += 1;
        cancelRequests.push({ url: String(input), init });
        return new Response(
          JSON.stringify({
            turn_uuid: String(input).split("/").at(-2),
            canceled: cancelCalls === 1,
          }),
          { status: 200, headers: { "Content-Type": "application/json" } }
        );
      }
      return new Response(
        new ReadableStream<Uint8Array>({
          start(controller) {
            controller.enqueue(
              new TextEncoder().encode('event: text\ndata: {"text":"first"}\n\n')
            );
          },
          cancel() {
            readerCancels += 1;
          },
        }),
        { status: 200, headers: { "Content-Type": "text/event-stream" } }
      );
    };

    const events: AgentTurnEvent[] = [];
    const open = typed.agent.startTurn(
      { threadUUID: "thread-cancel", userText: "go", chatMode: "ppt" },
      (event) => events.push(event)
    );
    await waitFor(() => events.some((event) => event.type === "text_delta"));
    assert.deepEqual(await typed.agent.cancelTurn(open.turnID), {
      turnID: open.turnID,
      canceled: true,
    });
    assert.deepEqual(await typed.agent.cancelTurn(open.turnID), {
      turnID: open.turnID,
      canceled: false,
    });
    await waitFor(() => readerCancels === 1);
    assert.deepEqual(events, [
      { type: "text_delta", turnID: open.turnID, delta: "first" },
      { type: "canceled", turnID: open.turnID },
    ]);
    assert.equal(cancelCalls, 2);
    for (const request of cancelRequests) {
      assert.equal(
        request.url,
        `http://127.0.0.1:49152/agent/turns/${open.turnID}/cancel`
      );
      assert.equal(request.init?.method, "POST");
      assert.equal(request.init?.body, undefined);
      const headers = new Headers(request.init?.headers);
      assert.equal(headers.get("Accept"), "application/json");
      assert.equal(headers.get("Content-Type"), null);
      assert.equal(headers.get("X-Local-Token"), "local-token");
    }
  });

  it("locally fences an active turn before a failing recovery cancel returns", async () => {
    const typed = loadPreloadWithElectronMock({
      WORKMAX_LOCAL_PORT: "49152",
      WORKMAX_LOCAL_TOKEN: "local-token",
    }).desktopBridge;
    let streamController: ReadableStreamDefaultController<Uint8Array>;
    let resolveCancel: ((response: Response) => void) | undefined;
    globalThis.fetch = async (input) => {
      if (String(input).endsWith("/cancel")) {
        return new Promise<Response>((resolve) => {
          resolveCancel = resolve;
        });
      }
      return new Response(
        new ReadableStream<Uint8Array>({
          start(controller) {
            streamController = controller;
            controller.enqueue(
              new TextEncoder().encode('event: text\ndata: {"text":"first"}\n\n')
            );
          },
        }),
        { status: 200, headers: { "Content-Type": "text/event-stream" } }
      );
    };

    const events: AgentTurnEvent[] = [];
    const open = typed.agent.startTurn(
      { threadUUID: "thread-cancel-fail", userText: "go", chatMode: "ppt" },
      (event) => events.push(event)
    );
    await waitFor(() => events.length === 1);
    const cancel = typed.agent.cancelTurn(open.turnID);
    await waitFor(() => events.some((event) => event.type === "canceled"));
    assert.throws(
      () =>
        streamController!.enqueue(
          new TextEncoder().encode('event: text\ndata: {"text":"late"}\n\n')
        ),
      /closed|invalid state/i
    );
    await waitFor(() => resolveCancel !== undefined);
    resolveCancel!(
      new Response(JSON.stringify({ error: "unavailable" }), { status: 503 })
    );
    await assert.rejects(cancel, /HTTP 503/);
    await new Promise((resolve) => setImmediate(resolve));
    assert.deepEqual(events, [
      { type: "text_delta", turnID: open.turnID, delta: "first" },
      { type: "canceled", turnID: open.turnID },
    ]);
  });

  it("rejects malformed recovery cancel results without widening the DTO", async () => {
    const typed = loadPreloadWithElectronMock({
      WORKMAX_LOCAL_PORT: "49152",
      WORKMAX_LOCAL_TOKEN: "local-token",
    }).desktopBridge;
    const turnUUID = "123e4567-e89b-42d3-a456-426614174000";
    for (const payload of [
      null,
      { turn_uuid: "123e4567-e89b-42d3-a456-426614174099", canceled: true },
      { turn_uuid: turnUUID, canceled: "true" },
      { turn_uuid: turnUUID, canceled: true, uid: 42 },
    ]) {
      globalThis.fetch = async () =>
        new Response(JSON.stringify(payload), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        });
      await assert.rejects(
        () => typed.agent.cancelTurn(turnUUID),
        /agent\.cancelTurn response is malformed/
      );
    }
  });

  it("fails closed at the five-turn Preload resource limit", async () => {
    const typed = loadPreloadWithElectronMock({
      WORKMAX_LOCAL_PORT: "49152",
      WORKMAX_LOCAL_TOKEN: "local-token",
    }).desktopBridge;
    let fetches = 0;
    globalThis.fetch = async (input) => {
      fetches += 1;
      if (String(input).endsWith("/cancel")) {
        return new Response(
          JSON.stringify({
            turn_uuid: String(input).split("/").at(-2),
            canceled: true,
          }),
          { status: 200, headers: { "Content-Type": "application/json" } }
        );
      }
      return new Response(new ReadableStream<Uint8Array>({}), {
        status: 200,
        headers: { "Content-Type": "text/event-stream" },
      });
    };

    const opens = Array.from({ length: 5 }, (_, index) =>
      typed.agent.startTurn(
        {
          threadUUID: `thread-limit-${index}`,
          userText: "go",
          chatMode: "ppt",
        },
        () => {}
      )
    );
    assert.throws(
      () =>
        typed.agent.startTurn(
          { threadUUID: "thread-limit-6", userText: "go", chatMode: "ppt" },
          () => {}
        ),
      /active turn limit of 5 reached/
    );
    assert.equal(fetches, 5);
    const canceled = await Promise.all(
      opens.map((open) => typed.agent.cancelTurn(open.turnID))
    );
    assert.equal(canceled.every((result) => result.canceled), true);
    assert.equal(fetches, 10);
  });

  it("reports EOF and 1 MiB frame violations as a single protocol terminal", async () => {
    const typed = loadPreloadWithElectronMock({
      WORKMAX_LOCAL_PORT: "49152",
      WORKMAX_LOCAL_TOKEN: "local-token",
    }).desktopBridge;
    const responses = [
      agentStreamResponse([
        new TextEncoder().encode('event: text\ndata: {"text":"partial"}\n\n'),
      ]),
      agentStreamResponse([
        new TextEncoder().encode(`data: ${"x".repeat((1 << 20) + 1)}`),
      ]),
    ];
    globalThis.fetch = async () => responses.shift()!;

    const eofEvents: AgentTurnEvent[] = [];
    const eof = typed.agent.startTurn(
      { threadUUID: "thread-eof", userText: "go", chatMode: "ppt" },
      (event) => eofEvents.push(event)
    );
    await waitFor(() => eofEvents.some((event) => event.type === "protocol_error"));
    assert.deepEqual(eofEvents, [
      { type: "text_delta", turnID: eof.turnID, delta: "partial" },
      {
        type: "protocol_error",
        turnID: eof.turnID,
        code: "unexpected_eof",
        message: "Agent stream ended without a terminal event",
      },
    ]);

    const largeEvents: AgentTurnEvent[] = [];
    const large = typed.agent.startTurn(
      { threadUUID: "thread-large", userText: "go", chatMode: "ppt" },
      (event) => largeEvents.push(event)
    );
    await waitFor(() => largeEvents.some((event) => event.type === "protocol_error"));
    assert.deepEqual(largeEvents, [
      {
        type: "protocol_error",
        turnID: large.turnID,
        code: "frame_too_large",
        message: `Agent SSE frame exceeds ${1 << 20} bytes`,
      },
    ]);
  });

  it("maps bounded HTTP session_changed JSON to the unified proxy_error signal", async () => {
    const typed = loadPreloadWithElectronMock({
      WORKMAX_LOCAL_PORT: "49152",
      WORKMAX_LOCAL_TOKEN: "local-token",
    }).desktopBridge;
    globalThis.fetch = async () =>
      new Response(JSON.stringify({ error: "session_changed" }), {
        status: 409,
        headers: { "Content-Type": "application/json" },
      });

    const events: AgentTurnEvent[] = [];
    const open = typed.agent.startTurn(
      { threadUUID: "thread-session", userText: "go", chatMode: "ppt" },
      (event) => events.push(event)
    );
    await waitFor(() => events.length === 1);
    assert.deepEqual(events, [
      {
        type: "proxy_error",
        turnID: open.turnID,
        error: {
          kind: "session_changed",
          message: "Desktop session changed",
          retryable: false,
        },
      },
    ]);
  });

  it("bounds non-2xx Agent bodies before reporting a generic HTTP protocol error", async () => {
    const typed = loadPreloadWithElectronMock({
      WORKMAX_LOCAL_PORT: "49152",
      WORKMAX_LOCAL_TOKEN: "local-token",
    }).desktopBridge;
    let bodyCancels = 0;
    globalThis.fetch = async () =>
      new Response(
        new ReadableStream<Uint8Array>({
          start(controller) {
            controller.enqueue(new Uint8Array(64 * 1024 + 1));
          },
          cancel() {
            bodyCancels += 1;
          },
        }),
        { status: 502 }
      );

    const events: AgentTurnEvent[] = [];
    const open = typed.agent.startTurn(
      { threadUUID: "thread-http", userText: "go", chatMode: "ppt" },
      (event) => events.push(event)
    );
    await waitFor(() => events.length === 1);
    assert.deepEqual(events, [
      {
        type: "protocol_error",
        turnID: open.turnID,
        code: "http_error",
        message: "Agent Sidecar returned HTTP 502",
      },
    ]);
    assert.equal(bodyCancels, 1);
  });

  it("routes fetch calls through loopback and injects local-only headers", async () => {
    const exposed = loadPreloadWithElectronMock({
      WORKMAX_LOCAL_PORT: "49152",
      WORKMAX_LOCAL_TOKEN: "local-token",
    });
    const calls: Array<{ url: string; init?: RequestInit }> = [];
    globalThis.fetch = async (input: string | URL | Request, init?: RequestInit) => {
      calls.push({ url: String(input), init });
      return new Response(JSON.stringify({ ok: true }), { status: 200 });
    };

    const response = await exposed.workmaxLocal.fetch("health", {
      method: "POST",
      headers: { "X-Request-ID": "caller-request-id" },
    });

    assert.equal(response.status, 200);
    assert.equal(response.ok, true);
    assert.equal(calls.length, 1);
    assert.equal(calls[0].url, "http://127.0.0.1:49152/health");
    const headers = new Headers(calls[0].init?.headers);
    assert.equal(headers.get("X-Local-Token"), "local-token");
    assert.equal(headers.get("X-Request-ID"), "caller-request-id");
  });

  it("rejects unsafe fetch paths before issuing a request", async () => {
    const exposed = loadPreloadWithElectronMock({
      WORKMAX_LOCAL_PORT: "49152",
      WORKMAX_LOCAL_TOKEN: "local-token",
    });
    const calls: Array<{ url: string; init?: RequestInit }> = [];
    globalThis.fetch = async (input: string | URL | Request, init?: RequestInit) => {
      calls.push({ url: String(input), init });
      return new Response(JSON.stringify({ ok: true }), { status: 200 });
    };

    await assert.rejects(
      () => exposed.workmaxLocal.fetch("https://example.com/health"),
      /sidecar-relative/
    );
    await assert.rejects(
      () => exposed.workmaxLocal.fetch("//example.com/health"),
      /sidecar-relative/
    );
    await assert.rejects(
      () => exposed.workmaxLocal.fetch("/health#fragment"),
      /fragment/
    );
    await assert.rejects(
      () => exposed.workmaxLocal.fetch("/health\nX-Local-Token: leaked"),
      /control characters/
    );
    await assert.rejects(
      () => exposed.workmaxLocal.fetch(""),
      /non-empty string/
    );
    await assert.rejects(
      () => exposed.workmaxLocal.fetch(" /health"),
      /leading or trailing whitespace/
    );
    await assert.rejects(
      () => exposed.workmaxLocal.fetch(null as unknown as string),
      /non-empty string/
    );
    await assert.rejects(
      () => exposed.workmaxLocal.fetch("/" + "a".repeat(8 * 1024)),
      /too long/
    );
    await assert.rejects(
      () => exposed.workmaxLocal.fetch("/%2525252525252525252561uth/start"),
      /excessive URL encoding/
    );
    for (const privilegedPath of [
      "/auth/start",
      "/auth/start/",
      "/auth/start/?next=%2Fhealth",
      "/auth/%73tart",
      "/auth/%2573tart",
      "/%2525252561uth/start",
      "/auth/../auth/start",
      "/auth/%2e%2e/auth/start",
      "/auth/login-transaction",
      "/auth/login-transaction/",
      "/auth/login-transaction/password",
      "/auth/%6cogin-transaction/password",
      "/auth/other/../login-transaction/password",
      "/auth/login-transaction/%2e%2e/login-transaction",
    ]) {
      await assert.rejects(
        () => exposed.workmaxLocal.fetch(privilegedPath, { method: "POST" }),
        /privileged sign-in transaction route/
      );
    }
    for (const typedAgentPath of [
      "/agent/chat",
      "/agent/chat/",
      "/agent//chat",
      "/agent/chat?retry=1",
      "/agent/%63hat",
      "/agent/%2563hat",
      "/agent/other/../chat",
      "/agent/skills/catalog",
      "/agent/skills/catalog/",
      "/agent//skills///catalog",
      "/agent/skills/catalog?refresh=1",
      "/agent/skills/%63atalog",
      "/agent/skills/%2563atalog",
      "/agent/skills/other/../catalog",
      "/agent/turns/recoverable",
      `/agent/turns/123e4567-e89b-42d3-a456-426614174000/replay`,
      `/agent/turns/123e4567-e89b-42d3-a456-426614174000/cancel`,
      "/agent/%74urns/recoverable",
      "/agent/other/../turns/recoverable",
    ]) {
      await assert.rejects(
        () => exposed.workmaxLocal.fetch(typedAgentPath),
        /typed Agent routes/
      );
    }
    const createUUID = "123e4567-e89b-42d3-a456-426614174000";
    for (const typedThreadMutationPath of [
      `/agent/threads/${createUUID}`,
      `/agent/threads/${createUUID}/`,
      `/agent//threads//${createUUID}`,
      `/agent/threads/${createUUID}?retry=1`,
      `/agent/%74hreads/${createUUID}`,
      `/agent/%2574hreads/${createUUID}`,
      `/agent/other/../threads/${createUUID}`,
      `/agent/threads/%31%32%33e4567-e89b-42d3-a456-426614174000`,
    ]) {
      await assert.rejects(
        () => exposed.workmaxLocal.fetch(typedThreadMutationPath, { method: "put" }),
        /typed Agent routes/
      );
    }

    assert.equal(calls.length, 0);
  });

  it("forces cookie omission and rejects redirects for compatibility fetches", async () => {
    const exposed = loadPreloadWithElectronMock({
      WORKMAX_LOCAL_PORT: "49152",
      WORKMAX_LOCAL_TOKEN: "local-token",
    });
    const calls: RequestInit[] = [];
    globalThis.fetch = async (_input, init) => {
      calls.push(init ?? {});
      return new Response("{}", {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    };

    await exposed.workmaxLocal.fetch("/health", {
      credentials: "include",
      redirect: "follow",
    });

    assert.equal(calls.length, 1);
    assert.equal(calls[0].credentials, "omit");
    assert.equal(calls[0].redirect, "error");
  });

  it("returns a contextBridge-safe response facade for JSON and text callers", async () => {
    const exposed = loadPreloadWithElectronMock({
      WORKMAX_LOCAL_PORT: "49152",
      WORKMAX_LOCAL_TOKEN: "local-token",
    });
    globalThis.fetch = async () => {
      return new Response(JSON.stringify({ state: "authenticated" }), {
        status: 201,
        statusText: "Created",
        headers: { "Content-Type": "application/json", "X-Test": "yes" },
      });
    };

    const response = await exposed.workmaxLocal.fetch("/auth/status");

    assert.equal(response.ok, true);
    assert.equal(response.status, 201);
    assert.equal(response.statusText, "Created");
    assert.equal(response.headers["content-type"], "application/json");
    assert.equal(response.headers["x-test"], "yes");
    assert.deepEqual(await response.json(), { state: "authenticated" });
    assert.equal(await response.text(), JSON.stringify({ state: "authenticated" }));
  });

  it("keeps GET Agent history on legacy fetch while closing typed mutations", async () => {
    const exposed = loadPreloadWithElectronMock({
      WORKMAX_LOCAL_PORT: "49152",
      WORKMAX_LOCAL_TOKEN: "local-token",
    });
    globalThis.fetch = async () => {
      return new Response(JSON.stringify({ items: [], count: 0 }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    };

    await assert.rejects(
      () => exposed.workmaxLocal.fetch("/agent/chat", { method: "POST" }),
      /typed Agent routes/
    );
    await assert.rejects(
      () => exposed.workmaxLocal.fetch("/agent/skills/catalog"),
      /typed Agent routes/
    );
    await assert.rejects(
      () =>
        exposed.workmaxLocal.fetch(
          "/agent/threads/123e4567-e89b-42d3-a456-426614174000",
          { method: "PUT" }
        ),
      /typed Agent routes/
    );
    const history = await exposed.workmaxLocal.fetch("/agent/threads");
    assert.equal(history.ok, true);
    assert.deepEqual(await history.json(), { items: [], count: 0 });
    const messages = await exposed.workmaxLocal.fetch(
      "/agent/threads/123e4567-e89b-42d3-a456-426614174000/messages"
    );
    assert.equal(messages.ok, true);
  });

  it("generates an X-Request-ID when the caller does not provide one", async () => {
    const exposed = loadPreloadWithElectronMock({
      WORKMAX_LOCAL_PORT: "49152",
      WORKMAX_LOCAL_TOKEN: "local-token",
    });
    const calls: Array<{ url: string; init?: RequestInit }> = [];
    globalThis.fetch = async (input: string | URL | Request, init?: RequestInit) => {
      calls.push({ url: String(input), init });
      return new Response(JSON.stringify({ ok: true }), { status: 200 });
    };

    await exposed.workmaxLocal.fetch("/system/diagnostics");

    assert.equal(calls.length, 1);
    const headers = new Headers(calls[0].init?.headers);
    assert.equal(headers.get("X-Local-Token"), "local-token");
    assert.match(headers.get("X-Request-ID") ?? "", /^[a-z0-9]+-[0-9a-f]{16}$/);
  });

  it("does not expose either bridge when the sidecar port or token is missing", () => {
    console.error = () => {};

    assert.deepEqual(loadPreloadWithElectronMock({ WORKMAX_LOCAL_PORT: "49152", WORKMAX_LOCAL_TOKEN: "" }), {});
    assert.deepEqual(loadPreloadWithElectronMock({ WORKMAX_LOCAL_PORT: "", WORKMAX_LOCAL_TOKEN: "token" }), {});
    assert.deepEqual(loadPreloadWithElectronMock({ WORKMAX_LOCAL_PORT: "0", WORKMAX_LOCAL_TOKEN: "token" }), {});
    assert.deepEqual(loadPreloadWithElectronMock({ WORKMAX_LOCAL_PORT: "1.5", WORKMAX_LOCAL_TOKEN: "token" }), {});
    assert.deepEqual(loadPreloadWithElectronMock({ WORKMAX_LOCAL_PORT: "65536", WORKMAX_LOCAL_TOKEN: "token" }), {});
    assert.deepEqual(loadPreloadWithElectronMock({ WORKMAX_LOCAL_PORT: "not-a-port", WORKMAX_LOCAL_TOKEN: "token" }), {});
    assert.deepEqual(loadPreloadWithElectronMock({ WORKMAX_LOCAL_PORT: " 49152", WORKMAX_LOCAL_TOKEN: "token" }), {});
    assert.deepEqual(loadPreloadWithElectronMock({ WORKMAX_LOCAL_PORT: "49152\n", WORKMAX_LOCAL_TOKEN: "token" }), {});
    assert.deepEqual(loadPreloadWithElectronMock({ WORKMAX_LOCAL_PORT: "49152", WORKMAX_LOCAL_TOKEN: " token" }), {});
    assert.deepEqual(loadPreloadWithElectronMock({ WORKMAX_LOCAL_PORT: "49152", WORKMAX_LOCAL_TOKEN: "token\nleak" }), {});
  });
});

function agentStreamResponse(chunks: Uint8Array[]): Response {
  return new Response(
    new ReadableStream<Uint8Array>({
      start(controller) {
        for (const chunk of chunks) controller.enqueue(chunk);
        controller.close();
      },
    }),
    { status: 200, headers: { "Content-Type": "text/event-stream" } }
  );
}

function indexOfBytes(haystack: Uint8Array, needle: Uint8Array): number {
  outer: for (
    let index = 0;
    index <= haystack.byteLength - needle.byteLength;
    index += 1
  ) {
    for (let offset = 0; offset < needle.byteLength; offset += 1) {
      if (haystack[index + offset] !== needle[offset]) continue outer;
    }
    return index;
  }
  return -1;
}

async function waitFor(
  predicate: () => boolean,
  timeoutMilliseconds = 2_000
): Promise<void> {
  const deadline = Date.now() + timeoutMilliseconds;
  while (!predicate()) {
    if (Date.now() >= deadline) {
      throw new Error("timed out waiting for asynchronous preload event");
    }
    await new Promise<void>((resolve) => setTimeout(resolve, 0));
  }
}

function loadPreloadWithElectronMock(
  env: NodeJS.ProcessEnv,
  ipcInvocations: Array<{ channel: string; args: unknown[] }> = []
): LoadedPreload {
  process.env = { ...originalEnv, ...env };
  deletePreloadFromRequireCache();

  const exposed: Record<string, unknown> = {};
  const moduleLoader = Module as typeof Module & {
    _load: (request: string, parent: unknown, isMain: boolean) => unknown;
  };
  const originalLoad = moduleLoader._load;

  moduleLoader._load = function mockedLoad(request: string, parent: unknown, isMain: boolean): unknown {
    if (request === "electron") {
      return {
        contextBridge: {
          exposeInMainWorld: (name: string, value: unknown) => {
            exposed[name] = value;
          },
        },
        ipcRenderer: {
          invoke: async (channel: string, ...args: unknown[]) => {
            ipcInvocations.push({ channel, args });
            if (channel === "auth-begin-login-transaction") {
              return { state: "awaiting_password" };
            }
            if (channel === "auth-login-transaction-status") {
              return { state: "submitting" };
            }
            if (channel === "auth-submit-login-password") {
              return { state: "authenticated" };
            }
            if (channel === "auth-cancel-login-transaction") {
              return { state: "idle", error: "canceled" };
            }
            return { ok: true };
          },
        },
      };
    }
    return originalLoad.call(this, request, parent, isMain);
  };

  try {
    require("./preload");
  } finally {
    moduleLoader._load = originalLoad;
  }

  return exposed as LoadedPreload;
}

function deletePreloadFromRequireCache() {
  const preloadPath = require.resolve("./preload");
  delete require.cache[preloadPath];
}
