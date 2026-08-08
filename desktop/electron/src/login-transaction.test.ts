import assert from "node:assert/strict";
import { Buffer } from "node:buffer";
import { describe, it } from "node:test";

import {
  assertLoginPasswordIPCArgs,
  assertNoLoginTransactionIPCArgs,
  beginLoginTransaction,
  cancelLoginTransaction,
  clearLoginPasswordIPCValue,
  getLoginTransactionStatus,
  LoginTransactionError,
  loginTransactionErrorCode,
  loginTransactionFailureResult,
  MainLoginTransactionSession,
  submitLoginTransactionPassword,
  type LoginTransactionDependencies,
  type LoginTransactionResult,
  type LoginTransactionRuntime,
} from "./login-transaction";

const runtime: LoginTransactionRuntime = {
  sidecarPort: 49152,
  localToken: "test-local-token",
};

const flowA = Buffer.alloc(32, 0x31).toString("base64url");
const flowB = Buffer.alloc(32, 0x42).toString("base64url");
const flowC = Buffer.alloc(32, 0x53).toString("base64url");

function rawJSONResponse(raw: string, init: ResponseInit = {}): Response {
  return new Response(raw, {
    status: 200,
    headers: { "Content-Type": "application/json" },
    ...init,
  });
}

function resultResponse(
  value: LoginTransactionResult,
  init: ResponseInit = {}
): Response {
  return rawJSONResponse(JSON.stringify(value), init);
}

function deferred<T>(): {
  promise: Promise<T>;
  resolve: (value: T) => void;
} {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((fulfill) => {
    resolve = fulfill;
  });
  return { promise, resolve };
}

async function expectCode(
  action: () => Promise<unknown>,
  code: string
): Promise<LoginTransactionError> {
  let observed: LoginTransactionError | null = null;
  await assert.rejects(action, (error: unknown) => {
    assert.ok(error instanceof LoginTransactionError);
    assert.equal(error.code, code);
    observed = error;
    return true;
  });
  assert.ok(observed);
  return observed;
}

