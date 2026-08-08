#!/usr/bin/env node
import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import vm from "node:vm";

const repoRoot = path.resolve(path.dirname(new URL(import.meta.url).pathname), "..", "..");
const rendererDir = process.env.WORKMAX_BUNDLED_RENDERER_DIR
  ? path.resolve(process.env.WORKMAX_BUNDLED_RENDERER_DIR)
  : path.join(repoRoot, "desktop/renderer/en/desktop");
const rendererPath = path.join(rendererDir, "renderer.js");
const rendererSource = fs.readFileSync(rendererPath, "utf8");
const rendererHTML = fs.readFileSync(path.join(rendererDir, "index.html"), "utf8");

assert.match(rendererHTML, /id=["']source-code-link["']/u);
assert.match(rendererHTML, /href=["']https:\/\/github\.com\/jonnyquan\/workmax["']/u);
assert.match(rendererHTML, /Source code\s*·\s*AGPL-3\.0/u);

assert.doesNotMatch(rendererSource, /["']\/auth\/start["']/u);
assert.doesNotMatch(rendererSource, /["']\/auth\/login-transaction(?:\/password)?["']/u);
assert.doesNotMatch(rendererSource, /openOAuthWindow|authorize_url|auth_port|oauth\/callback/u);
assert.doesNotMatch(
  rendererSource,
  /\/api\/v1\/desktop\/identity\/login-transactions|transaction_secret|transactionSecret|exchange_token|exchangeToken|DesktopLogin|DesktopExchange|authorization_code|redirect_location/u
);
assert.doesNotMatch(rendererSource, /localStorage|sessionStorage|indexedDB|console\./u);
for (const method of [
  "beginLogin",
  "loginStatus",
  "submitLoginPassword",
  "cancelLogin",
]) {
  assert.match(rendererSource, new RegExp(`auth\\.${method}\\b`, "u"));
}
for (const id of [
  "login-form",
  "login-email",
  "login-password",
  "login-submit-button",
  "login-cancel-button",
  "new-thread-button",
  "new-thread-form",
  "new-thread-name",
  "new-thread-mode",
  "new-thread-error",
  "new-thread-submit-button",
  "new-thread-cancel-button",
  "empty-title",
  "empty-description",
  "empty-new-thread-button",
  "chat-form",
  "agent-mode",
  "composer-status",
  "chat-input",
  "stop-button",
  "send-button",
  "turn-state",
  "message-viewport",
  "turn-recovery-card",
  "turn-recovery-description",
  "turn-recovery-prompt",
  "turn-recovery-feedback",
  "turn-recovery-resume-button",
  "turn-recovery-dismiss-button",
]) {
  assert.match(rendererHTML, new RegExp(`id=["']${id}["']`, "u"));
}
assert.match(rendererHTML, /id=["']login-password["'][\s\S]*?type=["']password["']/u);

class FakeClassList {
  constructor() {
    this.values = new Set();
  }

  toggle(name, force) {
    if (force) {
      this.values.add(name);
    } else {
      this.values.delete(name);
    }
  }

  add(name) {
    this.values.add(name);
  }

  remove(name) {
    this.values.delete(name);
  }

  contains(name) {
    return this.values.has(name);
  }
}

class FakeElement {
  constructor(tagName = "div") {
    this.tagName = tagName.toUpperCase();
    this.children = [];
    this.attributes = new Map();
    this.listeners = new Map();
    this.classList = new FakeClassList();
    this.hidden = false;
    this._textContent = "";
    this._className = "";
    this.type = "";
    this.value = "";
    this.disabled = false;
    this.focused = false;
    this.parentNode = null;
    this.scrollTop = 0;
  }

  set className(value) {
    this._className = value;
    this.classList.values = new Set(String(value).split(/\s+/).filter(Boolean));
  }

  get className() {
    return this._className;
  }

  set textContent(value) {
    this._textContent = String(value);
    this.children = [];
  }

  get textContent() {
    return this._textContent + this.children.map((child) => child.textContent).join("");
  }

  set innerHTML(_value) {
    this.textContent = "";
  }

  append(...nodes) {
    for (const node of nodes) {
      this.appendChild(node);
    }
  }

  appendChild(node) {
    node.parentNode = this;
    this.children.push(node);
    return node;
  }

  remove() {
    if (!this.parentNode) return;
    this.parentNode.children = this.parentNode.children.filter(
      (child) => child !== this
    );
    this.parentNode = null;
  }

  addEventListener(type, handler) {
    this.listeners.set(type, handler);
  }

  click() {
    if (this.disabled) {
      return;
    }
    const handler = this.listeners.get("click");
    if (handler) {
      handler({ preventDefault() {} });
    }
  }

  submit() {
    const handler = this.listeners.get("submit");
    if (handler) {
      handler({ preventDefault() {} });
    }
  }

  dispatch(type, init = {}) {
    const handler = this.listeners.get(type);
    if (!handler) return;
    handler({
      key: init.key,
      metaKey: init.metaKey ?? false,
      ctrlKey: init.ctrlKey ?? false,
      repeat: init.repeat ?? false,
      preventDefault: init.preventDefault ?? (() => {}),
    });
  }

  get scrollHeight() {
    return this.children.length * 100;
  }

  focus() {
    this.focused = true;
  }

  select() {
    this.selected = true;
  }

  setAttribute(name, value) {
    this.attributes.set(name, String(value));
  }

  getAttribute(name) {
    return this.attributes.get(name) ?? null;
  }
}

// The element set is derived from index.html rather than listed here.
//
// It used to be a hardcoded array, and it drifted: twenty elements added by
// later milestones (model settings, attachments) were never added to the list,
// so renderer.js threw on a null element and this whole behavior suite has
// been failing rather than checking anything. Reading the real markup means
// the stub cannot fall behind what ships — a new element in index.html simply
// exists here too.
//
// Tag name and initial hidden state come from the markup for the same reason:
// guessing them from the id ("...-button" means a button) is another place for
// the two to disagree.
function parseRendererElements(html) {
  const elements = new Map();
  for (const match of html.matchAll(/<([a-zA-Z][a-zA-Z0-9-]*)\s([^>]*)>/gu)) {
    const [, tagName, attributes] = match;
    const idMatch = /\bid="([^"]+)"/u.exec(attributes);
    if (!idMatch) continue;
    elements.set(idMatch[1], {
      tagName: tagName.toLowerCase(),
      hidden: /\bhidden\b/u.test(attributes),
    });
  }
  if (elements.size === 0) {
    throw new Error("no id-bearing elements found in index.html; the parse is wrong, not the markup");
  }
  return elements;
}

class FakeDocument {
  constructor(rendererHtml) {
    this.byId = new Map();
    for (const [id, spec] of parseRendererElements(rendererHtml)) {
      const element = new FakeElement(spec.tagName);
      element.hidden = spec.hidden;
      this.byId.set(id, element);
    }
  }

  querySelector(selector) {
    if (!selector.startsWith("#")) {
      throw new Error(`unsupported selector in bundled renderer test: ${selector}`);
    }
    return this.byId.get(selector.slice(1)) ?? null;
  }

  createElement(tagName) {
    return new FakeElement(tagName);
  }

  // Text nodes are how the Markdown renderer puts model output on the page —
  // never as markup. The stub keeps them distinguishable from elements so a
  // test can assert that a would-be tag really did arrive as text.
  createTextNode(data) {
    return {
      nodeType: 3,
      tagName: "#text",
      children: [],
      classList: new FakeClassList(),
      parentNode: null,
      textContent: String(data),
    };
  }
}

function response(body, init = {}) {
  return {
    ok: init.ok ?? true,
    status: init.status ?? 200,
    async json() {
      return body;
    },
  };
}

function typedSuccess(data, status = 200) {
  return {
    ok: true,
    status,
    statusText: status === 200 ? "OK" : "Success",
    headers: { "content-type": "application/json" },
    data,
  };
}

function typedFailure(status, error) {
  return {
    ok: false,
    status,
    statusText: status === 409 ? "Conflict" : "Error",
    headers: { "content-type": "application/json" },
    error,
  };
}

function pptCatalog() {
  return {
    items: [
      {
        agentMode: "ppt",
        name: "Presentation",
        description: "Create and refine presentation decks.",
        version: "2.1.0",
        hasQuestionForm: true,
        hasDirectionsFallback: true,
        hasPostScripts: true,
        labelKey: "skills.ppt.name",
        descriptionKey: "skills.ppt.description",
      },
    ],
    count: 1,
    allowed_modes: ["ppt"],
  };
}

function pptCatalogWithReplayMode() {
  const catalog = pptCatalog();
  catalog.items.push({
    ...catalog.items[0],
    agentMode: "ppt_revised",
    name: "Presentation revised",
    labelKey: "skills.ppt.revised.name",
    descriptionKey: "skills.ppt.revised.description",
  });
  catalog.count = 2;
  catalog.allowed_modes.push("ppt_revised");
  return catalog;
}

function thread(uuid, name, messageCount = 1) {
  return {
    uuid,
    name,
    agent_mode: "ppt",
    message_count: messageCount,
    updated_at: "2026-05-21T00:00:00Z",
  };
}

function createdThread(uuid, name, agentMode = "ppt") {
  return {
    uuid,
    name,
    agent_mode: agentMode,
    message_count: 0,
    updated_at: "2026-08-06T00:00:00Z",
    cloud_sync_state: "synced",
  };
}

function recoverableTurn(overrides = {}) {
  return {
    turn_uuid: "123e4567-e89b-42d3-a456-426614174000",
    thread_uuid: "thread-agent",
    user_text: "  Resume the quarterly deck  ",
    chat_mode: "ppt",
    state: "interrupted",
    last_error_kind: "transport_error",
    updated_at: "2026-08-06T00:00:00Z",
    ...overrides,
  };
}

function message(uuid, userText, aiText, streamingState = "complete") {
  return {
    uuid,
    user_text: userText,
    ai_text: aiText,
    streaming_state: streamingState,
    created_at: "2026-05-21T00:00:00Z",
    updated_at: "2026-05-21T00:00:00Z",
  };
}

async function settle() {
  await new Promise((resolve) => setImmediate(resolve));
  await new Promise((resolve) => setImmediate(resolve));
}

function walk(node, predicate, out = []) {
  if (predicate(node)) {
    out.push(node);
  }
  for (const child of node.children ?? []) {
    walk(child, predicate, out);
  }
  return out;
}

async function runRenderer(mockBridge, mockDesktopBridge) {
  const document = new FakeDocument(rendererHTML);
  let uuidSequence = 0;
  const crypto = {
    randomUUID() {
      uuidSequence += 1;
      return `00000000-0000-4000-8000-${String(uuidSequence).padStart(12, "0")}`;
    },
  };
  const context = {
    console,
    crypto,
    Date,
    URL,
    document,
    setTimeout: (handler) => setImmediate(handler),
    clearTimeout: () => {},
    window: {
      workmaxLocal: mockBridge,
      desktopBridge: mockDesktopBridge,
    },
  };
  vm.createContext(context);
  vm.runInContext(rendererSource, context, { filename: rendererPath });
  await settle();
  return { context, document };
}

async function testMissingBridge() {
  const { document } = await runRenderer(undefined);
  assert.match(
    document.byId.get("status-card").textContent,
    /must run inside WorkMax Desktop/
  );
  assert.equal(document.byId.get("status-card").classList.contains("error"), true);
}

async function testAuthenticatedCacheRead() {
  const calls = [];
  const bridge = {
    sidecarVersion: "sidecar-test",
    appVersion: "app-test",
    async fetch(pathname) {
      calls.push(pathname);
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", tier: "pro", updated_at: "2026-05-21T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=false") {
        return response({
          items: [
            {
              uuid: "thread one",
              name: "Storyboard draft",
              agent_mode: "ppt",
              message_count: 2,
              updated_at: "2026-05-21T00:00:00Z",
            },
          ],
        });
      }
      if (pathname === "/agent/threads/thread%20one/messages") {
        return response({
          items: [
            {
              uuid: "msg one",
              user_text: "make a shot list",
              ai_text: "cached assistant answer",
              streaming_state: "complete",
              created_at: "2026-05-21T00:00:00Z",
              updated_at: "2026-05-21T00:00:00Z",
            },
          ],
        });
      }
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };

  const { document } = await runRenderer(bridge);
  assert.deepEqual(calls, ["/auth/status", "/agent/threads?include_paused=false"]);
  assert.match(document.byId.get("runtime-label").textContent, /sidecar sidecar-test · app app-test/);
  assert.equal(document.byId.get("login-button").hidden, true);
  assert.match(document.byId.get("thread-list").textContent, /Storyboard draft/);

  const threadButton = walk(
    document.byId.get("thread-list"),
    (node) => node.tagName === "BUTTON"
  )[0];
  assert.ok(threadButton, "expected a thread button");
  threadButton.click();
  await settle();

  assert.equal(calls.at(-1), "/agent/threads/thread%20one/messages");
  assert.equal(document.byId.get("empty-state").hidden, true);
  assert.equal(document.byId.get("thread-panel").hidden, false);
  assert.match(document.byId.get("message-list").textContent, /make a shot list/);
  assert.match(document.byId.get("message-list").textContent, /cached assistant answer/);
  assert.equal(document.byId.get("chat-input").disabled, true);
  assert.equal(document.byId.get("send-button").disabled, true);
  assert.match(document.byId.get("composer-status").textContent, /streaming is unavailable/i);
}

async function testUnauthenticatedLogin() {
  const calls = [];
  const beginCalls = [];
  const statusCalls = [];
  const passwordCalls = [];
  let authenticated = false;
  let rendererDocument;
  const bridge = {
    async fetch(pathname, init = {}) {
      calls.push([pathname, init.method ?? "GET"]);
      if (pathname === "/auth/status") {
        return response({
          state: authenticated ? "authenticated" : "unauthenticated",
          tier: authenticated ? "pro" : undefined,
          updated_at: "2026-05-21T00:00:00Z",
        });
      }
      if (pathname === "/agent/threads?include_paused=false") {
        return response({ items: [] });
      }
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    auth: {
      async beginLogin(...args) {
        beginCalls.push(args);
        return { state: "awaiting_password" };
      },
      async loginStatus(...args) {
        statusCalls.push(args);
        return { state: "idle" };
      },
      async submitLoginPassword(input) {
        assert.equal(
          rendererDocument.byId.get("login-password").value,
          "",
          "password DOM value must be cleared before the privileged IPC begins"
        );
        passwordCalls.push(input);
        authenticated = true;
        return { state: "authenticated" };
      },
      async cancelLogin() {
        throw new Error("cancelLogin should not be called during successful sign-in");
      },
    },
  };

  const { context, document } = await runRenderer(bridge, desktopBridge);
  rendererDocument = document;
  assert.equal(document.byId.get("login-button").hidden, false);
  assert.equal(document.byId.get("login-form").hidden, true);
  assert.match(document.byId.get("status-card").textContent, /Auth state: unauthenticated/);
  assert.deepEqual(statusCalls, [[]]);

  document.byId.get("login-button").click();
  await settle();
  assert.deepEqual(beginCalls, [[]]);
  assert.equal(document.byId.get("login-button").hidden, true);
  assert.equal(document.byId.get("login-form").hidden, false);
  assert.match(document.byId.get("status-card").textContent, /email and password/i);

  document.byId.get("login-email").value = "  writer@example.com  ";
  document.byId.get("login-password").value = "do-not-persist-this";
  document.byId.get("login-form").submit();
  assert.equal(document.byId.get("login-password").value, "");
  await settle();
  await settle();

  assert.equal(passwordCalls.length, 1);
  assert.equal(passwordCalls[0].email, "writer@example.com");
  assert.equal(passwordCalls[0].password, "do-not-persist-this");
  assert.equal(document.byId.get("login-password").value, "");
  assert.doesNotMatch(
    JSON.stringify(vm.runInContext("state", context)),
    /writer@example\.com|do-not-persist-this/u
  );
  assert.equal(document.byId.get("login-form").hidden, true);
  assert.equal(calls.some(([pathname]) => pathname.startsWith("/auth/login-transaction")), false);
  assert.deepEqual(calls.at(-1), ["/agent/threads?include_paused=false", "GET"]);
  assert.match(document.byId.get("status-card").textContent, /Authenticated/);
}

async function testResumesAndCancelsPasswordLogin() {
  let cancelCalls = 0;
  let rendererDocument;
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "unauthenticated", updated_at: "2026-05-21T00:00:00Z" });
      }
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    auth: {
      async beginLogin() {
        throw new Error("beginLogin should not replace the resumable transaction");
      },
      async loginStatus() {
        return { state: "awaiting_password" };
      },
      async submitLoginPassword() {
        throw new Error("submitLoginPassword should not run in the cancellation test");
      },
      async cancelLogin() {
        cancelCalls += 1;
        assert.equal(rendererDocument.byId.get("login-password").value, "");
        return { state: "idle" };
      },
    },
  };

  const { document } = await runRenderer(bridge, desktopBridge);
  rendererDocument = document;
  assert.equal(document.byId.get("login-form").hidden, false);
  document.byId.get("login-password").value = "must-be-cleared-on-cancel";
  document.byId.get("login-cancel-button").click();
  await settle();

  assert.equal(cancelCalls, 1);
  assert.equal(document.byId.get("login-password").value, "");
  assert.equal(document.byId.get("login-form").hidden, true);
  assert.match(document.byId.get("status-card").textContent, /Sign-in was canceled/);
}

async function testInvalidCredentialsStayRetryableAndClearPassword() {
  let passwordCalls = 0;
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "unauthenticated", updated_at: "2026-05-21T00:00:00Z" });
      }
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    auth: {
      async beginLogin() {
        return { state: "awaiting_password" };
      },
      async loginStatus() {
        return { state: "idle" };
      },
      async submitLoginPassword() {
        passwordCalls += 1;
        return { state: "awaiting_password", error: "invalid_credentials" };
      },
      async cancelLogin() {
        return { state: "idle" };
      },
    },
  };

  const { document } = await runRenderer(bridge, desktopBridge);
  document.byId.get("login-button").click();
  await settle();
  document.byId.get("login-email").value = "writer@example.com";
  document.byId.get("login-password").value = "wrong-password";
  document.byId.get("login-form").submit();
  await settle();

  assert.equal(passwordCalls, 1);
  assert.equal(document.byId.get("login-form").hidden, false);
  assert.equal(document.byId.get("login-password").value, "");
  assert.equal(document.byId.get("login-submit-button").disabled, false);
  assert.match(document.byId.get("status-card").textContent, /email or password is incorrect/i);
  assert.doesNotMatch(document.byId.get("status-card").textContent, /invalid_credentials/u);
}

async function testCancelFencesLatePasswordCompletion() {
  let resolvePasswordSubmission;
  const pendingPasswordSubmission = new Promise((resolve) => {
    resolvePasswordSubmission = resolve;
  });
  const calls = [];
  const bridge = {
    async fetch(pathname) {
      calls.push(pathname);
      if (pathname === "/auth/status") {
        return response({ state: "unauthenticated", updated_at: "2026-05-21T00:00:00Z" });
      }
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    auth: {
      async beginLogin() {
        return { state: "awaiting_password" };
      },
      async loginStatus() {
        return { state: "idle" };
      },
      async submitLoginPassword() {
        return pendingPasswordSubmission;
      },
      async cancelLogin() {
        return { state: "idle" };
      },
    },
  };

  const { document } = await runRenderer(bridge, desktopBridge);
  document.byId.get("login-button").click();
  await settle();
  document.byId.get("login-email").value = "writer@example.com";
  document.byId.get("login-password").value = "late-password";
  document.byId.get("login-form").submit();
  document.byId.get("login-cancel-button").click();
  await settle();

  resolvePasswordSubmission({ state: "authenticated" });
  await settle();
  assert.equal(document.byId.get("login-password").value, "");
  assert.equal(document.byId.get("login-form").hidden, true);
  assert.match(document.byId.get("status-card").textContent, /Sign-in was canceled/);
  assert.deepEqual(calls, ["/auth/status"]);
}

async function testAmbiguousPasswordResponseReconcilesSessionWithoutReplay() {
  const calls = [];
  let authenticated = false;
  let passwordCalls = 0;
  const bridge = {
    async fetch(pathname) {
      calls.push(pathname);
      if (pathname === "/auth/status") {
        return response({
          state: authenticated ? "authenticated" : "unauthenticated",
          tier: authenticated ? "pro" : undefined,
          updated_at: "2026-05-21T00:00:00Z",
        });
      }
      if (pathname === "/agent/threads?include_paused=false") {
        return response({ items: [] });
      }
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    auth: {
      async beginLogin() {
        return { state: "awaiting_password" };
      },
      async loginStatus() {
        return { state: "idle" };
      },
      async submitLoginPassword() {
        passwordCalls += 1;
        authenticated = true;
        // Electron Main collapses a lost/malformed Sidecar response to this
        // closed result instead of exposing transport details to Renderer.
        return { state: "idle", error: "unavailable" };
      },
      async cancelLogin() {
        return { state: "idle" };
      },
    },
  };

  const { document } = await runRenderer(bridge, desktopBridge);
  document.byId.get("login-button").click();
  await settle();
  document.byId.get("login-email").value = "writer@example.com";
  document.byId.get("login-password").value = "ambiguous-password";
  document.byId.get("login-form").submit();
  await settle();
  await settle();

  assert.equal(passwordCalls, 1, "an ambiguous password result must never be replayed");
  assert.equal(document.byId.get("login-password").value, "");
  assert.equal(document.byId.get("login-form").hidden, true);
  assert.match(document.byId.get("status-card").textContent, /Authenticated/);
  assert.deepEqual(calls, [
    "/auth/status",
    "/auth/status",
    "/agent/threads?include_paused=false",
  ]);
}

async function testRejectsMalformedAuthStatus() {
  for (const payload of [
    { state: "admin", tier: "pro", updated_at: "2026-05-21T00:00:00Z" },
    { state: "authenticated", tier: "pro" },
    { state: "authenticated", tier: 123, updated_at: "2026-05-21T00:00:00Z" },
    { state: "authenticated", user_id: 123, updated_at: "2026-05-21T00:00:00Z" },
  ]) {
    const bridge = {
      async fetch(pathname) {
        if (pathname === "/auth/status") {
          return response(payload);
        }
        throw new Error(`unexpected fetch path ${pathname}`);
      },
    };

    const { document } = await runRenderer(bridge);
    assert.match(document.byId.get("status-card").textContent, /Malformed \/auth\/status response/);
    assert.equal(document.byId.get("status-card").classList.contains("error"), true);
  }
}

async function testRejectsMalformedThreadList() {
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", tier: "pro", updated_at: "2026-05-21T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=false") {
        return response({
          items: [
            {
              uuid: " bad-thread",
              name: "Bad thread",
              agent_mode: "ppt",
              message_count: 0,
              updated_at: "2026-05-21T00:00:00Z",
            },
          ],
        });
      }
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };

  const { document } = await runRenderer(bridge);
  assert.match(document.byId.get("status-card").textContent, /Malformed \/agent\/threads response/);
  assert.equal(document.byId.get("status-card").classList.contains("error"), true);
}

async function testRejectsMalformedThreadCountAndTimestamp() {
  for (const item of [
    {
      uuid: "thread one",
      name: "Bad count",
      agent_mode: "ppt",
      message_count: -1,
      updated_at: "2026-05-21T00:00:00Z",
    },
    {
      uuid: "thread one",
      name: "Bad timestamp",
      agent_mode: "ppt",
      message_count: 1,
      updated_at: "not-a-date",
    },
  ]) {
    const bridge = {
      async fetch(pathname) {
        if (pathname === "/auth/status") {
          return response({ state: "authenticated", tier: "pro", updated_at: "2026-05-21T00:00:00Z" });
        }
        if (pathname === "/agent/threads?include_paused=false") {
          return response({ items: [item] });
        }
        throw new Error(`unexpected fetch path ${pathname}`);
      },
    };

    const { document } = await runRenderer(bridge);
    assert.match(document.byId.get("status-card").textContent, /Malformed \/agent\/threads response/);
    assert.equal(document.byId.get("status-card").classList.contains("error"), true);
  }
}

async function testRejectsMalformedMessages() {
  const calls = [];
  const bridge = {
    async fetch(pathname) {
      calls.push(pathname);
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", tier: "pro", updated_at: "2026-05-21T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=false") {
        return response({
          items: [
            {
              uuid: "thread one",
              name: "Storyboard draft",
              agent_mode: "ppt",
              message_count: 1,
              updated_at: "2026-05-21T00:00:00Z",
            },
          ],
        });
      }
      if (pathname === "/agent/threads/thread%20one/messages") {
        return response({
          items: [
            {
              uuid: "msg-one\n",
              user_text: "make a shot list",
              ai_text: "cached assistant answer",
              streaming_state: "complete",
              created_at: "2026-05-21T00:00:00Z",
              updated_at: "2026-05-21T00:00:00Z",
            },
          ],
        });
      }
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };

  const { document } = await runRenderer(bridge);
  const threadButton = walk(
    document.byId.get("thread-list"),
    (node) => node.tagName === "BUTTON"
  )[0];
  assert.ok(threadButton, "expected a thread button");
  threadButton.click();
  await settle();

  assert.equal(calls.at(-1), "/agent/threads/thread%20one/messages");
  assert.match(
    document.byId.get("status-card").textContent,
    /Malformed \/agent\/threads\/:uuid\/messages response/
  );
  assert.equal(document.byId.get("status-card").classList.contains("error"), true);
}

async function testRejectsMalformedMessageTimestamps() {
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", tier: "pro", updated_at: "2026-05-21T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=false") {
        return response({
          items: [
            {
              uuid: "thread one",
              name: "Storyboard draft",
              agent_mode: "ppt",
              message_count: 1,
              updated_at: "2026-05-21T00:00:00Z",
            },
          ],
        });
      }
      if (pathname === "/agent/threads/thread%20one/messages") {
        return response({
          items: [
            {
              uuid: "msg-one",
              user_text: "make a shot list",
              ai_text: "cached assistant answer",
              streaming_state: "complete",
              created_at: "bad-date",
              updated_at: "2026-05-21T00:00:00Z",
            },
          ],
        });
      }
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };

  const { document } = await runRenderer(bridge);
  const threadButton = walk(
    document.byId.get("thread-list"),
    (node) => node.tagName === "BUTTON"
  )[0];
  assert.ok(threadButton, "expected a thread button");
  threadButton.click();
  await settle();

  assert.match(
    document.byId.get("status-card").textContent,
    /Malformed \/agent\/threads\/:uuid\/messages response/
  );
  assert.equal(document.byId.get("status-card").classList.contains("error"), true);
}

