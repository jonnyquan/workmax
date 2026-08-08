import { Buffer } from "node:buffer";

const LOGIN_TRANSACTION_PATH = "/auth/login-transaction";
const LOGIN_TRANSACTION_PASSWORD_PATH = "/auth/login-transaction/password";
const LOGIN_TRANSACTION_FLOW_HEADER = "X-WorkMax-Login-Flow";
const MAX_LOGIN_TRANSACTION_RESPONSE_BYTES = 4 * 1024;
const MAX_LOCAL_TOKEN_BYTES = 4 * 1024;
const MIN_EMAIL_BYTES = 3;
const MAX_EMAIL_BYTES = 320;
const MIN_PASSWORD_BYTES = 1;
const MAX_PASSWORD_BYTES = 1024;

const LOGIN_TRANSACTION_STATES = new Set<LoginTransactionState>([
  "idle",
  "awaiting_password",
  "submitting",
  "authenticated",
]);

const LOGIN_TRANSACTION_ERRORS = new Set<LoginTransactionPublicError>([
  "busy",
  "invalid_request",
  "invalid_credentials",
  "expired",
  "unavailable",
  "canceled",
]);

export interface LoginTransactionRuntime {
  sidecarPort: number;
  localToken: string;
}

export interface LoginTransactionDependencies {
  request: (input: string, init: RequestInit) => Promise<Response>;
}

export type LoginTransactionState =
  | "idle"
  | "awaiting_password"
  | "submitting"
  | "authenticated";

export type LoginTransactionPublicError =
  | "busy"
  | "invalid_request"
  | "invalid_credentials"
  | "expired"
  | "unavailable"
  | "canceled";

export type LoginTransactionResult =
  | { state: LoginTransactionState }
  | { state: LoginTransactionState; error: LoginTransactionPublicError };

export interface LoginPasswordInput {
  email: string;
  password: string;
}

type LoginTransactionMethod = "GET" | "POST" | "DELETE";

export class LoginTransactionError extends Error {
  readonly code: string;

  constructor(code: string, message: string) {
    super(message);
    this.name = "LoginTransactionError";
    this.code = code;
  }
}

/** Starts a fresh local login transaction through the Main-only route. */
export function beginLoginTransaction(
  runtime: LoginTransactionRuntime,
  deps: LoginTransactionDependencies,
  flowID: string
): Promise<LoginTransactionResult> {
  return requestLoginTransaction(
    runtime,
    deps,
    "POST",
    LOGIN_TRANSACTION_PATH,
    201,
    undefined,
    flowID
  );
}

/** Reads the current local login transaction state. */
export function getLoginTransactionStatus(
  runtime: LoginTransactionRuntime,
  deps: LoginTransactionDependencies
): Promise<LoginTransactionResult> {
  return requestLoginTransaction(
    runtime,
    deps,
    "GET",
    LOGIN_TRANSACTION_PATH,
    200
  );
}

/** Submits password credentials without exposing the privileged route itself. */
export async function submitLoginTransactionPassword(
  runtime: LoginTransactionRuntime,
  deps: LoginTransactionDependencies,
  flowID: string,
  input: LoginPasswordInput
): Promise<LoginTransactionResult> {
  validateLocalFlowID(flowID);
  const validated = validateLoginPasswordInput(input);
  let body = JSON.stringify(validated);
  validated.password = "";
  try {
    return await requestLoginTransaction(
      runtime,
      deps,
      "POST",
      LOGIN_TRANSACTION_PASSWORD_PATH,
      200,
      body,
      flowID
    );
  } finally {
    body = "";
  }
}

/** Cancels the current local login transaction. */
export function cancelLoginTransaction(
  runtime: LoginTransactionRuntime,
  deps: LoginTransactionDependencies,
  flowID: string
): Promise<LoginTransactionResult> {
  return requestLoginTransaction(
    runtime,
    deps,
    "DELETE",
    LOGIN_TRANSACTION_PATH,
    200,
    undefined,
    flowID
  );
}

/**
 * Owns the Main-only login flow generation. A candidate survives a lost or
 * busy Begin response and is promoted to active only after the Sidecar's 201
 * success envelope. Neither identifier is exposed through IPC results.
 */