describe("Desktop login transaction", () => {
  it("pins every operation to its Main-only method, path, and headers", async () => {
    const requests: Array<{ input: string; init: RequestInit }> = [];
    const responses: LoginTransactionResult[] = [
      { state: "awaiting_password" },
      { state: "submitting" },
      { state: "authenticated" },
      { state: "idle", error: "canceled" },
    ];
    const deps: LoginTransactionDependencies = {
      request: async (input, init) => {
        requests.push({ input, init });
        const response = responses.shift();
        assert.ok(response);
        const successStatus = requests.length === 1 ? 201 : 200;
        return resultResponse(response, {
          status: "error" in response ? 409 : successStatus,
          headers: { "Content-Type": "application/json; charset=utf-8" },
        });
      },
    };

    assert.deepEqual(await beginLoginTransaction(runtime, deps, flowA), {
      state: "awaiting_password",
    });
    assert.deepEqual(await getLoginTransactionStatus(runtime, deps), {
      state: "submitting",
    });
    assert.deepEqual(
      await submitLoginTransactionPassword(
        runtime,
        deps,
        flowA,
        {
          email: "user@example.test",
          password: "local test phrase",
        }
      ),
      { state: "authenticated" }
    );
    assert.deepEqual(await cancelLoginTransaction(runtime, deps, flowA), {
      state: "idle",
      error: "canceled",
    });

    assert.deepEqual(
      requests.map(({ input, init }) => ({
        input,
        method: init.method,
        body: init.body,
        credentials: init.credentials,
        redirect: init.redirect,
      })),
      [
        {
          input: "http://127.0.0.1:49152/auth/login-transaction",
          method: "POST",
          body: undefined,
          credentials: "omit",
          redirect: "error",
        },
        {
          input: "http://127.0.0.1:49152/auth/login-transaction",
          method: "GET",
          body: undefined,
          credentials: "omit",
          redirect: "error",
        },
        {
          input: "http://127.0.0.1:49152/auth/login-transaction/password",
          method: "POST",
          body: JSON.stringify({
            email: "user@example.test",
            password: "local test phrase",
          }),
          credentials: "omit",
          redirect: "error",
        },
        {
          input: "http://127.0.0.1:49152/auth/login-transaction",
          method: "DELETE",
          body: undefined,
          credentials: "omit",
          redirect: "error",
        },
      ]
    );

    for (const request of requests) {
      const headers = new Headers(request.init.headers);
      assert.equal(headers.get("Accept"), "application/json");
      assert.equal(headers.get("Cache-Control"), "no-store");
      assert.equal(headers.get("X-Local-Token"), "test-local-token");
      assert.equal(headers.get("Origin"), null);
    }
    assert.equal(
      new Headers(requests[0].init.headers).get("X-WorkMax-Login-Flow"),
      flowA
    );
    assert.equal(
      new Headers(requests[1].init.headers).get("X-WorkMax-Login-Flow"),
      null
    );
    assert.equal(
      new Headers(requests[2].init.headers).get("X-WorkMax-Login-Flow"),
      flowA
    );
    assert.equal(
      new Headers(requests[3].init.headers).get("X-WorkMax-Login-Flow"),
      flowA
    );
    assert.equal(new Headers(requests[0].init.headers).get("Content-Type"), null);
    assert.equal(new Headers(requests[1].init.headers).get("Content-Type"), null);
    assert.equal(
      new Headers(requests[2].init.headers).get("Content-Type"),
      "application/json"
    );
    assert.equal(new Headers(requests[3].init.headers).get("Content-Type"), null);
  });

  it("accepts only the closed public state and error envelopes", async () => {
    const states = [
      "idle",
      "awaiting_password",
      "submitting",
      "authenticated",
    ] as const;
    const errors = [
      "busy",
      "invalid_request",
      "invalid_credentials",
      "expired",
      "unavailable",
      "canceled",
    ] as const;

    for (const state of states) {
      const result = await getLoginTransactionStatus(runtime, {
        request: async () => resultResponse({ state }),
      });
      assert.deepEqual(result, { state });
      assert.deepEqual(Object.keys(result), ["state"]);
    }
    const errorStatuses = {
      busy: 409,
      invalid_request: 400,
      invalid_credentials: 401,
      expired: 410,
      unavailable: 503,
      canceled: 409,
    } as const;
    for (const error of errors) {
      const result = await getLoginTransactionStatus(runtime, {
        request: async () =>
          resultResponse(
            { state: "idle", error },
            { status: errorStatuses[error] }
          ),
      });
      assert.deepEqual(result, { state: "idle", error });
      assert.deepEqual(Object.keys(result).sort(), ["error", "state"]);
    }
  });

  it("rejects malformed, repeated, oversized, and non-UTF-8 responses", async () => {
    const malformedResponses: Array<() => Response> = [
      () =>
        new Response('{"state":"idle"}', {
          status: 200,
          headers: { "Content-Type": "text/plain" },
        }),
      () =>
        new Response('{"state":"idle"}', {
          status: 200,
          headers: {
            "Content-Type": "application/json, application/json",
          },
        }),
      () => rawJSONResponse('{"state":"idle","state":"authenticated"}'),
      () => rawJSONResponse('{"state":"idle","st\\u0061te":"authenticated"}'),
      () => rawJSONResponse('{"state":"idle","error":"busy","error":"expired"}'),
      () => rawJSONResponse('{"state":"idle","extra":"value"}'),
      () => rawJSONResponse(`{"state":"idle","flow_id":"${flowA}"}`),
      () => rawJSONResponse('{"error":"busy"}'),
      () => rawJSONResponse('{"state":"pending"}'),
      () => rawJSONResponse('{"state":"idle","error":"private_failure"}'),
      () => rawJSONResponse('{"state":"idle","error":"busy"}'),
      () =>
        rawJSONResponse('{"state":"idle","error":"busy"}', {
          status: 400,
        }),
      () => rawJSONResponse('{"state":"idle"}', { status: 201 }),
      () =>
        rawJSONResponse('{"state":"idle"}', {
          status: 503,
          statusText: "Service Unavailable",
        }),
      () => rawJSONResponse('{"state":1}'),
      () => rawJSONResponse('{"state":"idle","error":true}'),
      () => rawJSONResponse('[{"state":"idle"}]'),
      () => rawJSONResponse('{"state":{"nested":"idle"}}'),
      () => rawJSONResponse('{"state":"idle",}'),
      () => rawJSONResponse('{"state":"idle"} trailing'),
      () => rawJSONResponse(""),
      () =>
        new Response('{"state":"idle"}', {
          status: 200,
          headers: {
            "Content-Type": "application/json",
            "Content-Length": "4097",
          },
        }),
      () =>
        new Response('{"state":"idle"}', {
          status: 200,
          headers: {
            "Content-Type": "application/json",
            "Content-Length": "01",
          },
        }),
      () =>
        new Response('{"state":"idle"}', {
          status: 200,
          headers: {
            "Content-Type": "application/json",
            "Content-Length": "1",
          },
        }),
      () =>
        new Response(new Uint8Array([0x7b, 0x22, 0x80, 0x22, 0x3a, 0x31, 0x7d]), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      () =>
        new Response("x".repeat(4097), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      () =>
        new Response(null, {
          status: 204,
          headers: { "Content-Type": "application/json" },
        }),
    ];

    for (const makeResponse of malformedResponses) {
      const error = await expectCode(
        () =>
          getLoginTransactionStatus(runtime, {
            request: async () => makeResponse(),
          }),
        "invalid-sidecar-response"
      );
      assert.equal(error.message.includes("private_failure"), false);
    }
  });

  it("reads streaming responses with a hard byte bound", async () => {
    const validBody = new TextEncoder().encode('{"state":"authenticated"}');
    const validStream = new ReadableStream<Uint8Array>({
      start(controller) {
        controller.enqueue(validBody.slice(0, 10));
        controller.enqueue(validBody.slice(10));
        controller.close();
      },
    });
    assert.deepEqual(
      await getLoginTransactionStatus(runtime, {
        request: async () =>
          new Response(validStream, {
            status: 200,
            headers: {
              "Content-Type": "application/json",
              "Content-Length": String(validBody.byteLength),
            },
          }),
      }),
      { state: "authenticated" }
    );

    const oversizedStream = new ReadableStream<Uint8Array>({
      start(controller) {
        controller.enqueue(new Uint8Array(4096));
        controller.enqueue(new Uint8Array(1));
        controller.close();
      },
    });
    await expectCode(
      () =>
        getLoginTransactionStatus(runtime, {
          request: async () =>
            new Response(oversizedStream, {
              status: 200,
              headers: { "Content-Type": "application/json" },
            }),
        }),
      "invalid-sidecar-response"
    );
  });

  it("validates exact password IPC input by UTF-8 bytes before any request", async () => {
    assert.deepEqual(
      assertLoginPasswordIPCArgs([
        { email: "u@example.test", password: "three words here" },
      ]),
      { email: "u@example.test", password: "three words here" }
    );
    assert.doesNotThrow(() => assertNoLoginTransactionIPCArgs([]));

    const invalidIPCArgs: unknown[][] = [
      [],
      [{ email: "u@example.test", password: "value" }, "extra"],
      [null],
      [[]],
      [{ email: "u@example.test" }],
      [{ password: "value" }],
      [{ email: 1, password: "value" }],
      [{ email: "u@example.test", password: 1 }],
      [{ email: "u@example.test", password: "value", extra: true }],
      [{ email: "ab", password: "value" }],
      [{ email: "abc", password: "value" }],
      [{ email: " u@example.test", password: "value" }],
      [{ email: "u@example.test ", password: "value" }],
      [{ email: "a".repeat(321), password: "value" }],
      [{ email: "u@example.test", password: "" }],
      [{ email: "u@example.test", password: "a".repeat(1025) }],
      [{ email: "u@example.test\n", password: "value" }],
      [{ email: "u@example.test", password: "value\u0085" }],
      [{ email: "bad\ud800", password: "value" }],
      [{ email: "u@example.test", password: "bad\udc00" }],
    ];
    for (const args of invalidIPCArgs) {
      assert.throws(
        () => assertLoginPasswordIPCArgs(args),
        (error: unknown) => {
          assert.ok(error instanceof LoginTransactionError);
          assert.equal(error.code, "invalid-ipc-request");
          return true;
        }
      );
    }
    assert.throws(
      () => assertNoLoginTransactionIPCArgs([{}]),
      (error: unknown) => {
        assert.ok(error instanceof LoginTransactionError);
        assert.equal(error.code, "invalid-ipc-request");
        return true;
      }
    );

    assert.doesNotThrow(() =>
      assertLoginPasswordIPCArgs([
        { email: `${"é".repeat(156)}@x.com`, password: "é".repeat(512) },
      ])
    );
    assert.throws(() =>
      assertLoginPasswordIPCArgs([
        { email: `${"é".repeat(158)}@x.com`, password: "value" },
      ])
    );
    assert.throws(() =>
      assertLoginPasswordIPCArgs([
        { email: "abc", password: "é".repeat(513) },
      ])
    );

    let requests = 0;
    await expectCode(
      () =>
        submitLoginTransactionPassword(
          runtime,
          {
            request: async () => {
              requests += 1;
              return resultResponse({ state: "idle" });
            },
          },
          flowA,
          { email: "ab", password: "value" }
        ),
      "invalid-login-input"
    );
    assert.equal(requests, 0);
  });

  it("rejects non-canonical local flow IDs before any mutation request", async () => {
    const invalidFlowIDs = [
      "",
      `${flowA}=`,
      flowA.slice(0, -1),
      `${flowA}A`,
      ` ${flowA}`,
      "_".repeat(43),
      "a".repeat(43),
      `${"A".repeat(42)}+`,
    ];
    let requests = 0;
    const deps: LoginTransactionDependencies = {
      request: async () => {
        requests += 1;
        return resultResponse({ state: "idle" });
      },
    };
    for (const invalidFlowID of invalidFlowIDs) {
      await expectCode(
        () => beginLoginTransaction(runtime, deps, invalidFlowID),
        "invalid-local-flow"
      );
    }
    await expectCode(
      () =>
        submitLoginTransactionPassword(runtime, deps, "not-a-flow", {
          email: "user@example.test",
          password: "must-not-serialize",
        }),
      "invalid-local-flow"
    );
    await expectCode(
      () => cancelLoginTransaction(runtime, deps, "not-a-flow"),
      "invalid-local-flow"
    );
    assert.equal(requests, 0);
    assert.deepEqual(
      loginTransactionFailureResult(
        new LoginTransactionError("invalid-local-flow", "private flow detail")
      ),
      { state: "idle", error: "invalid_request" }
    );
  });

  it("retains one Begin candidate across transport ambiguity and busy replay", async () => {
    const observedFlowIDs: Array<string | null> = [];
    let requests = 0;
    let generated = 0;
    const session = new MainLoginTransactionSession(
      runtime,
      {
        request: async (_input, init) => {
          requests += 1;
          observedFlowIDs.push(
            new Headers(init.headers).get("X-WorkMax-Login-Flow")
          );
          if (requests === 1) {
            throw new Error("ambiguous begin transport");
          }
          if (requests === 2) {
            return resultResponse(
              { state: "awaiting_password", error: "busy" },
              { status: 409 }
            );
          }
          if (requests === 3) {
            return resultResponse(
              { state: "awaiting_password", error: "invalid_credentials" },
              { status: 401 }
            );
          }
          return resultResponse({ state: "idle" });
        },
      },
      () => {
        generated += 1;
        return flowA;
      }
    );

    await expectCode(() => session.begin(), "sidecar-unavailable");
    assert.deepEqual(await session.begin(), {
      state: "awaiting_password",
      error: "busy",
    });
    assert.deepEqual(
      await session.submitPassword({
        email: "user@example.test",
        password: "candidate-password",
      }),
      { state: "awaiting_password", error: "invalid_credentials" }
    );
    assert.equal(generated, 1);
    assert.deepEqual(observedFlowIDs, [flowA, flowA, flowA]);
  });

  it("serializes exact Cancel after an in-flight Begin of the same flow", async () => {
    const delayedBeginA = deferred<Response>();
    const observed: string[] = [];
    let generatorIndex = 0;
    const generated = [flowA, flowB];
    const session = new MainLoginTransactionSession(
      runtime,
      {
        request: async (input, init) => {
          const flowID = new Headers(init.headers).get(
            "X-WorkMax-Login-Flow"
          );
          observed.push(`${init.method}:${flowID ?? "none"}`);
          if (
            init.method === "POST" &&
            input.endsWith("/auth/login-transaction") &&
            flowID === flowA
          ) {
            return delayedBeginA.promise;
          }
          if (init.method === "DELETE") {
            assert.equal(flowID, flowA);
            return resultResponse({ state: "idle" });
          }
          assert.equal(flowID, flowB);
          return resultResponse(
            { state: "awaiting_password" },
            { status: 201 }
          );
        },
      },
      () => {
        const value = generated[generatorIndex];
        generatorIndex += 1;
        assert.ok(value);
        return value;
      }
    );

    const firstBegin = session.begin();
    const repeatedBegin = session.begin();
    const cancel = session.cancel();
    await Promise.resolve();
    assert.deepEqual(observed, [`POST:${flowA}`]);

    delayedBeginA.resolve(
      resultResponse({ state: "awaiting_password" }, { status: 201 })
    );
    assert.deepEqual(await firstBegin, { state: "awaiting_password" });
    assert.deepEqual(await repeatedBegin, { state: "awaiting_password" });
    assert.deepEqual(await cancel, { state: "idle" });
    assert.deepEqual(observed, [`POST:${flowA}`, `DELETE:${flowA}`]);

    assert.deepEqual(await session.begin(), { state: "awaiting_password" });
    assert.deepEqual(observed, [
      `POST:${flowA}`,
      `DELETE:${flowA}`,
      `POST:${flowB}`,
    ]);
    assert.equal(generatorIndex, 2);
  });

  it("keeps B active when a delayed password from canceled A completes", async () => {
    const delayedPasswordA = deferred<Response>();
    const passwordFlowIDs: string[] = [];
    let beginCount = 0;
    let generatorIndex = 0;
    const generated = [flowA, flowB];
    const session = new MainLoginTransactionSession(
      runtime,
      {
        request: async (input, init) => {
          const method = init.method;
          const flowID = new Headers(init.headers).get(
            "X-WorkMax-Login-Flow"
          );
          if (method === "POST" && input.endsWith("/auth/login-transaction")) {
            beginCount += 1;
            return resultResponse(
              { state: "awaiting_password" },
              { status: 201 }
            );
          }
          if (method === "DELETE") {
            assert.equal(flowID, flowA);
            return resultResponse({ state: "idle" });
          }
          assert.equal(method, "POST");
          assert.ok(flowID);
          passwordFlowIDs.push(flowID);
          if (flowID === flowA) {
            return delayedPasswordA.promise;
          }
          return resultResponse(
            { state: "awaiting_password", error: "invalid_credentials" },
            { status: 401 }
          );
        },
      },
      () => {
        const value = generated[generatorIndex];
        generatorIndex += 1;
        assert.ok(value);
        return value;
      }
    );

    await session.begin();
    const stalePassword = session.submitPassword({
      email: "flow-a@example.test",
      password: "flow-a-password",
    });
    await session.cancel();
    await session.begin();
    delayedPasswordA.resolve(
      resultResponse(
        { state: "awaiting_password", error: "invalid_request" },
        { status: 400 }
      )
    );
    assert.deepEqual(await stalePassword, {
      state: "awaiting_password",
      error: "invalid_request",
    });
    assert.deepEqual(
      await session.submitPassword({
        email: "flow-b@example.test",
        password: "flow-b-password",
      }),
      { state: "awaiting_password", error: "invalid_credentials" }
    );
    assert.equal(beginCount, 2);
    assert.deepEqual(passwordFlowIDs, [flowA, flowB]);
  });

  it("keeps B active when exact Cancel A is delayed past Begin B", async () => {
    const delayedCancelA = deferred<Response>();
    let generatorIndex = 0;
    const generated = [flowA, flowB, flowC];
    const observed: Array<{ method: string | undefined; flowID: string | null }> = [];
    const session = new MainLoginTransactionSession(
      runtime,
      {
        request: async (input, init) => {
          const flowID = new Headers(init.headers).get(
            "X-WorkMax-Login-Flow"
          );
          observed.push({ method: init.method, flowID });
          if (init.method === "DELETE" && flowID === flowA) {
            return delayedCancelA.promise;
          }
          if (init.method === "POST" && input.endsWith("/password")) {
            return resultResponse(
              { state: "awaiting_password", error: "invalid_credentials" },
              { status: 401 }
            );
          }
          return resultResponse(
            { state: "awaiting_password" },
            { status: 201 }
          );
        },
      },
      () => {
        const value = generated[generatorIndex];
        generatorIndex += 1;
        assert.ok(value);
        return value;
      }
    );

    await session.begin();
    const staleCancel = session.cancel();
    await session.begin();
    delayedCancelA.resolve(
      resultResponse(
        { state: "awaiting_password", error: "invalid_request" },
        { status: 400 }
      )
    );
    assert.deepEqual(await staleCancel, {
      state: "awaiting_password",
      error: "invalid_request",
    });
    await session.submitPassword({
      email: "flow-b@example.test",
      password: "flow-b-password",
    });
    assert.deepEqual(observed, [
      { method: "POST", flowID: flowA },
      { method: "DELETE", flowID: flowA },
      { method: "POST", flowID: flowB },
      { method: "POST", flowID: flowB },
    ]);
    assert.equal(generatorIndex, 2);
  });

  it("clears only safe writable IPC password clones", () => {
    const mutable = {
      email: "user@example.test",
      password: "credential-copy",
    };
    clearLoginPasswordIPCValue(mutable);
    assert.equal(mutable.password, "");

    const frozen = Object.freeze({ password: "frozen-copy" });
    clearLoginPasswordIPCValue(frozen);
    assert.equal(frozen.password, "frozen-copy");

    let getterCalls = 0;
    const accessor = Object.create(null) as Record<string, unknown>;
    Object.defineProperty(accessor, "password", {
      get() {
        getterCalls += 1;
        return "accessor-copy";
      },
    });
    clearLoginPasswordIPCValue(accessor);
    assert.equal(getterCalls, 0);
  });

  it("maps internal failures to closed public errors without leaking details or retrying", async () => {
    let requests = 0;
    const error = await expectCode(
      () =>
        beginLoginTransaction(runtime, {
          request: async () => {
            requests += 1;
            throw new Error("private transport diagnostic");
          },
        }, flowA),
      "sidecar-unavailable"
    );
    assert.equal(requests, 1);
    assert.equal(error.message.includes("private transport diagnostic"), false);
    assert.deepEqual(loginTransactionFailureResult(error), {
      state: "idle",
      error: "unavailable",
    });
    assert.deepEqual(
      loginTransactionFailureResult(new Error("private transport diagnostic")),
      { state: "idle", error: "unavailable" }
    );

    let invalidIPC: unknown;
    try {
      assertNoLoginTransactionIPCArgs(["unexpected"]);
    } catch (caught) {
      invalidIPC = caught;
    }
    assert.equal(loginTransactionErrorCode(invalidIPC), "invalid-ipc-request");
    assert.deepEqual(loginTransactionFailureResult(invalidIPC), {
      state: "idle",
      error: "invalid_request",
    });
  });

  it("rejects invalid trusted runtime before issuing a request", async () => {
    const invalidRuntimes: LoginTransactionRuntime[] = [
      { sidecarPort: 0, localToken: "token" },
      { sidecarPort: 65536, localToken: "token" },
      { sidecarPort: 49152.5, localToken: "token" },
      { sidecarPort: 49152, localToken: "" },
      { sidecarPort: 49152, localToken: " token" },
      { sidecarPort: 49152, localToken: "token\nvalue" },
      { sidecarPort: 49152, localToken: "x".repeat(4097) },
      { sidecarPort: 49152, localToken: "bad\ud800" },
    ];
    let requests = 0;
    for (const invalidRuntime of invalidRuntimes) {
      await expectCode(
        () =>
          beginLoginTransaction(invalidRuntime, {
            request: async () => {
              requests += 1;
              return resultResponse({ state: "idle" });
            },
          }, flowA),
        "invalid-runtime"
      );
    }
    assert.equal(requests, 0);
  });
});