async function testRejectsMalformedLoginTransactionResult() {
  for (const payload of [
    { state: "pending" },
    { state: "awaiting_password", error: "private-error-must-not-cross" },
    {
      state: "awaiting_password",
      redirect_location: "private-location-must-not-cross",
    },
    { state: "idle", error: "canceled", extra: "private-extra-must-not-cross" },
  ]) {
    const calls = [];
    const bridge = {
      async fetch(pathname) {
        calls.push(pathname);
        if (pathname === "/auth/status") {
          return response({ state: "unauthenticated", updated_at: "2026-05-21T00:00:00Z" });
        }
        throw new Error(`unexpected fetch path ${pathname}`);
      },
    };
    const desktopBridge = {
      auth: {
        async beginLogin() {
          return payload;
        },
        async loginStatus() {
          return { state: "idle" };
        },
        async submitLoginPassword() {
          throw new Error("submitLoginPassword should not be called for malformed begin result");
        },
        async cancelLogin() {
          throw new Error("cancelLogin should not be called for malformed begin result");
        },
      },
    };

    const { document } = await runRenderer(bridge, desktopBridge);
    document.byId.get("login-button").click();
    await settle();

    assert.match(document.byId.get("status-card").textContent, /temporarily unavailable/i);
    assert.doesNotMatch(
      document.byId.get("status-card").textContent,
      /private-error|private-location|private-extra/u
    );
    assert.equal(document.byId.get("status-card").classList.contains("error"), true);
    assert.deepEqual(calls, ["/auth/status"]);
  }
}