export class MainLoginTransactionSession {
  private activeFlowID: string | null = null;
  private beginCandidateFlowID: string | null = null;
  private readonly beginInFlight = new Map<
    string,
    Promise<LoginTransactionResult>
  >();

  constructor(
    private readonly runtime: LoginTransactionRuntime,
    private readonly deps: LoginTransactionDependencies,
    private readonly generateFlowID: () => string
  ) {}

  async begin(): Promise<LoginTransactionResult> {
    let flowID = this.currentFlowID();
    if (flowID === null) {
      flowID = this.generateFlowID();
      validateLocalFlowID(flowID);
      this.beginCandidateFlowID = flowID;
    }

    const existing = this.beginInFlight.get(flowID);
    if (existing !== undefined) {
      return existing;
    }
    const operation = this.performBegin(flowID);
    this.beginInFlight.set(flowID, operation);
    try {
      return await operation;
    } finally {
      if (this.beginInFlight.get(flowID) === operation) {
        this.beginInFlight.delete(flowID);
      }
    }
  }

  private async performBegin(flowID: string): Promise<LoginTransactionResult> {
    const result = await beginLoginTransaction(this.runtime, this.deps, flowID);
    if (
      this.beginCandidateFlowID === flowID &&
      !("error" in result) &&
      result.state === "awaiting_password"
    ) {
      this.activeFlowID = flowID;
      this.beginCandidateFlowID = null;
    }
    this.clearTerminalFlow(flowID, result);
    return result;
  }

  async status(): Promise<LoginTransactionResult> {
    const flowID = this.currentFlowID();
    const result = await getLoginTransactionStatus(this.runtime, this.deps);
    if (flowID !== null) {
      this.clearTerminalFlow(flowID, result);
    }
    return result;
  }

  async submitPassword(
    input: LoginPasswordInput
  ): Promise<LoginTransactionResult> {
    const flowID = this.currentFlowID();
    if (flowID === null) {
      throw invalidLocalFlowState();
    }
    const result = await submitLoginTransactionPassword(
      this.runtime,
      this.deps,
      flowID,
      input
    );
    this.clearTerminalFlow(flowID, result);
    return result;
  }

  async cancel(): Promise<LoginTransactionResult> {
    const flowID = this.currentFlowID();
    if (flowID === null) {
      return { state: "idle" };
    }
    // Retire A before awaiting its exact DELETE so a concurrent Begin can own
    // B. The Sidecar binding makes a delayed A request harmless to B.
    this.clearFlow(flowID);
    const pendingBegin = this.beginInFlight.get(flowID);
    if (pendingBegin !== undefined) {
      // Never let DELETE overtake the POST for the same generation. Ignore the
      // Begin outcome here: exact cancellation is still required after either
      // a definitive or ambiguous result.
      try {
        await pendingBegin;
      } catch {
        // The candidate was already retired; continue with exact cleanup.
      }
    }
    return cancelLoginTransaction(this.runtime, this.deps, flowID);
  }

  private currentFlowID(): string | null {
    return this.activeFlowID ?? this.beginCandidateFlowID;
  }

  private clearTerminalFlow(
    flowID: string,
    result: LoginTransactionResult
  ): void {
    if (result.state === "idle" || result.state === "authenticated") {
      this.clearFlow(flowID);
    }
  }

  private clearFlow(flowID: string): void {
    if (this.activeFlowID === flowID) {
      this.activeFlowID = null;
    }
    if (this.beginCandidateFlowID === flowID) {
      this.beginCandidateFlowID = null;
    }
  }
}

// Clear only a plain, writable IPC clone. This avoids retaining an additional
// credential copy in Main without invoking accessors on an unexpected object.
export function clearLoginPasswordIPCValue(value: unknown): void {
  if (
    value === null ||
    typeof value !== "object" ||
    Array.isArray(value) ||
    Object.getPrototypeOf(value) !== Object.prototype
  ) {
    return;
  }
  const descriptor = Object.getOwnPropertyDescriptor(value, "password");
  if (descriptor !== undefined && "value" in descriptor && descriptor.writable) {
    Reflect.set(value, "password", "");
  }
}

export function assertNoLoginTransactionIPCArgs(args: readonly unknown[]): void {
  if (args.length !== 0) {
    throw invalidIPCRequest();
  }
}