async function testRedactsErrorStatusMessages() {
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        throw new Error(
          "Authorization: Bearer bearer-secret Basic bare-basic-secret https://user:pass@example.com/path?refresh_token=refresh-secret X-Local-Token=local-secret client_secret=client-secret password=password-secret apikey=api-secret secret=generic-secret"
        );
      }
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };

  const { document } = await runRenderer(bridge);
  const status = document.byId.get("status-card").textContent;
  assert.match(status, /\[REDACTED\]/);
  assert.match(status, /Basic \[REDACTED\]/);
  for (const secret of [
    "bearer-secret",
    "bare-basic-secret",
    "user:pass",
    "refresh-secret",
    "local-secret",
    "client-secret",
    "password-secret",
    "api-secret",
    "generic-secret",
  ]) {
    assert.doesNotMatch(status, new RegExp(secret.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")));
  }
  assert.equal(document.byId.get("status-card").classList.contains("error"), true);
}

async function testCachedStreamingStatesRenderPartialAndRejectUnknown() {
  const validBridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", updated_at: "2026-05-21T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=false") {
        return response({ items: [thread("partial-thread", "Recovered responses", 2)] });
      }
      if (pathname === "/agent/threads/partial-thread/messages") {
        return response({
          items: [
            message("partial-message", "Partial prompt", "Interrupted answer", "partial"),
            message("streaming-message", "Streaming prompt", "", "streaming"),
          ],
        });
      }
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };

  const { document } = await runRenderer(validBridge);
  walk(document.byId.get("thread-list"), (node) => node.tagName === "BUTTON")[0].click();
  await settle();
  const partialAssistants = walk(
    document.byId.get("message-list"),
    (node) =>
      node.tagName === "ARTICLE" &&
      node.classList.contains("assistant") &&
      node.classList.contains("partial")
  );
  assert.equal(partialAssistants.length, 2);
  assert.match(document.byId.get("message-list").textContent, /Interrupted answer/);
  assert.match(document.byId.get("message-list").textContent, /Response interrupted/);

  const invalidBridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", updated_at: "2026-05-21T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=false") {
        return response({ items: [thread("invalid-state-thread", "Invalid state")] });
      }
      if (pathname === "/agent/threads/invalid-state-thread/messages") {
        return response({
          items: [message("invalid-state-message", "Prompt", "Answer", "private")],
        });
      }
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const invalid = await runRenderer(invalidBridge);
  walk(
    invalid.document.byId.get("thread-list"),
    (node) => node.tagName === "BUTTON"
  )[0].click();
  await settle();
  assert.match(
    invalid.document.byId.get("status-card").textContent,
    /Malformed \/agent\/threads\/:uuid\/messages response/
  );
  assert.doesNotMatch(invalid.document.byId.get("message-list").textContent, /Answer/);
}

// A turn must carry the attachments the user staged. This regression is the
// reason worth having: the tray was cleared before startTurn read it, so
// fileIDs was always empty and every upload was silently dropped — the chips
// appeared, the upload succeeded, and the model never saw the file.
// The shim's external-link interception, which has no other home.
//
// It cannot be checked in a real webview: ExecJS queues until the Wails
// runtime reports itself loaded, and on a loopback origin it never does. So
// the shim is loaded here against a minimal DOM and a click is dispatched
// directly — which is also the better place for it, because this runs in CI.
//
// What must hold: the default action is prevented (otherwise the webview
// navigates and the app is replaced by a remote page, with no way back on a
// shell that has no cancellable navigation hook), and the URL is handed to Go
// rather than opened here.
// The task context panel must paint on load, not on the first interaction.
//
// It sits after the bootstrap call in renderer.js, so an initial render is not
// implicit — and a panel that never ran is indistinguishable from one that ran
// and found nothing, because index.html ships plausible-looking static values.
// Conversation grouping and search.
//
// Grouping is by local calendar day, not elapsed hours — the case that makes
// the difference is a conversation from late last night read at 1am: "3 hours
// ago" is true and useless, "Yesterday" is what someone is looking for. The
// two other rules are equally deliberate: a timestamp that will not parse goes
// to Older rather than vanishing, and a future one stays in Today.
async function testThreadGroupingAndSearch() {
  // Built from LOCAL date components, not UTC strings. The grouping is by
  // local calendar day, so a fixture written in UTC would pass or fail
  // depending on the machine's timezone — which is how the first version of
  // this test failed: "23:30 UTC yesterday" is this morning in UTC+8, and the
  // code was right to call it today.
  const local = (y, m, d, h = 9) => new Date(y, m - 1, d, h, 0, 0).toISOString();
  const now = new Date(2026, 7, 8, 10, 0, 0);
  const threads = [
    { uuid: "t-today", name: "Quarterly sales", updatedAt: local(2026, 8, 8, 1) },
    { uuid: "t-lastnight", name: "Late night", updatedAt: local(2026, 8, 7, 23) },
    { uuid: "t-week", name: "Pipeline notes", updatedAt: local(2026, 8, 3) },
    { uuid: "t-old", name: "Archived plan", updatedAt: local(2026, 6, 1) },
    { uuid: "t-broken", name: "Broken stamp", updatedAt: "not-a-date" },
    { uuid: "t-future", name: "Clock skew", updatedAt: local(2026, 8, 9) },
  ];

  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", updated_at: "2026-08-08T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=false") {
        // Built through the shared helper so the fixture satisfies the same
        // strict parser the renderer applies; only the timestamp is varied.
        // The malformed row is deliberately NOT served here: parseThread
        // rejects an unparseable updated_at and parseThreadList maps over
        // every item, so one bad row empties the entire sidebar. The grouping
        // function's own handling of it is asserted directly below instead.
        return response({
          items: threads
            .filter((t) => t.uuid !== "t-broken")
            .map((t) => ({ ...thread(t.uuid, t.name), updated_at: t.updatedAt })),
        });
      }
      if (pathname.startsWith("/agent/threads/")) return response({ items: [] });
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async uploadThreadFile() { throw new Error("not exercised"); },
      async listSkills() { return typedSuccess(pptCatalog()); },
      startTurn() { return { turnID: "unused" }; },
      async cancelTurn(turnID) { return { turnID, canceled: true }; },
    },
  };

  const { context, document } = await runRenderer(bridge, desktopBridge);

  const groups = context.groupThreads(
    threads.map((t) => ({ ...t, updated_at: t.updatedAt })),
    now,
  );
  // Array.from because these crossed a VM realm boundary: same contents,
  // different Array prototype, and deepStrictEqual cares.
  const ids = (bucket) => Array.from(bucket).map((t) => t.uuid).sort();
  assert.deepEqual(
    ids(groups.today),
    ["t-future", "t-today"],
    "today holds the same calendar day, and a future timestamp stays there rather than forming its own bucket",
  );
  assert.deepEqual(
    ids(groups.week),
    ["t-lastnight", "t-week"],
    "late last night belongs to the previous week bucket by calendar day, not to today by elapsed hours",
  );
  assert.deepEqual(
    ids(groups.older),
    ["t-broken", "t-old"],
    "an unparseable timestamp is kept in Older; dropping it would hide a real conversation",
  );

  // Group headings appear only for buckets that have something in them.
  const headings = Array.from(document.byId.get("thread-list").children).filter(
    (node) => node.classList?.contains("thread-group"),
  );
  assert.ok(headings.length >= 2, "populated groups must be labelled");

  // Driven through the input rather than by poking state: that is the path a
  // user takes, and top-level consts in a VM script are not reachable from the
  // context object anyway.
  const search = document.byId.get("thread-search");
  search.value = "quarterly";
  search.dispatch("input");
  const shown = Array.from(document.byId.get("thread-list").children)
    .filter((n) => n.classList?.contains("thread-item"));
  assert.equal(shown.length, 1, "search must narrow the list to matching titles");

  search.value = "nothing-matches-this";
  search.dispatch("input");
  assert.match(
    document.byId.get("thread-list").textContent,
    /No conversations match/,
    "an empty result must say the query found nothing, not look like an empty cache",
  );
  assert.equal(
    document.byId.get("thread-search-panel").hidden,
    false,
    "a query that matches nothing must keep its own input on screen — hiding it would strand the text the user typed",
  );
}

// The counterpart to the assertion above: with nothing cached, the filter is
// not just useless, it is misleading — an empty list under a search box reads
// as "your search found nothing".
async function testThreadSearchIsHiddenWithNothingToFilter() {
  // A signed-in session whose thread list comes back empty, rather than the
  // missing-bridge run: this has to prove renderThreads decided to hide the
  // filter, not merely that index.html ships it hidden.
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", updated_at: "2026-08-08T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=false") return response({ items: [] });
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async listSkills() { return typedSuccess(pptCatalog()); },
    },
  };
  const { document } = await runRenderer(bridge, desktopBridge);
  assert.match(
    document.byId.get("thread-list").textContent,
    /No cached threads yet/,
    "precondition: this run must have an empty thread list",
  );
  assert.equal(
    document.byId.get("thread-search-panel").hidden,
    true,
    "the conversation filter must stay hidden until there is something to filter",
  );
}

async function testTaskContextPanelRendersOnLoad() {
  const { document } = await runRenderer(undefined);
  const steps = document.byId.get("run-overview-list");
  assert.ok(steps, "the run overview list must exist");
  assert.equal(
    steps.children.length,
    4,
    "all four run-overview steps must be rendered on load; static markup would leave this empty",
  );
  assert.match(
    document.byId.get("run-overview-meta").textContent,
    /^\d\/4$/,
    "the step counter must be computed, not left at its markup default",
  );
  assert.equal(
    document.byId.get("sources-empty").hidden,
    false,
    "with no sources the empty note must be visible",
  );
  assert.equal(document.byId.get("sources-meta").textContent, "0");
}

async function testShimInterceptsExternalLinks() {
  const shimPath = path.join(rendererDir, "shim.js");
  const shimSource = fs.readFileSync(shimPath, "utf8");

  const listeners = [];
  const fetches = [];
  let prevented = false;

  const anchor = {
    tagName: "A",
    attrs: new Map(),
    getAttribute(name) { return this.attrs.get(name) ?? null; },
    closest(sel) { return sel === "a[href]" && this.attrs.has("href") ? this : null; },
  };

  const context = {
    console,
    URL,
    Headers,
    TextEncoder,
    crypto: { randomUUID: () => "00000000-0000-4000-8000-000000000001" },
    fetch: async (url, init) => {
      fetches.push({ url: String(url), init });
      return { ok: true, status: 200, statusText: "OK", url: String(url), headers: new Map(),
               text: async () => "{}", body: null };
    },
    setTimeout, clearTimeout,
    location: { origin: "http://127.0.0.1:5000" },
    document: {
      baseURI: "http://127.0.0.1:5000/CAPABILITY/",
      documentElement: { dataset: {} },
      addEventListener(type, handler, capture) { listeners.push({ type, handler, capture }); },
    },
  };
  context.window = context;
  // The shim stands down if a bridge is already installed, and needs the
  // generated library present; a stub is enough for the click path.
  context.window.__workmaxDesktopBridge = { createDesktopBridge: () => ({}) };
  vm.createContext(context);
  vm.runInContext(shimSource, context, { filename: shimPath });

  const click = listeners.find((l) => l.type === "click");
  assert.ok(click, "shim.js must register a click listener");
  assert.equal(click.capture, true,
    "the listener must be in the capture phase, or a handler that stops propagation skips it");

  // An external link: prevented and handed to Go.
  anchor.attrs.set("href", "https://github.com/jonnyquan/workmax");
  click.handler({ target: anchor, preventDefault() { prevented = true; } });
  await settle();
  assert.equal(prevented, true, "an external link must not be allowed to navigate the window");
  assert.equal(fetches.length, 1, "the URL must be handed to Go exactly once");
  assert.match(fetches[0].url, /\/CAPABILITY\/open-external$/,
    "posted to the capability-scoped open-external endpoint");
  assert.equal(JSON.parse(fetches[0].init.body).url, "https://github.com/jonnyquan/workmax");

  // A same-origin link is left alone: intercepting it would break in-app
  // navigation for no benefit.
  fetches.length = 0;
  prevented = false;
  anchor.attrs.set("href", "./index.html");
  click.handler({ target: anchor, preventDefault() { prevented = true; } });
  await settle();
  assert.equal(prevented, false, "a same-origin link must be left to the page");
  assert.equal(fetches.length, 0, "a same-origin link must not be sent to the system browser");

  // An in-page anchor likewise.
  anchor.attrs.set("href", "#section");
  click.handler({ target: anchor, preventDefault() { prevented = true; } });
  await settle();
  assert.equal(prevented, false, "a fragment link must be left to the page");
}

// Markdown is what models write, so it is what the chat column has to read
// back. This drives the real path: a turn streams Markdown, finishes, and the
// bubble is asserted structurally — elements, not a string that happens to
// contain the right characters.
async function testAssistantMarkdownIsRenderedAsElements() {
  let emit = null;
  const answer = [
    "## Findings",
    "",
    "Revenue is **up** and costs are `flat`.",
    "",
    "- first point",
    "- second point",
    "",
    "```sql",
    "SELECT 1;",
    "```",
    "",
    "See [the plan](https://example.com/plan) or [this](javascript:alert(1)).",
    "",
    "> quoted remark",
    "",
    "Not a tag: <img src=x onerror=alert(1)>",
  ].join("\n");

  // The turn is reconciled against the sidecar once it completes, so the
  // bubble that ends up on screen is the one rendered from cached history —
  // which is the path a reopened thread takes too. Serving the answer back
  // here means this test covers both.
  let answered = false;
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", updated_at: "2026-05-21T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=false") {
        return response({ items: [thread("md-thread", "Markdown")] });
      }
      if (pathname === "/agent/threads/md-thread/messages") {
        return response({
          items: answered
            ? [{
                uuid: "md-msg",
                user_text: "Summarise",
                ai_text: answer,
                streaming_state: "complete",
                created_at: "2026-05-21T00:00:00Z",
                updated_at: "2026-05-21T00:00:00Z",
              }]
            : [],
        });
      }
      if (pathname.startsWith("/agent/threads/")) return response({ items: [] });
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async uploadThreadFile() { throw new Error("not exercised"); },
      async listSkills() { return typedSuccess(pptCatalog()); },
      startTurn(input, callback) {
        emit = (event) => callback({ ...event, turnID: "md-turn" });
        return { turnID: "md-turn" };
      },
      async cancelTurn(turnID) { return { turnID, canceled: true }; },
    },
  };

  const { document } = await runRenderer(bridge, desktopBridge);
  walk(document.byId.get("thread-list"), (node) => node.tagName === "BUTTON")[0].click();
  await settle();

  const input = document.byId.get("chat-input");
  input.value = "Summarise";
  input.dispatch("input");
  document.byId.get("chat-form").submit();
  await settle();

  emit({ type: "text_delta", delta: answer });
  await settle();

  const streaming = walk(
    document.byId.get("message-list"),
    (n) => n.classList?.contains("bubble") && n.parentNode?.classList?.contains("assistant"),
  ).at(-1);
  assert.equal(
    streaming.classList.contains("markdown"),
    false,
    "while streaming the bubble must stay raw text; formatting a half-written block would make it change shape as it arrives",
  );

  answered = true;
  emit({ type: "done", result: { code: "", subtype: "", is_error: false } });
  await settle();

  const bubble = walk(
    document.byId.get("message-list"),
    (n) => n.classList?.contains("bubble") && n.parentNode?.classList?.contains("assistant"),
  ).at(-1);
  assert.ok(bubble, "the reconciled thread must still show the assistant's answer");
  assert.equal(bubble.classList.contains("markdown"), true, "the finished answer must be formatted");

  const tags = (name) => walk(bubble, (n) => n.tagName === name);
  // Heading levels are clamped so model output cannot outrank the app's own.
  assert.equal(tags("H5").length, 1, "'##' must become a heading, clamped below the app's headings");
  assert.equal(tags("H1").length + tags("H2").length + tags("H3").length, 0);
  assert.equal(tags("STRONG").length, 1, "**up** must be emphasis, not literal asterisks");
  assert.equal(tags("UL").length, 1);
  assert.equal(tags("LI").length, 2);
  assert.equal(tags("BLOCKQUOTE").length, 1);

  const pre = tags("PRE");
  assert.equal(pre.length, 1, "a fenced block must become a code block");
  assert.equal(pre[0].textContent, "SELECT 1;", "the code must be the code, without the fence");
  assert.equal(walk(pre[0], (n) => n.tagName === "CODE")[0].className, "language-sql");

  const links = tags("A");
  assert.equal(links.length, 1, "only the http link may become an anchor");
  assert.equal(links[0].getAttribute("href"), "https://example.com/plan");
  assert.match(
    bubble.textContent,
    /\[this\]\(javascript:alert\(1\)\)/,
    "a javascript: link must be shown as the literal text the model wrote, not offered as a link",
  );

  // The security property, stated as a test: markup in model output is text.
  assert.equal(tags("IMG").length, 0, "model output must never become an element");
  assert.match(
    bubble.textContent,
    /<img src=x onerror=alert\(1\)>/,
    "the tag must be visible to the user as the characters it is",
  );

  // And the user's own words are shown back unchanged.
  const userBubble = walk(
    document.byId.get("message-list"),
    (n) => n.classList?.contains("bubble") && n.parentNode?.classList?.contains("user"),
  ).at(-1);
  assert.equal(userBubble.classList.contains("markdown"), false);
}

// The retrieval announcement is the only way the user learns that an answer
// came out of their own documents rather than out of the model. This drives it
// the whole way: a turn streams the event, the panel names the sources, and
// the next turn does not inherit them.
async function testRetrievedContextIsShownAndResetPerTurn() {
  let emit = null;
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", updated_at: "2026-05-21T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=false") {
        return response({ items: [thread("rag-thread", "Grounded answers")] });
      }
      if (pathname === "/agent/threads/rag-thread/messages") return response({ items: [] });
      if (pathname.startsWith("/agent/threads/")) return response({ items: [] });
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  let turnSeq = 0;
  const desktopBridge = {
    agent: {
      async uploadThreadFile() { throw new Error("not exercised"); },
      async listSkills() { return typedSuccess(pptCatalog()); },
      startTurn(input, callback) {
        turnSeq += 1;
        const turnID = `rag-turn-${turnSeq}`;
        emit = (event) => callback({ ...event, turnID });
        return { turnID };
      },
      async cancelTurn(turnID) { return { turnID, canceled: true }; },
    },
  };

  const { document } = await runRenderer(bridge, desktopBridge);
  walk(document.byId.get("thread-list"), (node) => node.tagName === "BUTTON")[0].click();
  await settle();

  assert.equal(
    document.byId.get("retrieved-empty").hidden,
    false,
    "before any turn the section must say nothing has been retrieved",
  );

  const input = document.byId.get("chat-input");
  input.value = "How did Q3 go?";
  input.dispatch("input");
  document.byId.get("chat-form").submit();
  await settle();

  emit({
    type: "retrieval",
    sources: [
      { kind: "file", label: "q3-plan.md", snippet: "Revenue grew 12%.", score: 0.88 },
      { kind: "conversation", label: "Earlier conversation", snippet: "We set the Q3 target.", score: 0.51 },
    ],
  });
  emit({ type: "text_delta", delta: "Revenue was up." });
  await settle();

  assert.equal(document.byId.get("retrieved-meta").textContent, "2");
  assert.equal(
    document.byId.get("retrieved-empty").hidden,
    true,
    "the empty note must give way once sources arrive",
  );
  const listed = document.byId.get("retrieved-list");
  assert.equal(listed.children.length, 2);
  assert.match(listed.textContent, /q3-plan\.md/, "the file must be named");
  assert.match(
    listed.textContent,
    /Revenue grew 12%\./,
    "the passage handed to the model must be shown verbatim, not summarised",
  );
  assert.match(listed.textContent, /88% match/, "the score must be rendered as a similarity");

  // A new question invalidates the old provenance. Nothing clears it on the
  // way in — a turn that retrieves nothing sends no event at all — so if this
  // is not cleared at turn start the panel credits the new answer to the old
  // documents.
  emit({ type: "done", result: { code: "", subtype: "", is_error: false } });
  await settle();
  input.value = "Unrelated question";
  input.dispatch("input");
  document.byId.get("chat-form").submit();
  await settle();

  assert.equal(
    document.byId.get("retrieved-list").children.length,
    0,
    "the previous turn's sources must not survive into the next turn",
  );
  assert.equal(document.byId.get("retrieved-meta").textContent, "0");
}

// runShimTurn drives the shipped shim over a canned SSE body: real frame
// parsing, real validation, real callbacks. The shim is an IIFE, so nothing
// inside it can be poked at directly — which is the right constraint. What
// matters is what a turn delivers, and that is observable from here.
async function runShimTurn(sseText) {
  const shimPath = path.join(rendererDir, "shim.js");
  const shimSource = fs.readFileSync(shimPath, "utf8");
  const encoded = new TextEncoder().encode(sseText);
  let sent = false;

  const context = {
    console,
    URL,
    Headers,
    TextEncoder,
    TextDecoder,
    AbortController,
    crypto: { randomUUID: () => "00000000-0000-4000-8000-00000000beef" },
    fetch: async () => ({
      ok: true,
      status: 200,
      statusText: "OK",
      headers: new Map([["content-type", "text/event-stream"]]),
      body: {
        getReader: () => ({
          async read() {
            if (sent) return { done: true, value: undefined };
            sent = true;
            return { done: false, value: encoded };
          },
          async cancel() {},
        }),
      },
    }),
    setTimeout, clearTimeout,
    location: { origin: "http://127.0.0.1:5000" },
    document: {
      baseURI: "http://127.0.0.1:5000/CAPABILITY/",
      documentElement: { dataset: {} },
      addEventListener() {},
    },
  };
  context.window = context;
  // The shim hands its turn functions to the generated bridge factory rather
  // than putting them on a global, so the factory is where they are reachable
  // from. Capturing them is not a back door: it is the same object the real
  // lib/desktop-bridge.js receives.
  let transport = null;
  context.window.__workmaxDesktopBridge = {
    createDesktopBridge: (deps) => {
      transport = deps;
      return {};
    },
  };
  vm.createContext(context);
  vm.runInContext(shimSource, context, { filename: shimPath });
  assert.ok(transport, "shim.js must build the typed bridge");

  const events = [];
  transport.startAgentTurn({ thread_uuid: "t" }, (event) => events.push(event));
  await settle();
  return events;
}

const SSE_DONE_FRAME = 'event: done\ndata: {"type":"done","result":"OK"}\n\n';

function retrievalFrame(payload) {
  return `event: retrieval\ndata: ${JSON.stringify(payload)}\n\n`;
}

// The shim decides whether a payload from the sidecar is allowed to reach the
// renderer at all. These are the shapes it must refuse — and, just as
// important, the fact that refusing one must not cost the user the answer.
async function testShimValidatesRetrievalPayloads() {
  const good = await runShimTurn(
    retrievalFrame({
      sources: [
        { kind: "file", label: "a.md", snippet: "text", score: 0.5 },
        { kind: "conversation", label: "Earlier conversation", snippet: "prior", score: 0.25 },
      ],
    }) + SSE_DONE_FRAME,
  );
  const retrieval = good.find((e) => e.type === "retrieval");
  assert.ok(retrieval, "a well-formed retrieval frame must be delivered");
  assert.equal(retrieval.sources.length, 2);
  assert.equal(retrieval.sources[0].label, "a.md");
  assert.equal(retrieval.sources[0].score, 0.5);
  assert.ok(good.some((e) => e.type === "done"), "the turn must still complete");

  // Each of these is malformed in a different way. All must be dropped, and
  // none may turn into a protocol error: the provenance list is informational,
  // and failing a delivered answer over it would be a bad trade.
  const refused = [
    { sources: "not-an-array" },
    { sources: [{ kind: "file", label: "" }] },
    { sources: [{ kind: "deliverable", label: "a.md" }] },
    { sources: [{ label: "a.md" }] },
  ];
  for (const payload of refused) {
    const events = await runShimTurn(retrievalFrame(payload) + SSE_DONE_FRAME);
    assert.equal(
      events.some((e) => e.type === "retrieval"),
      false,
      `malformed payload must not be delivered: ${JSON.stringify(payload)}`,
    );
    assert.equal(
      events.some((e) => e.type === "protocol_error"),
      false,
      `a malformed provenance list must not fail the turn: ${JSON.stringify(payload)}`,
    );
    assert.ok(events.some((e) => e.type === "done"), "the answer must still arrive");
  }

  // A score outside 0..1 is clamped rather than refused: the source really was
  // used, and losing that over a rounding artefact would be the worse error.
  const clamped = await runShimTurn(
    retrievalFrame({ sources: [
      { kind: "file", label: "high.md", score: 4 },
      { kind: "file", label: "low.md", score: -1 },
      { kind: "file", label: "text.md", score: "high" },
    ] }) + SSE_DONE_FRAME,
  ).then((events) => events.find((e) => e.type === "retrieval"));
  assert.equal(clamped.sources[0].score, 1);
  assert.equal(clamped.sources[1].score, 0);
  assert.equal(
    clamped.sources[2].score,
    null,
    "a non-numeric score becomes absent, not zero — zero would read as 'no match'",
  );

  const bounded = await runShimTurn(
    retrievalFrame({
      sources: Array.from({ length: 40 }, () => ({
        kind: "file",
        label: "x".repeat(500),
        snippet: "y".repeat(5000),
      })),
    }) + SSE_DONE_FRAME,
  ).then((events) => events.find((e) => e.type === "retrieval"));
  assert.equal(bounded.sources.length, 12, "the list must be bounded whatever the sidecar sends");
  assert.equal(bounded.sources[0].label.length, 120);
  assert.equal(bounded.sources[0].snippet.length, 400);
}

async function testStagedAttachmentsAreSentWithTheTurn() {
  let sentFileIDs = null;
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", updated_at: "2026-05-21T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=false") {
        return response({ items: [thread("attach-thread", "Attachments")] });
      }
      if (pathname === "/agent/threads/attach-thread/messages") {
        return response({ items: [] });
      }
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async uploadThreadFile() {
        return typedSuccess({ file_id: 4242 });
      },
      async listSkills() {
        return typedSuccess(pptCatalog());
      },
      startTurn(input, callback) {
        sentFileIDs = input.fileIDs;
        callback({ type: "text_delta", turnID: "attach-turn", delta: "ok" });
        callback({
          type: "done",
          turnID: "attach-turn",
          result: { code: "", subtype: "", is_error: false },
        });
        return { turnID: "attach-turn" };
      },
      async cancelTurn(turnID) {
        return { turnID, canceled: true };
      },
    },
  };

  const { document, context } = await runRenderer(bridge, desktopBridge);
  walk(document.byId.get("thread-list"), (node) => node.tagName === "BUTTON")[0].click();
  await settle();

  // Stage one uploaded attachment the way the upload callback does.
  // uploadThreadFile is a top-level renderer function; the VM context exposes
  // it, and a plain object is enough because the renderer only reads name/size
  // before handing the file to the bridge.
  context.uploadThreadFile({ name: "notes.txt", size: 12 });
  await settle();

  const input = document.byId.get("chat-input");
  input.value = "Summarise the attachment";
  input.dispatch("input");
  document.byId.get("chat-form").submit();
  await settle();

  // Array.from because the value crossed a VM realm boundary: same contents,
  // different Array prototype, and deepStrictEqual cares.
  assert.deepEqual(
    Array.from(sentFileIDs ?? []),
    [4242],
    "the staged attachment id must reach startTurn; an empty list means the tray was cleared first"
  );
  assert.equal(
    document.byId.get("attachment-chips").hidden,
    true,
    "the tray must be cleared once the turn owns the ids"
  );
}