export function assertLoginPasswordIPCArgs(
  args: readonly unknown[]
): LoginPasswordInput {
  if (args.length !== 1) {
    throw invalidIPCRequest();
  }
  try {
    return validateLoginPasswordInput(args[0]);
  } catch {
    throw invalidIPCRequest();
  }
}

export function loginTransactionErrorCode(error: unknown): string {
  if (error instanceof LoginTransactionError) {
    return error.code;
  }
  return "unknown";
}

export function loginTransactionFailureResult(
  error: unknown
): LoginTransactionResult {
  const code = loginTransactionErrorCode(error);
  if (
    code === "invalid-ipc-request" ||
    code === "invalid-login-input" ||
    code === "invalid-local-flow"
  ) {
    return { state: "idle", error: "invalid_request" };
  }
  return { state: "idle", error: "unavailable" };
}

async function requestLoginTransaction(
  runtime: LoginTransactionRuntime,
  deps: LoginTransactionDependencies,
  method: LoginTransactionMethod,
  path: typeof LOGIN_TRANSACTION_PATH | typeof LOGIN_TRANSACTION_PASSWORD_PATH,
  successStatus: 200 | 201,
  body?: string,
  flowID?: string
): Promise<LoginTransactionResult> {
  validateRuntime(runtime);

  if (method === "GET") {
    if (flowID !== undefined) {
      throw invalidLocalFlowState();
    }
  } else {
    validateLocalFlowID(flowID);
  }

  const headers = new Headers({
    Accept: "application/json",
    "Cache-Control": "no-store",
    "X-Local-Token": runtime.localToken,
  });
  if (body !== undefined) {
    headers.set("Content-Type", "application/json");
  }
  if (flowID !== undefined) {
    headers.set(LOGIN_TRANSACTION_FLOW_HEADER, flowID);
  }

  let response: Response;
  let requestBody = body;
  try {
    response = await deps.request(
      `http://127.0.0.1:${runtime.sidecarPort}${path}`,
      {
        method,
        headers,
        body: requestBody,
        credentials: "omit",
        redirect: "error",
      }
    );
  } catch {
    throw new LoginTransactionError(
      "sidecar-unavailable",
      "The local sign-in service is unavailable."
    );
  } finally {
    requestBody = undefined;
    body = undefined;
  }

  return parseLoginTransactionResponse(response, successStatus);
}

async function parseLoginTransactionResponse(
  response: Response,
  successStatus: 200 | 201
): Promise<LoginTransactionResult> {
  if (!isJSONContentType(response.headers.get("content-type"))) {
    throw invalidResponse();
  }

  const declaredLength = parseContentLength(
    response.headers.get("content-length")
  );
  const bytes = await readBoundedResponse(response, declaredLength);
  if (bytes.byteLength === 0) {
    throw invalidResponse();
  }

  let raw: string;
  try {
    raw = new TextDecoder("utf-8", { fatal: true }).decode(bytes);
  } catch {
    throw invalidResponse();
  }

  const value = parseUniqueStringObject(raw);
  const keys = Object.keys(value).sort();
  const state = value.state;
  if (!isLoginTransactionState(state)) {
    throw invalidResponse();
  }

  if (keys.length === 1 && keys[0] === "state") {
    if (response.status !== successStatus) {
      throw invalidResponse();
    }
    return { state };
  }
  if (
    keys.length === 2 &&
    keys[0] === "error" &&
    keys[1] === "state" &&
    isLoginTransactionPublicError(value.error)
  ) {
    if (response.status !== loginTransactionErrorStatus(value.error)) {
      throw invalidResponse();
    }
    return { state, error: value.error };
  }
  throw invalidResponse();
}

function loginTransactionErrorStatus(error: LoginTransactionPublicError): number {
  switch (error) {
    case "invalid_request":
      return 400;
    case "invalid_credentials":
      return 401;
    case "busy":
    case "canceled":
      return 409;
    case "expired":
      return 410;
    case "unavailable":
      return 503;
  }
}

function isJSONContentType(value: string | null): boolean {
  if (value === null || value.length > 128) {
    return false;
  }
  return /^application\/json(?:\s*;\s*charset\s*=\s*(?:utf-8|"utf-8"))?$/iu.test(
    value.trim()
  );
}