async function testSynchronousTurnCallbacksAreBufferedUntilOpenResult() {
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", updated_at: "2026-05-21T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=false") {
        return response({ items: [thread("sync-callback-thread", "Synchronous callback")] });
      }
      if (pathname === "/agent/threads/sync-callback-thread/messages") {
        return response({
          items: [message("sync-callback-message", "Synchronous prompt", "Cached sync answer")],
        });
      }
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async uploadThreadFile() {
        throw new Error("uploadThreadFile is not exercised by this test");
      },
      async listSkills() {
        return typedSuccess(pptCatalog());
      },
      startTurn(_input, callback) {
        callback({
          type: "unknown",
          turnID: "sync-callback-turn",
          event: "private_sync_payload",
        });
        callback({
          type: "text_delta",
          turnID: "sync-callback-turn",
          delta: "Buffered live answer",
        });
        callback({
          type: "done",
          turnID: "sync-callback-turn",
          result: { code: "", subtype: "", is_error: false },
        });
        return { turnID: "sync-callback-turn" };
      },
      async cancelTurn(turnID) {
        return { turnID, canceled: true };
      },
    },
  };

  const { document } = await runRenderer(bridge, desktopBridge);
  walk(document.byId.get("thread-list"), (node) => node.tagName === "BUTTON")[0].click();
  await settle();
  const input = document.byId.get("chat-input");
  input.value = "Synchronous prompt";
  input.dispatch("input");
  document.byId.get("chat-form").submit();
  assert.match(document.byId.get("message-list").textContent, /Buffered live answer/);
  assert.doesNotMatch(document.byId.get("message-list").textContent, /sync-callback-secret/);
  assert.equal(document.byId.get("turn-state").textContent, "Done");
  await settle();
  assert.match(document.byId.get("message-list").textContent, /Cached sync answer/);
}

async function testAgentTurnStreamsAndReconciles() {
  const fetchCalls = [];
  const startCalls = [];
  let streamCallback;
  let messageReads = 0;
  let threadReads = 0;
  let skillReads = 0;
  const bridge = {
    sidecarVersion: "sidecar-agent",
    appVersion: "app-agent",
    async fetch(pathname) {
      fetchCalls.push(pathname);
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", tier: "pro", updated_at: "2026-05-21T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=false") {
        threadReads += 1;
        return response({
          items: [thread("thread-agent", "Deck draft", threadReads === 1 ? 1 : 2)],
        });
      }
      if (pathname === "/agent/threads/thread-agent/messages") {
        messageReads += 1;
        return response({
          items:
            messageReads === 1
              ? [message("message-initial", "Initial prompt", "Initial answer")]
              : [
                  message("message-initial", "Initial prompt", "Initial answer"),
                  message("message-final", "Refine the deck", "Final cached answer"),
                ],
        });
      }
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async uploadThreadFile() {
        throw new Error("uploadThreadFile is not exercised by this test");
      },
      async listSkills() {
        skillReads += 1;
        return typedSuccess(pptCatalog());
      },
      startTurn(input, callback) {
        startCalls.push({
          threadUUID: input.threadUUID,
          userText: input.userText,
          chatMode: input.chatMode,
        });
        streamCallback = callback;
        return { turnID: "turn-agent" };
      },
      async cancelTurn(turnID) {
        return { turnID, canceled: true };
      },
    },
  };

  const { context, document } = await runRenderer(bridge, desktopBridge);
  assert.equal(skillReads, 1);
  assert.equal(
    document.byId.get("new-thread-button").disabled,
    true,
    "an alpha.4-style Agent bridge must keep existing-thread chat while New stays unavailable"
  );
  const threadButton = walk(
    document.byId.get("thread-list"),
    (node) => node.tagName === "BUTTON"
  )[0];
  threadButton.click();
  await settle();

  assert.equal(document.byId.get("agent-mode").value, "ppt");
  assert.equal(document.byId.get("chat-input").disabled, false);
  const chatInput = document.byId.get("chat-input");
  chatInput.value = "  Refine the deck  ";
  chatInput.dispatch("input");
  assert.equal(document.byId.get("send-button").disabled, false);

  let prevented = 0;
  chatInput.dispatch("keydown", {
    key: "Enter",
    metaKey: true,
    preventDefault() {
      prevented += 1;
    },
  });
  document.byId.get("chat-form").submit();
  assert.equal(prevented, 1);
  assert.equal(startCalls.length, 1, "a second submit while streaming must be ignored");
  assert.deepEqual(startCalls[0], {
    threadUUID: "thread-agent",
    userText: "Refine the deck",
    chatMode: "ppt",
  });
  assert.match(document.byId.get("message-list").textContent, /Refine the deck/);
  assert.equal(document.byId.get("stop-button").hidden, false);

  streamCallback({
    type: "unknown",
    turnID: "turn-agent",
    event: "private_tool_payload",
  });
  assert.doesNotMatch(document.byId.get("message-list").textContent, /unknown-event-secret/);
  assert.doesNotMatch(
    vm.runInContext("state.activeTurn.assistantText", context),
    /unknown-event-secret/
  );
  assert.equal(vm.runInContext("state.activeTurn.pendingEvents.length", context), 0);

  streamCallback({ type: "text_delta", turnID: "turn-agent", delta: "Live " });
  streamCallback({ type: "text_delta", turnID: "turn-agent", delta: "answer" });
  assert.match(document.byId.get("message-list").textContent, /Live answer/);
  assert.equal(document.byId.get("turn-state").textContent, "Working");

  streamCallback({
    type: "done",
    turnID: "turn-agent",
    result: { code: "OK", subtype: "already_processed", is_error: false },
  });
  await settle();
  await settle();
  assert.equal(document.byId.get("turn-state").textContent, "Done");
  assert.equal(document.byId.get("stop-button").hidden, true);
  assert.equal(document.byId.get("chat-input").disabled, false);
  assert.equal(messageReads, 2, "done must reconcile the cached message history");
  assert.equal(threadReads, 2, "done must reconcile thread metadata");
  assert.match(document.byId.get("message-list").textContent, /Final cached answer/);
  assert.equal(fetchCalls.filter((path) => path.endsWith("/messages")).length, 2);
}

async function testLateThreadHistoryCannotContaminateSelection() {
  let resolveThreadA;
  const pendingThreadA = new Promise((resolve) => {
    resolveThreadA = resolve;
  });
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", updated_at: "2026-05-21T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=false") {
        return response({
          items: [thread("thread-a", "Thread A"), thread("thread-b", "Thread B")],
        });
      }
      if (pathname === "/agent/threads/thread-a/messages") {
        return pendingThreadA;
      }
      if (pathname === "/agent/threads/thread-b/messages") {
        return response({ items: [message("message-b", "Prompt B", "Answer B")] });
      }
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };

  const { document } = await runRenderer(bridge);
  const buttons = walk(
    document.byId.get("thread-list"),
    (node) => node.tagName === "BUTTON"
  );
  buttons[0].click();
  buttons[1].click();
  await settle();
  assert.match(document.byId.get("message-list").textContent, /Answer B/);

  resolveThreadA(
    response({ items: [message("message-a", "Prompt A", "Late answer A")] })
  );
  await settle();
  assert.match(document.byId.get("message-list").textContent, /Answer B/);
  assert.doesNotMatch(document.byId.get("message-list").textContent, /Late answer A/);
  assert.equal(document.byId.get("thread-title").textContent, "Thread B");
}

async function testThreadSwitchCancelsAndFencesOldTurn() {
  let oldTurnCallback;
  const canceled = [];
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", updated_at: "2026-05-21T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=false") {
        return response({
          items: [thread("turn-thread-a", "Turn A"), thread("turn-thread-b", "Turn B")],
        });
      }
      if (pathname === "/agent/threads/turn-thread-a/messages") {
        return response({ items: [message("turn-message-a", "A", "Cached A")] });
      }
      if (pathname === "/agent/threads/turn-thread-b/messages") {
        return response({ items: [message("turn-message-b", "B", "Cached B")] });
      }
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async uploadThreadFile() {
        throw new Error("uploadThreadFile is not exercised by this test");
      },
      async listSkills() {
        return typedSuccess(pptCatalog());
      },
      startTurn(_input, callback) {
        oldTurnCallback = callback;
        return { turnID: "old-turn" };
      },
      async cancelTurn(turnID) {
        canceled.push(turnID);
        return { turnID, canceled: true };
      },
    },
  };

  const { document } = await runRenderer(bridge, desktopBridge);
  const buttons = walk(
    document.byId.get("thread-list"),
    (node) => node.tagName === "BUTTON"
  );
  buttons[0].click();
  await settle();
  const input = document.byId.get("chat-input");
  input.value = "Continue A";
  input.dispatch("input");
  input.dispatch("keydown", { key: "Enter", ctrlKey: true });
  buttons[1].click();
  await settle();

  assert.deepEqual(canceled, ["old-turn"]);
  assert.match(document.byId.get("message-list").textContent, /Cached B/);
  oldTurnCallback({
    type: "text_delta",
    turnID: "old-turn",
    delta: "late-old-turn-secret",
  });
  oldTurnCallback({
    type: "done",
    turnID: "old-turn",
    result: { code: "", subtype: "", is_error: false },
  });
  await settle();
  assert.doesNotMatch(document.byId.get("message-list").textContent, /late-old-turn-secret/);
  assert.match(document.byId.get("message-list").textContent, /Cached B/);
  assert.equal(document.byId.get("thread-title").textContent, "Turn B");
}

async function testStopTurnIsSingleShot() {
  let cancelCalls = 0;
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", updated_at: "2026-05-21T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=false") {
        return response({ items: [thread("stop-thread", "Stop thread")] });
      }
      if (pathname === "/agent/threads/stop-thread/messages") {
        return response({ items: [message("stop-message", "Before", "Cached") ] });
      }
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async uploadThreadFile() {
        throw new Error("uploadThreadFile is not exercised by this test");
      },
      async listSkills() {
        return typedSuccess(pptCatalog());
      },
      startTurn() {
        return { turnID: "stop-turn" };
      },
      async cancelTurn(turnID) {
        cancelCalls += 1;
        return { turnID, canceled: true };
      },
    },
  };

  const { document } = await runRenderer(bridge, desktopBridge);
  walk(document.byId.get("thread-list"), (node) => node.tagName === "BUTTON")[0].click();
  await settle();
  const input = document.byId.get("chat-input");
  input.value = "Stop this generation";
  input.dispatch("input");
  document.byId.get("chat-form").submit();
  const stop = document.byId.get("stop-button");
  stop.click();
  stop.click();
  await settle();
  assert.equal(cancelCalls, 1);
  assert.equal(document.byId.get("turn-state").textContent, "Stopped");
  assert.equal(stop.hidden, true);
  assert.equal(document.byId.get("chat-input").disabled, false);
}

async function testInitialTurnBusyRefreshesRecoveryWithoutReplay() {
  const turnID = "123e4567-e89b-42d3-a456-426614174011";
  const interrupted = recoverableTurn({
    turn_uuid: turnID,
    thread_uuid: "initial-busy-thread",
    user_text: "Initial busy prompt",
    last_error_kind: "turn_in_progress",
  });
  let streamCallback;
  let startCalls = 0;
  let resumeCalls = 0;
  let recoveryReads = 0;
  let messageReads = 0;
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", updated_at: "2026-08-06T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=false") {
        return response({ items: [thread("initial-busy-thread", "Initial busy")] });
      }
      if (pathname === "/agent/threads/initial-busy-thread/messages") {
        messageReads += 1;
        return response({ items: [] });
      }
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async uploadThreadFile() {
        throw new Error("uploadThreadFile is not exercised by this test");
      },
      async listSkills() {
        return typedSuccess(pptCatalog());
      },
      async listRecoverableTurns() {
        recoveryReads += 1;
        return typedSuccess({ items: [], count: 0 });
      },
      startTurn(_input, callback) {
        startCalls += 1;
        streamCallback = callback;
        return { turnID };
      },
      resumeTurn() {
        resumeCalls += 1;
        throw new Error("THREAD_BUSY discovery must never replay automatically");
      },
      async cancelTurn(candidate) {
        return { turnID: candidate, canceled: true };
      },
    },
  };

  const { context, document } = await runRenderer(bridge, desktopBridge);
  walk(document.byId.get("thread-list"), (node) => node.tagName === "BUTTON")[0].click();
  await settle();
  const input = document.byId.get("chat-input");
  input.value = interrupted.user_text;
  input.dispatch("input");
  document.byId.get("chat-form").submit();
  streamCallback({
    type: "done",
    turnID,
    result: { code: "THREAD_BUSY", subtype: "thread_busy", is_error: true },
  });
  await settle();
  await settle();

  assert.equal(startCalls, 1);
  assert.equal(resumeCalls, 0, "initial THREAD_BUSY must never replay automatically");
  assert.equal(recoveryReads, 2, "initial THREAD_BUSY must refresh recoverable turns once");
  assert.equal(messageReads, 1, "THREAD_BUSY must not reconcile as a completed response");
  assert.equal(document.byId.get("turn-state").textContent, "Interrupted");
  assert.notEqual(document.byId.get("turn-state").textContent, "Done");
  assert.equal(document.byId.get("turn-recovery-card").hidden, false);
  assert.match(document.byId.get("status-card").textContent, /still busy.*Resume/i);
  assert.equal(vm.runInContext("state.recoverableTurns.length", context), 1);
  assert.equal(
    vm.runInContext("state.recoverableTurns[0].turn_uuid", context),
    turnID,
    "a stale recoverable list must not discard the immutable local intent"
  );
  await settle();
  assert.equal(resumeCalls, 0);
}

async function testCancelAckFailureShowsLocalStopAndRefreshesRecovery() {
  const turnID = "123e4567-e89b-42d3-a456-426614174012";
  const interrupted = recoverableTurn({
    turn_uuid: turnID,
    thread_uuid: "cancel-ack-thread",
    user_text: "Stop this request",
    last_error_kind: "transport_stopped",
  });
  let streamCallback;
  let cancelCalls = 0;
  let recoveryReads = 0;
  let resumeCalls = 0;
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", updated_at: "2026-08-06T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=false") {
        return response({ items: [thread("cancel-ack-thread", "Cancel acknowledgment")] });
      }
      if (pathname === "/agent/threads/cancel-ack-thread/messages") {
        return response({ items: [] });
      }
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async uploadThreadFile() {
        throw new Error("uploadThreadFile is not exercised by this test");
      },
      async listSkills() {
        return typedSuccess(pptCatalog());
      },
      async listRecoverableTurns() {
        recoveryReads += 1;
        return typedSuccess({ items: [], count: 0 });
      },
      startTurn(_input, callback) {
        streamCallback = callback;
        return { turnID };
      },
      resumeTurn() {
        resumeCalls += 1;
        throw new Error("cancel recovery discovery must never replay automatically");
      },
      async cancelTurn(candidate) {
        cancelCalls += 1;
        streamCallback({ type: "canceled", turnID: candidate });
        throw new Error("Sidecar cancel acknowledgment unavailable");
      },
    },
  };

  const { context, document } = await runRenderer(bridge, desktopBridge);
  walk(document.byId.get("thread-list"), (node) => node.tagName === "BUTTON")[0].click();
  await settle();
  const input = document.byId.get("chat-input");
  input.value = interrupted.user_text;
  input.dispatch("input");
  document.byId.get("chat-form").submit();
  document.byId.get("stop-button").click();
  await settle();
  await settle();

  assert.equal(cancelCalls, 1);
  assert.equal(resumeCalls, 0);
  assert.equal(recoveryReads, 2, "cancel ACK failure must refresh recoverable turns once");
  assert.equal(document.byId.get("turn-state").textContent, "Stopped locally");
  assert.match(
    document.byId.get("status-card").textContent,
    /stopped locally.*persistent dismissal was not confirmed/i
  );
  assert.equal(document.byId.get("turn-recovery-card").hidden, false);
  assert.equal(
    vm.runInContext("state.recoverableTurns[0].last_error_kind", context),
    "cancel_unconfirmed"
  );
  await settle();
  assert.equal(resumeCalls, 0, "cancel ACK failure must never replay automatically");
}

async function testSSESessionChangedClearsPromptWithoutReplay() {
  let streamCallback;
  let starts = 0;
  let threadReads = 0;
  let authReads = 0;
  let skillReads = 0;
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        authReads += 1;
        return response({ state: "authenticated", updated_at: "2026-05-21T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=false") {
        threadReads += 1;
        return response({
          items:
            threadReads === 1
              ? [thread("old-account-thread", "Old account thread")]
              : [thread("new-account-thread", "New account thread")],
        });
      }
      if (pathname === "/agent/threads/old-account-thread/messages") {
        return response({ items: [message("old-message", "Old", "Old cached answer")] });
      }
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async uploadThreadFile() {
        throw new Error("uploadThreadFile is not exercised by this test");
      },
      async listSkills() {
        skillReads += 1;
        return typedSuccess(pptCatalog());
      },
      startTurn(_input, callback) {
        starts += 1;
        streamCallback = callback;
        return { turnID: "session-turn" };
      },
      async cancelTurn(turnID) {
        return { turnID, canceled: true };
      },
    },
  };

  const { context, document } = await runRenderer(bridge, desktopBridge);
  walk(document.byId.get("thread-list"), (node) => node.tagName === "BUTTON")[0].click();
  await settle();
  const prompt = "prompt-must-never-cross-account";
  const input = document.byId.get("chat-input");
  input.value = prompt;
  input.dispatch("input");
  document.byId.get("chat-form").submit();
  streamCallback({ type: "text_delta", turnID: "session-turn", delta: "partial old answer" });
  streamCallback({
    type: "proxy_error",
    turnID: "session-turn",
    error: {
      kind: "session_changed",
      message: "session replaced",
      retryable: false,
    },
  });
  await settle();
  await settle();

  assert.equal(starts, 1, "session recovery must never replay the prompt");
  assert.equal(authReads, 2);
  assert.equal(skillReads, 2);
  assert.equal(document.byId.get("chat-input").value, "");
  assert.equal(document.byId.get("thread-panel").hidden, true);
  assert.match(document.byId.get("thread-list").textContent, /New account thread/);
  assert.doesNotMatch(document.byId.get("message-list").textContent, /partial old answer/);
  assert.doesNotMatch(document.byId.get("message-list").textContent, new RegExp(prompt));
  assert.doesNotMatch(JSON.stringify(vm.runInContext("state", context)), new RegExp(prompt));
  assert.match(document.byId.get("status-card").textContent, /account changed/i);
  assert.match(document.byId.get("status-card").textContent, /not resent/i);

  streamCallback({
    type: "text_delta",
    turnID: "session-turn",
    delta: "late-session-delta",
  });
  assert.doesNotMatch(document.byId.get("message-list").textContent, /late-session-delta/);
}

async function testCatalog409UsesSessionChangedRecovery() {
  let skillsCalls = 0;
  let authCalls = 0;
  let threadCalls = 0;
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        authCalls += 1;
        return response({ state: "authenticated", updated_at: "2026-05-21T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=false") {
        threadCalls += 1;
        return response({
          items: [thread(`catalog-thread-${threadCalls}`, `Catalog account ${threadCalls}`)],
        });
      }
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async uploadThreadFile() {
        throw new Error("uploadThreadFile is not exercised by this test");
      },
      async listSkills() {
        skillsCalls += 1;
        if (skillsCalls === 1) {
          return typedFailure(409, { error: "session_changed" });
        }
        return typedSuccess(pptCatalog());
      },
      startTurn() {
        throw new Error("no turn should start during catalog recovery");
      },
      async cancelTurn(turnID) {
        return { turnID, canceled: false };
      },
    },
  };

  const { context, document } = await runRenderer(bridge, desktopBridge);
  await settle();
  await settle();
  assert.equal(skillsCalls, 2);
  assert.equal(authCalls, 2);
  assert.equal(vm.runInContext("state.selectedThreadUUID", context), null);
  assert.match(document.byId.get("thread-list").textContent, /Catalog account 2/);
  assert.match(document.byId.get("status-card").textContent, /account changed/i);
  assert.equal(document.byId.get("chat-input").disabled, true);
}

async function testRejectsMalformedAgentContractsWithoutLeakingPayload() {
  let streamCallback;
  const canceled = [];
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", updated_at: "2026-05-21T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=false") {
        return response({ items: [thread("strict-thread", "Strict contracts")] });
      }
      if (pathname === "/agent/threads/strict-thread/messages") {
        return response({ items: [] });
      }
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async uploadThreadFile() {
        throw new Error("uploadThreadFile is not exercised by this test");
      },
      async listSkills() {
        return typedSuccess(pptCatalog());
      },
      startTurn(_input, callback) {
        streamCallback = callback;
        return { turnID: "strict-turn" };
      },
      async cancelTurn(turnID) {
        canceled.push(turnID);
        return { turnID, canceled: true };
      },
    },
  };

  const { document } = await runRenderer(bridge, desktopBridge);
  walk(document.byId.get("thread-list"), (node) => node.tagName === "BUTTON")[0].click();
  await settle();
  const input = document.byId.get("chat-input");
  input.value = "Strict event";
  input.dispatch("input");
  document.byId.get("chat-form").submit();
  streamCallback({
    type: "text_delta",
    turnID: "strict-turn",
    delta: "must not render",
    private_token: "malformed-event-secret",
  });
  await settle();

  assert.deepEqual(canceled, ["strict-turn"]);
  assert.doesNotMatch(document.byId.get("message-list").textContent, /must not render/);
  assert.doesNotMatch(document.byId.get("message-list").textContent, /malformed-event-secret/);
  assert.doesNotMatch(document.byId.get("status-card").textContent, /malformed-event-secret/);
  assert.match(document.byId.get("status-card").textContent, /invalid event/i);
  assert.equal(document.byId.get("turn-state").textContent, "Error");
}

async function testRejectsLegacyOpenAgentEventShapes() {
  const cases = [
    {
      name: "unknown data",
      secret: "unknown-open-secret",
      event: {
        type: "unknown",
        turnID: "strict-open-turn",
        event: "tool",
        data: { opaque_secret: "unknown-open-secret" },
      },
    },
    {
      name: "done result extras",
      secret: "done-open-secret",
      event: {
        type: "done",
        turnID: "strict-open-turn",
        result: {
          code: "OK",
          subtype: "already_processed",
          is_error: false,
          opaque_secret: "done-open-secret",
        },
      },
    },
    {
      name: "proxy details",
      secret: "proxy-open-secret",
      event: {
        type: "proxy_error",
        turnID: "strict-open-turn",
        error: {
          kind: "unknown",
          message: "Proxy failed",
          details: { opaque_secret: "proxy-open-secret" },
        },
      },
    },
  ];

  for (const testCase of cases) {
    let streamCallback;
    const canceled = [];
    const bridge = {
      async fetch(pathname) {
        if (pathname === "/auth/status") {
          return response({ state: "authenticated", updated_at: "2026-08-06T00:00:00Z" });
        }
        if (pathname === "/agent/threads?include_paused=false") {
          return response({ items: [thread("strict-open-thread", "Strict closed events")] });
        }
        if (pathname === "/agent/threads/strict-open-thread/messages") {
          return response({ items: [] });
        }
        throw new Error(`unexpected fetch path ${pathname}`);
      },
    };
    const desktopBridge = {
      agent: {
        async uploadThreadFile() {
          throw new Error("uploadThreadFile is not exercised by this test");
        },
        async listSkills() {
          return typedSuccess(pptCatalog());
        },
        startTurn(_input, callback) {
          streamCallback = callback;
          return { turnID: "strict-open-turn" };
        },
        async cancelTurn(turnID) {
          canceled.push(turnID);
          return { turnID, canceled: true };
        },
      },
    };

    const { document } = await runRenderer(bridge, desktopBridge);
    walk(document.byId.get("thread-list"), (node) => node.tagName === "BUTTON")[0].click();
    await settle();
    const input = document.byId.get("chat-input");
    input.value = `Strict ${testCase.name}`;
    input.dispatch("input");
    document.byId.get("chat-form").submit();
    streamCallback(testCase.event);
    await settle();

    assert.deepEqual(canceled, ["strict-open-turn"], testCase.name);
    assert.match(document.byId.get("status-card").textContent, /invalid event/i);
    assert.doesNotMatch(document.byId.get("status-card").textContent, new RegExp(testCase.secret));
    assert.doesNotMatch(document.byId.get("message-list").textContent, new RegExp(testCase.secret));
  }
}

async function testRejectsMalformedCatalogResult() {
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", updated_at: "2026-05-21T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=false") {
        return response({ items: [thread("catalog-malformed", "Malformed catalog")] });
      }
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async uploadThreadFile() {
        throw new Error("uploadThreadFile is not exercised by this test");
      },
      async listSkills() {
        const malformed = pptCatalog();
        malformed.count = 2;
        return typedSuccess(malformed);
      },
      startTurn() {
        throw new Error("startTurn must remain disabled for malformed catalog");
      },
      async cancelTurn(turnID) {
        return { turnID, canceled: false };
      },
    },
  };

  const { document } = await runRenderer(bridge, desktopBridge);
  assert.match(document.byId.get("status-card").textContent, /Malformed agent skills catalog response/);
  assert.equal(document.byId.get("chat-input").disabled, true);
  assert.equal(document.byId.get("send-button").disabled, true);
}