function parseContentLength(value: string | null): number | null {
  if (value === null) {
    return null;
  }
  if (!/^(?:0|[1-9][0-9]*)$/u.test(value)) {
    throw invalidResponse();
  }
  const length = Number(value);
  if (
    !Number.isSafeInteger(length) ||
    length < 0 ||
    length > MAX_LOGIN_TRANSACTION_RESPONSE_BYTES
  ) {
    throw invalidResponse();
  }
  return length;
}

async function readBoundedResponse(
  response: Response,
  declaredLength: number | null
): Promise<Uint8Array> {
  if (response.body === null) {
    throw invalidResponse();
  }

  const reader = response.body.getReader();
  const chunks: Uint8Array[] = [];
  let total = 0;
  try {
    while (true) {
      const { value, done } = await reader.read();
      if (done) {
        break;
      }
      if (!(value instanceof Uint8Array)) {
        throw invalidResponse();
      }
      total += value.byteLength;
      if (total > MAX_LOGIN_TRANSACTION_RESPONSE_BYTES) {
        await reader.cancel().catch(() => undefined);
        throw invalidResponse();
      }
      chunks.push(value);
    }
  } catch (error) {
    if (error instanceof LoginTransactionError) {
      throw error;
    }
    throw invalidResponse();
  }

  if (declaredLength !== null && declaredLength !== total) {
    throw invalidResponse();
  }
  const bytes = new Uint8Array(total);
  let offset = 0;
  for (const chunk of chunks) {
    bytes.set(chunk, offset);
    offset += chunk.byteLength;
  }
  return bytes;
}

/**
 * The public response is deliberately a tiny flat object. Parsing that shape
 * directly lets us reject repeated top-level fields before JSON semantics can
 * silently overwrite them.
 */
function parseUniqueStringObject(raw: string): Record<string, string> {
  let offset = skipJSONWhitespace(raw, 0);
  if (raw[offset] !== "{") {
    throw invalidResponse();
  }
  offset = skipJSONWhitespace(raw, offset + 1);

  const value: Record<string, string> = Object.create(null) as Record<
    string,
    string
  >;
  const seen = new Set<string>();
  if (raw[offset] === "}") {
    offset = skipJSONWhitespace(raw, offset + 1);
    if (offset !== raw.length) {
      throw invalidResponse();
    }
    return value;
  }

  while (offset < raw.length) {
    const keyToken = readJSONStringToken(raw, offset);
    const key = decodeJSONStringToken(keyToken.token);
    if (seen.has(key)) {
      throw invalidResponse();
    }
    seen.add(key);
    offset = skipJSONWhitespace(raw, keyToken.end);
    if (raw[offset] !== ":") {
      throw invalidResponse();
    }

    offset = skipJSONWhitespace(raw, offset + 1);
    const stringToken = readJSONStringToken(raw, offset);
    value[key] = decodeJSONStringToken(stringToken.token);
    offset = skipJSONWhitespace(raw, stringToken.end);

    if (raw[offset] === "}") {
      offset = skipJSONWhitespace(raw, offset + 1);
      if (offset !== raw.length) {
        throw invalidResponse();
      }
      return value;
    }
    if (raw[offset] !== ",") {
      throw invalidResponse();
    }
    offset = skipJSONWhitespace(raw, offset + 1);
  }

  throw invalidResponse();
}

function readJSONStringToken(
  raw: string,
  start: number
): { token: string; end: number } {
  if (raw[start] !== '"') {
    throw invalidResponse();
  }
  let offset = start + 1;
  while (offset < raw.length) {
    const code = raw.charCodeAt(offset);
    if (code === 0x22) {
      return { token: raw.slice(start, offset + 1), end: offset + 1 };
    }
    if (code < 0x20) {
      throw invalidResponse();
    }
    if (code === 0x5c) {
      offset += 1;
      const escape = raw[offset];
      if (escape === "u") {
        if (!/^[0-9a-f]{4}$/iu.test(raw.slice(offset + 1, offset + 5))) {
          throw invalidResponse();
        }
        offset += 5;
        continue;
      }
      if (escape === undefined || !'"\\/bfnrt'.includes(escape)) {
        throw invalidResponse();
      }
    }
    offset += 1;
  }
  throw invalidResponse();
}