async function testRecoverableTurnRequiresExplicitResumeAndHandlesBusy() {
  let messageReads = 0;
  let startCalls = 0;
  let resumeCalls = 0;
  let resumeCallback;
  let outcome = "busy-code";
  const recoverable = recoverableTurn();
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", updated_at: "2026-08-06T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=false") {
        return response({ items: [thread("thread-agent", "Quarterly deck")] });
      }
      if (pathname === "/agent/threads/thread-agent/messages") {
        messageReads += 1;
        return response({
          items: messageReads === 1
            ? [message("message-partial", "Resume the quarterly deck", "Cached partial", "partial")]
            : [message("message-final", "Resume the quarterly deck", "Recovered final")],
        });
      }
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async uploadThreadFile() {
        throw new Error("uploadThreadFile is not exercised by this test");
      },
      async listSkills() {
        return typedSuccess(pptCatalog());
      },
      async listRecoverableTurns() {
        return typedSuccess({ items: [recoverable], count: 1 });
      },
      startTurn() {
        startCalls += 1;
        throw new Error("recovery discovery must never start a new prompt");
      },
      resumeTurn(turnUUID, callback) {
        resumeCalls += 1;
        assert.equal(turnUUID, recoverable.turn_uuid);
        resumeCallback = callback;
        if (outcome === "busy-code") {
          callback({
            type: "done",
            turnID: turnUUID,
            result: {
              code: "THREAD_BUSY",
              subtype: "admission",
              is_error: true,
            },
          });
        } else if (outcome === "busy-subtype") {
          callback({
            type: "done",
            turnID: turnUUID,
            result: {
              code: "CONFLICT",
              subtype: "thread_busy",
              is_error: true,
            },
          });
        } else {
          callback({ type: "text_delta", turnID: turnUUID, delta: "Recovered stream" });
          callback({
            type: "done",
            turnID: turnUUID,
            result: {
              code: "OK",
              subtype: "replay",
              is_error: false,
            },
          });
        }
        return { turnID: turnUUID };
      },
      async cancelTurn(turnID) {
        return { turnID, canceled: true };
      },
    },
  };

  const { context, document } = await runRenderer(bridge, desktopBridge);
  assert.equal(resumeCalls, 0, "startup discovery must not replay automatically");
  assert.equal(startCalls, 0);
  assert.equal(document.byId.get("turn-recovery-card").hidden, true);
  assert.match(document.byId.get("thread-list").textContent, /Interrupted/);

  walk(document.byId.get("thread-list"), (node) => node.tagName === "BUTTON")[0].click();
  await settle();
  assert.equal(resumeCalls, 0, "thread selection must not replay automatically");
  assert.equal(document.byId.get("turn-recovery-card").hidden, false);
  assert.match(document.byId.get("turn-recovery-prompt").textContent, /Resume the quarterly deck/);
  assert.equal(document.byId.get("chat-input").disabled, true);
  assert.match(document.byId.get("composer-status").textContent, /resume or dismiss/i);

  document.byId.get("turn-recovery-resume-button").click();
  await settle();
  assert.equal(resumeCalls, 1);
  assert.equal(startCalls, 0);
  assert.equal(vm.runInContext("state.activeTurn", context), null);
  assert.equal(document.byId.get("turn-recovery-card").hidden, false);
  assert.equal(document.byId.get("turn-recovery-resume-button").disabled, false);
  assert.equal(document.byId.get("turn-recovery-resume-button").focused, true);
  assert.match(document.byId.get("turn-recovery-feedback").textContent, /still busy/i);
  assert.doesNotMatch(document.byId.get("message-list").textContent, /must-not-render/);
  await settle();
  assert.equal(resumeCalls, 1, "THREAD_BUSY must not schedule an automatic retry");

  outcome = "busy-subtype";
  document.byId.get("turn-recovery-resume-button").click();
  await settle();
  assert.equal(resumeCalls, 2);
  assert.equal(vm.runInContext("state.activeTurn", context), null);
  assert.equal(document.byId.get("turn-recovery-card").hidden, false);
  assert.equal(document.byId.get("turn-recovery-resume-button").disabled, false);
  assert.equal(document.byId.get("turn-recovery-resume-button").focused, true);
  assert.match(document.byId.get("turn-recovery-feedback").textContent, /still busy/i);
  assert.doesNotMatch(document.byId.get("message-list").textContent, /subtype-secret/);
  await settle();
  assert.equal(resumeCalls, 2, "thread_busy subtype must not schedule an automatic retry");

  outcome = "success";
  document.byId.get("turn-recovery-resume-button").click();
  await settle();
  await settle();
  assert.equal(resumeCalls, 3);
  assert.equal(startCalls, 0);
  assert.equal(document.byId.get("turn-recovery-card").hidden, true);
  assert.equal(vm.runInContext("state.recoverableTurns.length", context), 0);
  assert.equal(document.byId.get("turn-state").textContent, "Done");
  assert.match(document.byId.get("message-list").textContent, /Recovered final/);
  assert.doesNotMatch(document.byId.get("message-list").textContent, /must-not-render/);
  assert.equal(messageReads, 2);
  assert.ok(resumeCallback);
}

async function testRecoverableTurnDismissIsExplicitAndIdempotent() {
  let resumeCalls = 0;
  const cancelCalls = [];
  const recoverable = recoverableTurn();
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", updated_at: "2026-08-06T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=false") {
        return response({ items: [thread("thread-agent", "Quarterly deck")] });
      }
      if (pathname === "/agent/threads/thread-agent/messages") {
        return response({ items: [] });
      }
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async uploadThreadFile() {
        throw new Error("uploadThreadFile is not exercised by this test");
      },
      async listSkills() {
        return typedSuccess(pptCatalog());
      },
      async listRecoverableTurns() {
        return typedSuccess({ items: [recoverable], count: 1 });
      },
      startTurn() {
        throw new Error("dismiss must not start a prompt");
      },
      resumeTurn() {
        resumeCalls += 1;
        throw new Error("dismiss must not resume");
      },
      async cancelTurn(turnID) {
        cancelCalls.push(turnID);
        return { turnID, canceled: false };
      },
    },
  };

  const { context, document } = await runRenderer(bridge, desktopBridge);
  walk(document.byId.get("thread-list"), (node) => node.tagName === "BUTTON")[0].click();
  await settle();
  document.byId.get("turn-recovery-dismiss-button").click();
  await settle();

  assert.deepEqual(cancelCalls, [recoverable.turn_uuid]);
  assert.equal(resumeCalls, 0);
  assert.equal(document.byId.get("turn-recovery-card").hidden, true);
  assert.equal(vm.runInContext("state.recoverableTurns.length", context), 0);
  assert.equal(document.byId.get("chat-input").disabled, false);
  assert.equal(document.byId.get("chat-input").focused, true);
  assert.match(document.byId.get("status-card").textContent, /already dismissed/i);
}

async function testRecoverableErrorResultIsSanitized() {
  const recoverable = recoverableTurn();
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", updated_at: "2026-08-06T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=false") {
        return response({ items: [thread("thread-agent", "Quarterly deck")] });
      }
      if (pathname === "/agent/threads/thread-agent/messages") {
        return response({ items: [] });
      }
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async uploadThreadFile() {
        throw new Error("uploadThreadFile is not exercised by this test");
      },
      async listSkills() {
        return typedSuccess(pptCatalog());
      },
      async listRecoverableTurns() {
        return typedSuccess({ items: [recoverable], count: 1 });
      },
      startTurn() {
        throw new Error("recovery must not start a fresh prompt");
      },
      resumeTurn(turnID, callback) {
        callback({
          type: "done",
          turnID,
          result: {
            code: "PLUGIN_FAILED",
            subtype: "render",
            is_error: true,
          },
        });
        return { turnID };
      },
      async cancelTurn(turnID) {
        return { turnID, canceled: true };
      },
    },
  };

  const { context, document } = await runRenderer(bridge, desktopBridge);
  walk(document.byId.get("thread-list"), (node) => node.tagName === "BUTTON")[0].click();
  await settle();
  document.byId.get("turn-recovery-resume-button").click();
  await settle();

  assert.equal(vm.runInContext("state.recoverableTurns.length", context), 0);
  assert.equal(document.byId.get("turn-recovery-card").hidden, true);
  assert.equal(document.byId.get("turn-state").textContent, "Error");
  assert.match(document.byId.get("status-card").textContent, /PLUGIN_FAILED.*render/);
  assert.doesNotMatch(document.byId.get("status-card").textContent, /error-result-secret/);
  assert.doesNotMatch(document.byId.get("message-list").textContent, /error-result-secret/);
}

async function testMalformedRecoverableTurnDoesNotLeakOrRender() {
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", updated_at: "2026-08-06T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=false") {
        return response({ items: [thread("thread-agent", "Quarterly deck")] });
      }
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async uploadThreadFile() {
        throw new Error("uploadThreadFile is not exercised by this test");
      },
      async listSkills() {
        return typedSuccess(pptCatalog());
      },
      async listRecoverableTurns() {
        return typedSuccess({
          items: [{ ...recoverableTurn(), uid: "private-recovery-uid" }],
          count: 1,
        });
      },
      startTurn() {
        throw new Error("malformed recovery must not start");
      },
      resumeTurn() {
        throw new Error("malformed recovery must not replay");
      },
      async cancelTurn(turnID) {
        return { turnID, canceled: false };
      },
    },
  };

  const { context, document } = await runRenderer(bridge, desktopBridge);
  assert.equal(vm.runInContext("state.recoverableTurns.length", context), 0);
  assert.equal(document.byId.get("turn-recovery-card").hidden, true);
  assert.doesNotMatch(document.byId.get("thread-list").textContent, /Interrupted/);
  assert.doesNotMatch(document.byId.get("status-card").textContent, /private-recovery-uid/);
  assert.match(document.byId.get("status-card").textContent, /Malformed agent recoverable turns result/);
}

async function testCreatesThreadOnceAndFocusesComposer() {
  const createCalls = [];
  let startCalls = 0;
  const newUUID = "00000000-0000-4000-8000-000000000001";
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", updated_at: "2026-08-06T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=false") {
        return response({ items: [] });
      }
      if (pathname === `/agent/threads/${newUUID}/messages`) {
        return response({ items: [] });
      }
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async uploadThreadFile() {
        throw new Error("uploadThreadFile is not exercised by this test");
      },
      async listSkills() {
        return typedSuccess(pptCatalog());
      },
      async createThread(input) {
        createCalls.push({
          threadUUID: input.threadUUID,
          name: input.name,
          agentMode: input.agentMode,
        });
        return typedSuccess(
          {
            state: "ready",
            created: true,
            thread: createdThread(input.threadUUID, input.name, input.agentMode),
          },
          201
        );
      },
      startTurn() {
        startCalls += 1;
        throw new Error("create must not auto-send a prompt");
      },
      async cancelTurn(turnID) {
        return { turnID, canceled: false };
      },
    },
  };

  const { context, document } = await runRenderer(bridge, desktopBridge);
  assert.equal(document.byId.get("empty-title").textContent, "Start a presentation thread");
  assert.equal(document.byId.get("empty-new-thread-button").hidden, false);
  assert.equal(document.byId.get("empty-new-thread-button").disabled, false);
  document.byId.get("empty-new-thread-button").click();
  assert.equal(document.byId.get("new-thread-form").hidden, false);
  assert.equal(document.byId.get("new-thread-name").focused, true);
  assert.equal(document.byId.get("new-thread-name").selected, true);
  assert.equal(document.byId.get("new-thread-mode").value, "ppt");

  document.byId.get("new-thread-name").value = "Quarterly planning";
  document.byId.get("new-thread-name").dispatch("input");
  assert.equal(document.byId.get("new-thread-submit-button").disabled, false);
  document.byId.get("new-thread-form").dispatch("keydown", { key: "Enter" });
  document.byId.get("new-thread-form").submit();
  await settle();
  await settle();

  assert.deepEqual(createCalls, [
    {
      threadUUID: newUUID,
      name: "Quarterly planning",
      agentMode: "ppt",
    },
  ]);
  assert.equal(startCalls, 0);
  assert.equal(document.byId.get("new-thread-form").hidden, true);
  assert.match(document.byId.get("thread-list").textContent, /Quarterly planning/);
  assert.equal(document.byId.get("thread-title").textContent, "Quarterly planning");
  assert.equal(document.byId.get("thread-panel").hidden, false);
  assert.equal(document.byId.get("chat-input").focused, true);
  assert.equal(document.byId.get("chat-input").value, "");
  assert.equal(vm.runInContext("state.selectedThreadUUID", context), newUUID);
  assert.equal(vm.runInContext("state.createDraft", context), null);
}

async function testCreateRetriesKeepUUIDAndAcceptCurrentReplayRow() {
  const createCalls = [];
  let authReads = 0;
  let threadReads = 0;
  let messageReads = 0;
  const newUUID = "00000000-0000-4000-8000-000000000001";
  const existingUUID = "thread-existing";
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        authReads += 1;
        return response({ state: "authenticated", updated_at: "2026-08-06T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=false") {
        threadReads += 1;
        return response({ items: [thread(existingUUID, "Existing thread")] });
      }
      if (pathname === `/agent/threads/${newUUID}/messages`) {
        messageReads += 1;
        return response({ items: [] });
      }
      if (pathname === `/agent/threads/${existingUUID}/messages`) {
        messageReads += 1;
        return response({ items: [] });
      }
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async uploadThreadFile() {
        throw new Error("uploadThreadFile is not exercised by this test");
      },
      async listSkills() {
        return typedSuccess(pptCatalogWithReplayMode());
      },
      async createThread(input) {
        createCalls.push({
          threadUUID: input.threadUUID,
          name: input.name,
          agentMode: input.agentMode,
        });
        if (createCalls.length === 1) {
          return typedFailure(502, {
            error: "agent_create_unavailable",
            private_detail: "create-private-secret",
            retry_with_same_uuid: true,
          });
        }
        if (createCalls.length === 2) {
          return typedSuccess(
            { state: "pending_local_sync", thread_uuid: input.threadUUID },
            202
          );
        }
        return typedSuccess({
          state: "ready",
          created: false,
          thread: createdThread(
            input.threadUUID,
            "Current cloud title",
            "ppt_revised"
          ),
        });
      },
      startTurn() {
        throw new Error("no prompt should be sent during create recovery");
      },
      async cancelTurn(turnID) {
        return { turnID, canceled: false };
      },
    },
  };

  const { context, document } = await runRenderer(bridge, desktopBridge);
  document.byId.get("new-thread-button").click();
  document.byId.get("new-thread-name").value = "Original draft title";
  document.byId.get("new-thread-name").dispatch("input");
  document.byId.get("new-thread-form").submit();
  await settle();
  assert.equal(document.byId.get("new-thread-form").hidden, false);
  assert.equal(document.byId.get("new-thread-name").disabled, true);
  assert.match(document.byId.get("new-thread-error").textContent, /same identity/i);
  assert.doesNotMatch(document.byId.get("status-card").textContent, /create-private-secret/);
  assert.equal(vm.runInContext("state.createDraft.threadUUID", context), newUUID);
  assert.equal(document.byId.get("refresh-button").disabled, true);

  const firstDraft = vm.runInContext("({ ...state.createDraft })", context);
  await vm.runInContext("refresh()", context);
  await settle();
  assert.deepEqual(vm.runInContext("({ ...state.createDraft })", context), firstDraft);
  assert.equal(document.byId.get("new-thread-form").hidden, false);
  assert.equal(authReads, 1);
  assert.equal(threadReads, 1);
  assert.equal(messageReads, 0);
  assert.match(document.byId.get("status-card").textContent, /retry or cancel.*before refreshing/i);

  const [existingThreadButton] = walk(
    document.byId.get("thread-list"),
    (node) => node.classList?.contains("thread-button")
  );
  assert.ok(existingThreadButton);
  existingThreadButton.click();
  assert.equal(vm.runInContext("state.selectedThreadUUID", context), null);
  assert.deepEqual(vm.runInContext("({ ...state.createDraft })", context), firstDraft);
  assert.equal(messageReads, 0);
  assert.match(document.byId.get("status-card").textContent, /retry or cancel.*before switching/i);

  document.byId.get("new-thread-form").submit();
  await settle();
  assert.equal(document.byId.get("new-thread-submit-button").textContent, "Retry sync");
  assert.match(document.byId.get("new-thread-error").textContent, /cloud thread is ready/i);
  assert.equal(vm.runInContext("state.createDraft.threadUUID", context), newUUID);
  assert.equal(document.byId.get("refresh-button").disabled, true);
  await vm.runInContext("refresh()", context);
  await settle();
  assert.equal(vm.runInContext("state.createDraft.threadUUID", context), newUUID);
  assert.equal(vm.runInContext("state.createDraft.name", context), "Original draft title");
  assert.equal(vm.runInContext("state.createDraft.agentMode", context), "ppt");
  assert.equal(authReads, 1);
  assert.equal(threadReads, 1);

  document.byId.get("new-thread-form").submit();
  await settle();
  await settle();
  assert.equal(createCalls.length, 3);
  for (const call of createCalls) {
    assert.deepEqual(call, {
      threadUUID: newUUID,
      name: "Original draft title",
      agentMode: "ppt",
    });
  }
  assert.equal(document.byId.get("thread-title").textContent, "Current cloud title");
  assert.equal(document.byId.get("agent-mode").value, "ppt_revised");
  assert.equal(vm.runInContext("state.selectedThreadUUID", context), newUUID);
  assert.equal(messageReads, 1);
}

async function testPermanentCreateFailuresDoNotOfferSameIdentityRetry() {
  const cases = [
    {
      status: 401,
      error: { error: "authentication_required", retry_with_same_uuid: true },
      feedback: /authentication is required/i,
    },
    {
      status: 409,
      error: { error: "thread_uuid_conflict", retry_with_same_uuid: true },
      feedback: /already owned elsewhere/i,
    },
    {
      status: 409,
      error: { error: "local_identity_conflict", retry_with_same_uuid: true },
      feedback: /identity conflict/i,
    },
  ];

  for (const testCase of cases) {
    let authReads = 0;
    let threadReads = 0;
    let createCalls = 0;
    const bridge = {
      async fetch(pathname) {
        if (pathname === "/auth/status") {
          authReads += 1;
          return response({ state: "authenticated", updated_at: "2026-08-06T00:00:00Z" });
        }
        if (pathname === "/agent/threads?include_paused=false") {
          threadReads += 1;
          return response({ items: [] });
        }
        throw new Error(`unexpected fetch path ${pathname}`);
      },
    };
    const desktopBridge = {
      agent: {
        async uploadThreadFile() {
          throw new Error("uploadThreadFile is not exercised by this test");
        },
        async listSkills() {
          return typedSuccess(pptCatalog());
        },
        async createThread() {
          createCalls += 1;
          return typedFailure(testCase.status, testCase.error);
        },
        startTurn() {
          throw new Error("permanent create failure must not start a turn");
        },
        async cancelTurn(turnID) {
          return { turnID, canceled: false };
        },
      },
    };

    const { context, document } = await runRenderer(bridge, desktopBridge);
    document.byId.get("new-thread-button").click();
    document.byId.get("new-thread-form").submit();
    await settle();

    assert.equal(createCalls, 1);
    assert.equal(document.byId.get("new-thread-form").hidden, false);
    assert.equal(vm.runInContext("state.createDraft.retryable", context), false);
    assert.match(document.byId.get("new-thread-error").textContent, testCase.feedback);
    assert.doesNotMatch(
      `${document.byId.get("new-thread-error").textContent} ${document.byId.get("status-card").textContent}`,
      /same identity|retry keeps/i
    );
    assert.equal(document.byId.get("new-thread-submit-button").disabled, true);
    assert.equal(document.byId.get("new-thread-submit-button").textContent, "Cannot retry");
    assert.equal(document.byId.get("refresh-button").disabled, true);

    document.byId.get("new-thread-form").submit();
    await settle();
    assert.equal(createCalls, 1);

    await vm.runInContext("refresh()", context);
    await settle();
    assert.equal(createCalls, 1);
    assert.equal(authReads, 1);
    assert.equal(threadReads, 1);
    assert.equal(document.byId.get("new-thread-form").hidden, false);
    assert.equal(vm.runInContext("state.createDraft.retryable", context), false);
    assert.match(document.byId.get("status-card").textContent, /cancel.*before refreshing/i);

    document.byId.get("new-thread-cancel-button").click();
    assert.equal(document.byId.get("new-thread-form").hidden, true);
    assert.equal(vm.runInContext("state.createDraft", context), null);
    assert.equal(document.byId.get("refresh-button").disabled, false);
  }
}

async function testPausedCreateReplayRequiresExplicitCancel() {
  let createCalls = 0;
  const newUUID = "00000000-0000-4000-8000-000000000001";
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", updated_at: "2026-08-06T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=false") {
        return response({ items: [] });
      }
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async uploadThreadFile() {
        throw new Error("uploadThreadFile is not exercised by this test");
      },
      async listSkills() {
        return typedSuccess(pptCatalog());
      },
      async createThread(input) {
        createCalls += 1;
        return typedSuccess({
          state: "ready",
          created: false,
          thread: {
            ...createdThread(input.threadUUID, "Paused cloud title", input.agentMode),
            cloud_sync_state: "paused",
          },
        });
      },
      startTurn() {
        throw new Error("a paused create replay must not start a turn");
      },
      async cancelTurn(turnID) {
        return { turnID, canceled: false };
      },
    },
  };

  const { context, document } = await runRenderer(bridge, desktopBridge);
  document.byId.get("new-thread-button").click();
  document.byId.get("new-thread-form").submit();
  await settle();

  assert.equal(createCalls, 1);
  assert.equal(vm.runInContext("state.selectedThreadUUID", context), null);
  assert.equal(vm.runInContext("state.createDraft.threadUUID", context), newUUID);
  assert.equal(vm.runInContext("state.createDraft.retryable", context), false);
  assert.equal(document.byId.get("new-thread-form").hidden, false);
  assert.equal(document.byId.get("new-thread-submit-button").disabled, true);
  assert.equal(document.byId.get("refresh-button").disabled, true);
  assert.match(document.byId.get("new-thread-error").textContent, /paused.*cancel/i);
  assert.doesNotMatch(document.byId.get("thread-list").textContent, /Paused cloud title/);

  document.byId.get("new-thread-form").submit();
  await settle();
  assert.equal(createCalls, 1);

  document.byId.get("new-thread-cancel-button").click();
  assert.equal(document.byId.get("new-thread-form").hidden, true);
  assert.equal(vm.runInContext("state.createDraft", context), null);
  assert.equal(document.byId.get("refresh-button").disabled, false);
}

async function testCreateEscapeFencesLateCompletion() {
  let resolveCreate;
  let createCalls = 0;
  const newUUID = "00000000-0000-4000-8000-000000000001";
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", updated_at: "2026-08-06T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=false") {
        return response({ items: [] });
      }
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async uploadThreadFile() {
        throw new Error("uploadThreadFile is not exercised by this test");
      },
      async listSkills() {
        return typedSuccess(pptCatalog());
      },
      createThread() {
        createCalls += 1;
        return new Promise((resolve) => {
          resolveCreate = resolve;
        });
      },
      startTurn() {
        throw new Error("late create must not start a turn");
      },
      async cancelTurn(turnID) {
        return { turnID, canceled: false };
      },
    },
  };

  const { context, document } = await runRenderer(bridge, desktopBridge);
  document.byId.get("new-thread-button").click();
  document.byId.get("new-thread-form").submit();
  document.byId.get("new-thread-form").dispatch("keydown", { key: "Escape" });
  assert.equal(document.byId.get("new-thread-form").hidden, true);
  assert.match(document.byId.get("status-card").textContent, /late result will be ignored/i);

  resolveCreate(
    typedSuccess(
      {
        state: "ready",
        created: true,
        thread: createdThread(newUUID, "Untitled presentation"),
      },
      201
    )
  );
  await settle();
  assert.equal(createCalls, 1);
  assert.equal(vm.runInContext("state.selectedThreadUUID", context), null);
  assert.equal(vm.runInContext("state.createDraft", context), null);
  assert.doesNotMatch(document.byId.get("thread-list").textContent, /Untitled presentation/);
}

async function testCreateSessionChangedUsesUnifiedRecovery() {
  let authReads = 0;
  let threadReads = 0;
  let skillReads = 0;
  let createCalls = 0;
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        authReads += 1;
        return response({ state: "authenticated", updated_at: "2026-08-06T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=false") {
        threadReads += 1;
        return response({ items: [] });
      }
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async uploadThreadFile() {
        throw new Error("uploadThreadFile is not exercised by this test");
      },
      async listSkills() {
        skillReads += 1;
        return typedSuccess(pptCatalog());
      },
      async createThread() {
        createCalls += 1;
        return typedFailure(409, { error: "session_changed" });
      },
      startTurn() {
        throw new Error("session recovery must not start a turn");
      },
      async cancelTurn(turnID) {
        return { turnID, canceled: false };
      },
    },
  };

  const { context, document } = await runRenderer(bridge, desktopBridge);
  document.byId.get("new-thread-button").click();
  document.byId.get("new-thread-form").submit();
  await settle();
  await settle();

  assert.equal(createCalls, 1);
  assert.equal(authReads, 2);
  assert.equal(threadReads, 2);
  assert.equal(skillReads, 2);
  assert.equal(document.byId.get("new-thread-form").hidden, true);
  assert.equal(vm.runInContext("state.createDraft", context), null);
  assert.match(document.byId.get("status-card").textContent, /account changed/i);
  assert.match(document.byId.get("status-card").textContent, /creation was not replayed/i);
}

async function testCreateRejectsForeignUUIDAndMode() {
  const invalidThreads = [
    createdThread("00000000-0000-4000-8000-000000000099", "Foreign UUID"),
    createdThread(
      "00000000-0000-4000-8000-000000000001",
      "Foreign mode",
      "writer"
    ),
  ];
  for (const invalidThread of invalidThreads) {
    const bridge = {
      async fetch(pathname) {
        if (pathname === "/auth/status") {
          return response({ state: "authenticated", updated_at: "2026-08-06T00:00:00Z" });
        }
        if (pathname === "/agent/threads?include_paused=false") {
          return response({ items: [] });
        }
        throw new Error(`unexpected fetch path ${pathname}`);
      },
    };
    const desktopBridge = {
      agent: {
        async uploadThreadFile() {
          throw new Error("uploadThreadFile is not exercised by this test");
        },
        async listSkills() {
          return typedSuccess(pptCatalog());
        },
        async createThread() {
          return typedSuccess({
            state: "ready",
            created: false,
            thread: invalidThread,
          });
        },
        startTurn() {
          throw new Error("malformed create response must not start a turn");
        },
        async cancelTurn(turnID) {
          return { turnID, canceled: false };
        },
      },
    };

    const { context, document } = await runRenderer(bridge, desktopBridge);
    document.byId.get("new-thread-button").click();
    document.byId.get("new-thread-form").submit();
    await settle();
    assert.equal(vm.runInContext("state.selectedThreadUUID", context), null);
    assert.match(document.byId.get("new-thread-error").textContent, /same identity/i);
    assert.doesNotMatch(document.byId.get("thread-list").textContent, /Foreign/);
  }
}

await testMissingBridge();
await testAuthenticatedCacheRead();
await testUnauthenticatedLogin();
await testResumesAndCancelsPasswordLogin();
await testInvalidCredentialsStayRetryableAndClearPassword();
await testCancelFencesLatePasswordCompletion();
await testAmbiguousPasswordResponseReconcilesSessionWithoutReplay();
await testRejectsMalformedAuthStatus();
await testRejectsMalformedThreadList();
await testRejectsMalformedThreadCountAndTimestamp();
await testRejectsMalformedMessages();
await testRejectsMalformedMessageTimestamps();
await testRejectsMalformedLoginTransactionResult();
await testRedactsErrorStatusMessages();
await testCachedStreamingStatesRenderPartialAndRejectUnknown();
await testThreadGroupingAndSearch();
await testThreadSearchIsHiddenWithNothingToFilter();
await testTaskContextPanelRendersOnLoad();
await testShimInterceptsExternalLinks();
await testAssistantMarkdownIsRenderedAsElements();
await testRetrievedContextIsShownAndResetPerTurn();
await testShimValidatesRetrievalPayloads();
await testStagedAttachmentsAreSentWithTheTurn();
await testSynchronousTurnCallbacksAreBufferedUntilOpenResult();
await testAgentTurnStreamsAndReconciles();
await testLateThreadHistoryCannotContaminateSelection();
await testThreadSwitchCancelsAndFencesOldTurn();
await testStopTurnIsSingleShot();
await testInitialTurnBusyRefreshesRecoveryWithoutReplay();
await testCancelAckFailureShowsLocalStopAndRefreshesRecovery();
await testSSESessionChangedClearsPromptWithoutReplay();
await testCatalog409UsesSessionChangedRecovery();
await testRejectsMalformedAgentContractsWithoutLeakingPayload();
await testRejectsLegacyOpenAgentEventShapes();
await testRejectsMalformedCatalogResult();
await testRecoverableTurnRequiresExplicitResumeAndHandlesBusy();
await testRecoverableTurnDismissIsExplicitAndIdempotent();
await testRecoverableErrorResultIsSanitized();
await testMalformedRecoverableTurnDoesNotLeakOrRender();
await testCreatesThreadOnceAndFocusesComposer();
await testCreateRetriesKeepUUIDAndAcceptCurrentReplayRow();
await testPermanentCreateFailuresDoNotOfferSameIdentityRetry();
await testPausedCreateReplayRequiresExplicitCancel();
await testCreateEscapeFencesLateCompletion();
await testCreateSessionChangedUsesUnifiedRecovery();
await testCreateRejectsForeignUUIDAndMode();

console.log("ok bundled renderer behavior");