function decodeJSONStringToken(token: string): string {
  try {
    const value: unknown = JSON.parse(token);
    if (typeof value !== "string") {
      throw invalidResponse();
    }
    return value;
  } catch (error) {
    if (error instanceof LoginTransactionError) {
      throw error;
    }
    throw invalidResponse();
  }
}

function skipJSONWhitespace(raw: string, start: number): number {
  let offset = start;
  while (
    raw[offset] === " " ||
    raw[offset] === "\t" ||
    raw[offset] === "\n" ||
    raw[offset] === "\r"
  ) {
    offset += 1;
  }
  return offset;
}

function validateLoginPasswordInput(value: unknown): LoginPasswordInput {
  if (value === null || typeof value !== "object" || Array.isArray(value)) {
    throw invalidLoginInput();
  }
  const keys = Reflect.ownKeys(value).sort((left, right) =>
    String(left).localeCompare(String(right))
  );
  if (
    keys.length !== 2 ||
    keys[0] !== "email" ||
    keys[1] !== "password"
  ) {
    throw invalidLoginInput();
  }

  const record = value as Record<string, unknown>;
  if (typeof record.email !== "string" || typeof record.password !== "string") {
    throw invalidLoginInput();
  }
  validateBoundedText(record.email, MIN_EMAIL_BYTES, MAX_EMAIL_BYTES);
  if (record.email.trim() !== record.email || !record.email.includes("@")) {
    throw invalidLoginInput();
  }
  validateBoundedText(record.password, MIN_PASSWORD_BYTES, MAX_PASSWORD_BYTES);
  return { email: record.email, password: record.password };
}

function validateBoundedText(value: string, minBytes: number, maxBytes: number): void {
  if (!hasWellFormedUTF16(value) || /\p{Cc}/u.test(value)) {
    throw invalidLoginInput();
  }
  const length = new TextEncoder().encode(value).byteLength;
  if (length < minBytes || length > maxBytes) {
    throw invalidLoginInput();
  }
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

function validateRuntime(runtime: LoginTransactionRuntime): void {
  const tokenBytes = new TextEncoder().encode(runtime.localToken).byteLength;
  if (
    !Number.isInteger(runtime.sidecarPort) ||
    runtime.sidecarPort < 1 ||
    runtime.sidecarPort > 65535 ||
    runtime.localToken === "" ||
    tokenBytes > MAX_LOCAL_TOKEN_BYTES ||
    runtime.localToken.trim() !== runtime.localToken ||
    !hasWellFormedUTF16(runtime.localToken) ||
    /\p{Cc}/u.test(runtime.localToken)
  ) {
    throw new LoginTransactionError(
      "invalid-runtime",
      "The local sign-in service is not configured correctly."
    );
  }
}

function validateLocalFlowID(value: unknown): asserts value is string {
  if (typeof value !== "string" || !/^[A-Za-z0-9_-]{43}$/u.test(value)) {
    throw invalidLocalFlowState();
  }
  const decoded = Buffer.from(value, "base64url");
  if (decoded.byteLength !== 32 || decoded.toString("base64url") !== value) {
    throw invalidLocalFlowState();
  }
}

function isLoginTransactionState(value: string | undefined): value is LoginTransactionState {
  return value !== undefined && LOGIN_TRANSACTION_STATES.has(value as LoginTransactionState);
}

function isLoginTransactionPublicError(
  value: string | undefined
): value is LoginTransactionPublicError {
  return (
    value !== undefined &&
    LOGIN_TRANSACTION_ERRORS.has(value as LoginTransactionPublicError)
  );
}

function invalidIPCRequest(): LoginTransactionError {
  return new LoginTransactionError(
    "invalid-ipc-request",
    "The sign-in request did not match the Desktop contract."
  );
}

function invalidLoginInput(): LoginTransactionError {
  return new LoginTransactionError(
    "invalid-login-input",
    "The sign-in credentials did not match the Desktop contract."
  );
}

function invalidLocalFlowState(): LoginTransactionError {
  return new LoginTransactionError(
    "invalid-local-flow",
    "The local sign-in flow did not match the Main process generation."
  );
}

function invalidResponse(): LoginTransactionError {
  return new LoginTransactionError(
    "invalid-sidecar-response",
    "The local sign-in service returned an invalid response."
  );
}
