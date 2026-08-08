import { existsSync, readFileSync, readdirSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const scriptDir = dirname(fileURLToPath(import.meta.url));
const repoRoot = resolve(scriptDir, "..", "..");
const manifestPath = resolve(repoRoot, "desktop/contracts/desktop-boundaries.v0.json");
const manifest = JSON.parse(readFileSync(manifestPath, "utf8"));

if (manifest.schemaVersion !== 0 || manifest.status !== "current-observed") {
  throw new Error("desktop boundary manifest must be current-observed schemaVersion 0");
}

const repositorySurfaces = manifest.repositorySurfaces;
if (
  !repositorySurfaces ||
  JSON.stringify(repositorySurfaces.clients) !== JSON.stringify(["desktop"]) ||
  JSON.stringify(repositorySurfaces.services) !== JSON.stringify(["server"]) ||
  JSON.stringify(repositorySurfaces.forbiddenTopLevel) !== JSON.stringify(["web", "admin"])
) {
  throw new Error("repository surfaces must be exactly server + desktop");
}
for (const retiredClient of repositorySurfaces.forbiddenTopLevel) {
  if (existsSync(resolve(repoRoot, retiredClient))) {
    throw new Error(`retired top-level client must stay absent: ${retiredClient}`);
  }
}

function assertUnique(items, key, label) {
  const seen = new Set();
  for (const item of items) {
    const value = key(item);
    if (!value) throw new Error(`${label} contains an empty identity`);
    if (seen.has(value)) throw new Error(`${label} contains duplicate ${value}`);
    seen.add(value);
  }
  return seen;
}

function collectRuntimeSources(directory) {
  const sources = new Map();
  for (const entry of readdirSync(directory, { withFileTypes: true })) {
    const entryPath = resolve(directory, entry.name);
    if (entry.isDirectory()) {
      for (const [nestedPath, source] of collectRuntimeSources(entryPath)) {
        sources.set(nestedPath, source);
      }
      continue;
    }
    if (
      entry.isFile() &&
      /\.(?:js|ts)$/u.test(entry.name) &&
      !/\.test\.(?:js|ts)$/u.test(entry.name)
    ) {
      sources.set(entryPath, readFileSync(entryPath, "utf8"));
    }
  }
  return sources;
}

const loopbackExpected = assertUnique(
  manifest.loopbackRoutes,
  (route) => `${route.method} ${route.path}`,
  "loopbackRoutes"
);
assertUnique(manifest.loopbackRoutes, (route) => route.id, "loopback route ids");

for (const route of manifest.loopbackRoutes) {
  if (route.credential !== "local-token") {
    throw new Error(`loopback route ${route.id} must use local-token credential policy`);
  }
  const policy = route.requestPolicy;
  if (!policy || policy.origin !== "absent") {
    throw new Error(`loopback route ${route.id} must reject browser Origin headers`);
  }
  if (!['forbidden', 'optional', 'required'].includes(policy.body)) {
    throw new Error(`loopback route ${route.id} has invalid body policy`);
  }
  if (!Array.isArray(policy.contentTypes)) {
    throw new Error(`loopback route ${route.id} contentTypes must be an array`);
  }
  if (!Number.isSafeInteger(policy.maxBodyBytes) || policy.maxBodyBytes < 0) {
    throw new Error(`loopback route ${route.id} has invalid maxBodyBytes`);
  }
  if (policy.body === "forbidden") {
    if (
      policy.maxBodyBytes !== 0 ||
      policy.contentTypes.length !== 0 ||
      policy.bodyTooLargeError !== undefined
    ) {
      throw new Error(`loopback route ${route.id} forbids a body but declares body metadata`);
    }
  } else {
    if (policy.maxBodyBytes === 0 || policy.contentTypes.length === 0) {
      throw new Error(`loopback route ${route.id} must bound and type accepted bodies`);
    }
    if (typeof policy.bodyTooLargeError !== "string" || policy.bodyTooLargeError === "") {
      throw new Error(`loopback route ${route.id} must declare an oversize error`);
    }
  }
  assertUnique(policy.contentTypes, (contentType) => contentType, `${route.id} contentTypes`);
  for (const contentType of policy.contentTypes) {
    if (!/^[a-z0-9!#$&^_.+-]+\/[a-z0-9!#$&^_.+-]+$/u.test(contentType)) {
      throw new Error(`loopback route ${route.id} has non-canonical content type ${contentType}`);
    }
  }
}

const sidecarSource = readFileSync(resolve(repoRoot, "server/desktop/server.go"), "utf8");
if (!sidecarSource.includes("registerCurrentSidecarRoutes(router, s, cfg.LocalToken)")) {
  throw new Error("sidecar server must register the per-route policy table");
}

const routePolicySource = readFileSync(
  resolve(repoRoot, "server/desktop/route_policy.go"),
  "utf8"
);
const loopbackActual = new Set();
const loopbackActualIDs = new Map();
for (const match of routePolicySource.matchAll(
  /newCurrentSidecarRoutePolicy\(\s*"([^"]+)"\s*,\s*http\.Method(Get|Post|Put|Patch|Delete)\s*,\s*"([^"]+)"/gu
)) {
  const [, id, methodName, path] = match;
  const method = methodName.toUpperCase();
  const identity = `${method} ${path}`;
  loopbackActual.add(identity);
  if (loopbackActualIDs.has(id)) {
    throw new Error(`sidecar route policy source contains duplicate id ${id}`);
  }
  loopbackActualIDs.set(id, identity);
}
assertSameSet("loopback route", loopbackExpected, loopbackActual);
for (const route of manifest.loopbackRoutes) {
  const actual = loopbackActualIDs.get(route.id);
  const expected = `${route.method} ${route.path}`;
  if (actual !== expected) {
    throw new Error(
      `loopback route id drift for ${route.id}: expected=${expected} actual=${actual ?? "missing"}`
    );
  }
}

const cloudRouteIdentities = assertUnique(
  manifest.cloudRoutes,
  (route) => `${route.method} ${route.path}`,
  "cloudRoutes"
);
const cloudRouteIDs = assertUnique(
  manifest.cloudRoutes,
  (route) => route.id,
  "cloud route ids"
);
for (const route of manifest.cloudRoutes) {
  if (!['GET', 'POST', 'PUT', 'PATCH', 'DELETE'].includes(route.method)) {
    throw new Error(`cloud route ${route.id} has unsupported method ${route.method}`);
  }
  if (!['sidecar-cloud-proxy', 'server-desktop-login-api'].includes(route.inventorySource)) {
    throw new Error(`cloud route ${route.id} has unsupported inventory source`);
  }
}

const sidecarProxyCloudRoutes = manifest.cloudRoutes.filter(
  (route) => route.inventorySource === "sidecar-cloud-proxy"
);
const sidecarProxyExpected = assertUnique(
  sidecarProxyCloudRoutes,
  (route) => `${route.method} ${route.path}`,
  "sidecar proxy cloudRoutes"
);

const cloudRouteSource = readFileSync(
  resolve(repoRoot, "server/desktop/cloud_proxy/cloud_routes.go"),
  "utf8"
);
const cloudPathConstants = new Map();
for (const match of cloudRouteSource.matchAll(
  /(CloudRoute[A-Za-z0-9]+)\s*=\s*"([^"]+)"/gu
)) {
  cloudPathConstants.set(match[1], match[2]);
}
const cloudActual = new Set();
const cloudActualIDs = new Map();
for (const match of cloudRouteSource.matchAll(
  /newCloudRouteSpec\(\s*"([^"]+)"\s*,\s*http\.Method(Get|Post|Put|Patch|Delete)\s*,\s*(CloudRoute[A-Za-z0-9]+)\s*\)/gu
)) {
  const [, id, methodName, pathConstant] = match;
  const path = cloudPathConstants.get(pathConstant);
  if (!path) {
    throw new Error(`cloud route ${id} references unknown path constant ${pathConstant}`);
  }
  const identity = `${methodName.toUpperCase()} ${path}`;
  cloudActual.add(identity);
  if (cloudActualIDs.has(id)) {
    throw new Error(`cloud route spec source contains duplicate id ${id}`);
  }
  cloudActualIDs.set(id, identity);
}
const sidecarProxyActual = new Set();
for (const route of sidecarProxyCloudRoutes) {
  const actual = cloudActualIDs.get(route.id);
  const expected = `${route.method} ${route.path}`;
  if (actual !== expected) {
    throw new Error(
      `cloud route id drift for ${route.id}: expected=${expected} actual=${actual ?? "missing"}`
    );
  }
  sidecarProxyActual.add(actual);
}
assertSameSet("sidecar proxy cloud Method+Path", sidecarProxyExpected, sidecarProxyActual);

const serverLoginRoutes = manifest.cloudRoutes.filter(
  (route) => route.inventorySource === "server-desktop-login-api"
);
const expectedServerLoginRoutes = [
  {
    id: "desktop.identity.login-transaction.create",
    method: "POST",
    path: "/api/v1/desktop/identity/login-transactions",
    relativePath: "/login-transactions",
    handler: "Create",
    credentialPolicy: "desktop-login-bootstrap",
    credential: "none",
    requestSecrets: ["oauth_state"],
    responseSecrets: ["transaction_secret"],
  },
  {
    id: "desktop.identity.login-transaction.status",
    method: "GET",
    path: "/api/v1/desktop/identity/login-transactions/:id",
    relativePath: "/login-transactions/:id",
    handler: "Status",
    credentialPolicy: "desktop-login-transaction",
    credential: "DesktopLogin transaction_secret",
    requestSecrets: ["transaction_secret"],
    responseSecrets: [],
  },
  {
    id: "desktop.identity.login-transaction.password",
    method: "POST",
    path: "/api/v1/desktop/identity/login-transactions/:id/password",
    relativePath: "/login-transactions/:id/password",
    handler: "CompletePassword",
    credentialPolicy: "desktop-login-transaction",
    credential: "DesktopLogin transaction_secret",
    requestSecrets: ["transaction_secret", "password"],
    responseSecrets: ["exchange_token"],
  },
  {
    id: "desktop.identity.login-transaction.exchange",
    method: "POST",
    path: "/api/v1/desktop/identity/login-transactions/:id/exchange",
    relativePath: "/login-transactions/:id/exchange",
    handler: "Exchange",
    credentialPolicy: "desktop-login-transaction",
    credential: "DesktopExchange exchange_token",
    requestSecrets: ["exchange_token"],
    responseSecrets: ["authorization_code", "oauth_state", "redirect_location"],
  },
];
assertSameSet(
  "Server Desktop login route",
  new Set(expectedServerLoginRoutes.map((route) => `${route.method} ${route.path}`)),
  new Set(serverLoginRoutes.map((route) => `${route.method} ${route.path}`))
);
const serverLoginAPISource = readFileSync(
  resolve(repoRoot, "server/api/desktop/login/login_api.go"),
  "utf8"
);
const serverLoginRouterSource = readFileSync(
  resolve(repoRoot, "server/router/desktop/desktop_login_router.go"),
  "utf8"
);
const serverRouterSurfaceSource = readFileSync(
  resolve(repoRoot, "server/initialize/router_surfaces.go"),
  "utf8"
);
const serverLoginAPITestSource = readFileSync(
  resolve(repoRoot, "server/api/desktop/login/login_api_test.go"),
  "utf8"
);
if (!serverLoginAPITestSource.includes('router.Group("/api/v1/desktop/identity")')) {
  throw new Error("Server Desktop login route group is not pinned by its API contract test");
}
const serverLoginByID = new Map(serverLoginRoutes.map((route) => [route.id, route]));
for (const expected of expectedServerLoginRoutes) {
  const route = serverLoginByID.get(expected.id);
  if (
    !route ||
    route.surface !== "desktop-resource" ||
    route.status !== "server-contract" ||
    route.caller !== "sidecar" ||
    route.rendererAccess !== "forbidden" ||
    route.handler !== expected.handler ||
    route.credentialPolicy !== expected.credentialPolicy ||
    route.credential !== expected.credential ||
    JSON.stringify(route.requestSecrets) !== JSON.stringify(expected.requestSecrets) ||
    JSON.stringify(route.responseSecrets) !== JSON.stringify(expected.responseSecrets)
  ) {
    throw new Error(`Server Desktop login route policy drift for ${expected.id}`);
  }
  const handlerPattern = new RegExp(
    `func \\(a \\*LoginApi\\) ${expected.handler}\\(c \\*gin\\.Context\\)`,
    "u"
  );
  if (!handlerPattern.test(serverLoginAPISource)) {
    throw new Error(`Server Desktop login handler is missing: ${expected.handler}`);
  }
  const routerRegistration = serverLoginRouterSource
    .split("\n")
    .some(
      (line) =>
        line.includes(`g.${expected.method}("${expected.relativePath}"`) &&
        line.includes(`apis.${expected.handler}`)
    );
  if (!routerRegistration) {
    throw new Error(`Server Desktop login route is not mounted: ${expected.method} ${expected.relativePath}`);
  }
}
if (!serverRouterSurfaceSource.includes("desktop.DesktopLoginRouter.InitDesktopLoginRouter(group)")) {
  throw new Error("Desktop Login Transaction router is not mounted on the Desktop resource surface");
}

// inventorySource describes where a route contract is declared, not whether
// the Sidecar consumes it. The typed Desktop Login client consumes the four
// Server API routes directly; combine those with the nine generic cloud-proxy
// routes for the complete Sidecar-consumed Method+Path/ID contract.
const sidecarConsumedCloudRoutes = manifest.cloudRoutes.filter(
  (route) =>
    route.inventorySource === "sidecar-cloud-proxy" ||
    (route.inventorySource === "server-desktop-login-api" && route.caller === "sidecar")
);
const sidecarConsumedExpected = assertUnique(
  sidecarConsumedCloudRoutes,
  (route) => `${route.method} ${route.path}`,
  "Sidecar-consumed cloud routes"
);
const sidecarConsumedExpectedIDs = assertUnique(
  sidecarConsumedCloudRoutes,
  (route) => route.id,
  "Sidecar-consumed cloud route ids"
);
assertSameSet("Sidecar-consumed cloud Method+Path coverage", cloudRouteIdentities, sidecarConsumedExpected);
assertSameSet("Sidecar-consumed cloud ID coverage", cloudRouteIDs, sidecarConsumedExpectedIDs);

const sidecarConsumedActual = new Set(cloudActual);
const sidecarConsumedActualIDs = new Map(cloudActualIDs);
assertSameSet("Sidecar-consumed cloud Method+Path", sidecarConsumedExpected, sidecarConsumedActual);
assertSameSet(
  "Sidecar-consumed cloud route ID",
  sidecarConsumedExpectedIDs,
  new Set(sidecarConsumedActualIDs.keys())
);
for (const route of sidecarConsumedCloudRoutes) {
  const actual = sidecarConsumedActualIDs.get(route.id);
  const expected = `${route.method} ${route.path}`;
  if (actual !== expected) {
    throw new Error(
      `Sidecar-consumed cloud route drift for ${route.id}: expected=${expected} actual=${actual ?? "missing"}`
    );
  }
}

const ipcExpected = assertUnique(manifest.ipc, (entry) => entry.command, "ipc");
const ipcByID = new Map(manifest.ipc.map((entry) => [entry.id, entry]));
assertUnique(manifest.ipc, (entry) => entry.id, "ipc ids");
const mainSource = readFileSync(resolve(repoRoot, "desktop/electron/src/main.ts"), "utf8");
const ipcActual = new Set();
for (const match of mainSource.matchAll(/ipcMain\.handle\(\s*"([^"]+)"/gu)) {
  ipcActual.add(match[1]);
}
assertSameSet("IPC command", ipcExpected, ipcActual);

const typedBridge = manifest.typedBridge;
if (
  !typedBridge ||
  typedBridge.version !== "1.0.0-alpha.7" ||
  typedBridge.global !== "desktopBridge" ||
  typedBridge.compatibilityGlobal !== "workmaxLocal" ||
  typedBridge.legacyFetchPreserved !== true
) {
  throw new Error("typed bridge manifest identity or compatibility contract is invalid");
}

const typedBridgeMethods = assertUnique(
  typedBridge.routeMethods,
  (entry) => entry.bridgeMethod,
  "typed bridge route methods"
);
assertUnique(typedBridge.routeMethods, (entry) => entry.routeId, "typed bridge route ids");
const typedBridgeSource = readFileSync(
  resolve(repoRoot, "desktop/electron/src/desktop-bridge.ts"),
  "utf8"
);
const preloadSource = readFileSync(
  resolve(repoRoot, "desktop/electron/src/preload.ts"),
  "utf8"
);
for (const globalName of [typedBridge.global, typedBridge.compatibilityGlobal]) {
  const exposedGlobalPattern = new RegExp(
    `contextBridge\\.exposeInMainWorld\\(\\s*"${globalName}"`,
    "u"
  );
  if (!exposedGlobalPattern.test(preloadSource)) {
    throw new Error(`preload does not expose declared bridge global ${globalName}`);
  }
}
if (!typedBridgeSource.includes('export const DESKTOP_BRIDGE_VERSION = "1.0.0-alpha.7"')) {
  throw new Error("typed bridge source version does not match the manifest");
}
const buildCapabilitiesStart = typedBridgeSource.indexOf(
  "function buildCapabilities(): DesktopBridgeCapabilities"
);
const buildCapabilitiesEnd = typedBridgeSource.indexOf(
  "function buildAgentSidecarTurnRequest",
  buildCapabilitiesStart
);
if (buildCapabilitiesStart < 0 || buildCapabilitiesEnd <= buildCapabilitiesStart) {
  throw new Error("typed bridge capabilities implementation is missing");
}
const capabilitiesSource = typedBridgeSource.slice(
  buildCapabilitiesStart,
  buildCapabilitiesEnd
);
if (
  !/agent:\s*\{\s*supported:\s*true,\s*methods:\s*\[\s*"listSkills",\s*"createThread",\s*"uploadThreadFile",\s*"listRecoverableTurns",\s*"startTurn",\s*"resumeTurn",\s*"cancelTurn",?\s*\],\s*deferred:\s*\[\s*"artifact"\s*\]/u.test(
    capabilitiesSource
  )
) {
  throw new Error("typed bridge Agent capability contract is invalid");
}
const typedSourceSpecs = new Map();
for (const match of typedBridgeSource.matchAll(
  /defineTypedRoute\(\s*"([^"]+)"\s*,\s*"([^"]+)"\s*,\s*"(GET|POST|PUT|PATCH|DELETE)"\s*,\s*"([^"]+)"\s*,\s*"(none|json|multipart)"\s*,\s*(null|"[^"]+")\s*\)/gu
)) {
  const [, bridgeMethod, routeId, method, path, body, rawContentType] = match;
  if (typedSourceSpecs.has(bridgeMethod)) {
    throw new Error(`typed bridge source contains duplicate method ${bridgeMethod}`);
  }
  typedSourceSpecs.set(bridgeMethod, {
    routeId,
    method,
    path,
    body,
    contentType: rawContentType === "null" ? null : rawContentType.slice(1, -1),
  });
}
assertSameSet("typed bridge method", typedBridgeMethods, new Set(typedSourceSpecs.keys()));

const typedLocalMethods = assertUnique(
  typedBridge.localMethods,
  (entry) => entry.bridgeMethod,
  "typed bridge local methods"
);
assertSameSet(
  "typed bridge local method",
  new Set(["agent.cancelTurn"]),
  typedLocalMethods
);
const cancelTurnMethod = typedBridge.localMethods.find(
  (entry) => entry.bridgeMethod === "agent.cancelTurn"
);
if (
  cancelTurnMethod?.kind !== "preload-active-turn-cancel-and-sidecar-recovery-cancel" ||
  !/cancelTurn:\s*\(turnID\)\s*=>\s*deps\.cancelAgentTurn\(validateTurnID\(turnID\)\)/u.test(
    typedBridgeSource
  ) ||
  !preloadSource.includes("cancelOpenAgentTurn(") ||
  !preloadSource.includes('`/agent/turns/${encodeURIComponent(turnID)}/cancel`') ||
  !preloadSource.includes('active.reader?.cancel("renderer canceled Agent turn")') ||
  !preloadSource.includes("active.abortController.abort()")
) {
  throw new Error("typed bridge Agent cancel must fence the local stream and call Sidecar recovery cancel");
}

const loopbackByID = new Map(manifest.loopbackRoutes.map((route) => [route.id, route]));
for (const entry of typedBridge.routeMethods) {
  const route = loopbackByID.get(entry.routeId);
  const source = typedSourceSpecs.get(entry.bridgeMethod);
  if (!route) {
    throw new Error(`typed bridge method ${entry.bridgeMethod} references unknown route ${entry.routeId}`);
  }
  if (
    !source ||
    source.routeId !== entry.routeId ||
    source.method !== route.method ||
    source.path !== route.path
  ) {
    throw new Error(`typed bridge method ${entry.bridgeMethod} drifts from route ${entry.routeId}`);
  }
  if (source.body === "json") {
    if (
      source.contentType !== "application/json" ||
      !route.requestPolicy.contentTypes.includes(source.contentType) ||
      route.requestPolicy.body === "forbidden"
    ) {
      throw new Error(`typed bridge method ${entry.bridgeMethod} has invalid JSON body policy`);
    }
  } else if (source.body === "multipart") {
    if (
      source.contentType !== null ||
      route.requestPolicy.body !== "required" ||
      !route.requestPolicy.contentTypes.includes("multipart/form-data")
    ) {
      throw new Error(`typed bridge method ${entry.bridgeMethod} has invalid multipart body policy`);
    }
  } else if (source.contentType !== null || route.requestPolicy.body === "required") {
    throw new Error(`typed bridge method ${entry.bridgeMethod} must not omit a required body`);
  }
}

const typedIPCMethods = assertUnique(
  typedBridge.ipcMethods,
  (entry) => entry.bridgeMethod,
  "typed bridge IPC methods"
);
assertUnique(typedBridge.ipcMethods, (entry) => entry.ipcId, "typed bridge IPC ids");
for (const entry of typedBridge.ipcMethods) {
  const ipc = ipcByID.get(entry.ipcId);
  if (!ipc) {
    throw new Error(`typed bridge method ${entry.bridgeMethod} references unknown IPC ${entry.ipcId}`);
  }
  if (!preloadSource.includes(`ipcRenderer.invoke("${ipc.command}"`)) {
    throw new Error(`preload does not invoke typed bridge IPC ${ipc.command}`);
  }
}

const privilegedRouteIDs = assertUnique(
  typedBridge.privilegedRoutes,
  (entry) => entry.routeId,
  "typed bridge privileged routes"
);
assertUnique(
  typedBridge.privilegedRoutes,
  (entry) => entry.bridgeMethod,
  "typed bridge privileged methods"
);
const expectedLoginTransactionPrivilegedRoutes = [
  {
    bridgeMethod: "auth.beginLogin",
    routeId: "auth.login-transaction.begin",
    method: "POST",
    path: "/auth/login-transaction",
    ipcId: "auth.begin-login-transaction",
    command: "auth-begin-login-transaction",
    rendererInput: { kind: "none", fields: [] },
    requestBody: "forbidden",
    contentTypes: [],
    maxBodyBytes: 0,
  },
  {
    bridgeMethod: "auth.loginStatus",
    routeId: "auth.login-transaction.status",
    method: "GET",
    path: "/auth/login-transaction",
    ipcId: "auth.login-transaction-status",
    command: "auth-login-transaction-status",
    rendererInput: { kind: "none", fields: [] },
    requestBody: "forbidden",
    contentTypes: [],
    maxBodyBytes: 0,
  },
  {
    bridgeMethod: "auth.submitLoginPassword",
    routeId: "auth.login-transaction.password",
    method: "POST",
    path: "/auth/login-transaction/password",
    ipcId: "auth.submit-login-password",
    command: "auth-submit-login-password",
    rendererInput: { kind: "exact-object", fields: ["email", "password"] },
    requestBody: "required",
    contentTypes: ["application/json"],
    maxBodyBytes: 4096,
  },
  {
    bridgeMethod: "auth.cancelLogin",
    routeId: "auth.login-transaction.cancel",
    method: "DELETE",
    path: "/auth/login-transaction",
    ipcId: "auth.cancel-login-transaction",
    command: "auth-cancel-login-transaction",
    rendererInput: { kind: "none", fields: [] },
    requestBody: "forbidden",
    contentTypes: [],
    maxBodyBytes: 0,
  },
];
assertSameSet(
  "Login Transaction privileged bridge method",
  new Set(expectedLoginTransactionPrivilegedRoutes.map((entry) => entry.bridgeMethod)),
  new Set(typedBridge.privilegedRoutes.map((entry) => entry.bridgeMethod))
);
assertSameSet(
  "Login Transaction privileged route id",
  new Set(expectedLoginTransactionPrivilegedRoutes.map((entry) => entry.routeId)),
  privilegedRouteIDs
);

const routeBridgeMethods = new Set(
  typedBridge.routeMethods.map((entry) => entry.bridgeMethod)
);
const directRouteIDs = new Set(
  typedBridge.routeMethods.map((entry) => entry.routeId)
);
for (const expected of expectedLoginTransactionPrivilegedRoutes) {
  const entry = typedBridge.privilegedRoutes.find(
    (candidate) => candidate.bridgeMethod === expected.bridgeMethod
  );
  const route = loopbackByID.get(expected.routeId);
  const ipc = ipcByID.get(expected.ipcId);
  const ipcMethod = typedBridge.ipcMethods.find(
    (candidate) => candidate.bridgeMethod === expected.bridgeMethod
  );
  if (
    !entry ||
    entry.routeId !== expected.routeId ||
    entry.ipcId !== expected.ipcId ||
    entry.rendererVisibility !== "status-only" ||
    JSON.stringify(entry.rendererInput) !== JSON.stringify(expected.rendererInput) ||
    !route ||
    route.method !== expected.method ||
    route.path !== expected.path ||
    route.credential !== "local-token" ||
    route.requestPolicy.origin !== "absent" ||
    route.requestPolicy.body !== expected.requestBody ||
    JSON.stringify(route.requestPolicy.contentTypes) !== JSON.stringify(expected.contentTypes) ||
    route.requestPolicy.maxBodyBytes !== expected.maxBodyBytes ||
    !ipc ||
    ipc.command !== expected.command ||
    ipc.status !== "current" ||
    ipcMethod?.ipcId !== expected.ipcId ||
    !typedIPCMethods.has(expected.bridgeMethod)
  ) {
    throw new Error(
      `Desktop Login Transaction privileged contract drift for ${expected.bridgeMethod}`
    );
  }
  if (
    routeBridgeMethods.has(expected.bridgeMethod) ||
    directRouteIDs.has(expected.routeId)
  ) {
    throw new Error(
      `Desktop Login Transaction method must not use the generic route bridge: ${expected.bridgeMethod}`
    );
  }
  if (!preloadSource.includes(`ipcRenderer.invoke("${expected.command}"`)) {
    throw new Error(
      `preload must invoke Desktop Login Transaction IPC directly: ${expected.command}`
    );
  }
  const methodName = expected.bridgeMethod.split(".").at(-1);
  const directDependencyPattern = new RegExp(
    `${methodName}\\s*:\\s*(?:async\\s*)?\\([^)]*\\)\\s*=>\\s*deps\\.${methodName}\\s*\\(`,
    "u"
  );
  if (!directDependencyPattern.test(typedBridgeSource)) {
    throw new Error(
      `typed bridge must keep ${expected.bridgeMethod} on its direct privileged dependency`
    );
  }
}

for (const entry of typedBridge.privilegedRoutes) {
  if (!loopbackByID.has(entry.routeId)) {
    throw new Error(
      `typed bridge privileged method ${entry.bridgeMethod} references unknown route ${entry.routeId}`
    );
  }
  const ipc = ipcByID.get(entry.ipcId);
  if (!ipc || !typedIPCMethods.has(entry.bridgeMethod)) {
    throw new Error(
      `typed bridge privileged method ${entry.bridgeMethod} must reference a declared IPC`
    );
  }
  const ipcMethod = typedBridge.ipcMethods.find(
    (candidate) => candidate.bridgeMethod === entry.bridgeMethod
  );
  if (ipcMethod?.ipcId !== entry.ipcId || entry.rendererVisibility !== "status-only") {
    throw new Error(
      `typed bridge privileged method ${entry.bridgeMethod} must expose status-only through ${entry.ipcId}`
    );
  }
}
if (
  !preloadSource.includes("isPrivilegedLoginTransactionURL(url)") ||
  !preloadSource.includes('credentials: "omit"') ||
  !preloadSource.includes('redirect: "error"') ||
  !preloadSource.includes('canonical.startsWith("/auth/start/")') ||
  !preloadSource.includes('canonical === "/auth/login-transaction"') ||
  !preloadSource.includes('canonical.startsWith("/auth/login-transaction/")')
) {
  throw new Error(
    "Desktop login transaction must stay behind the no-cookie/no-redirect privileged typed bridge"
  );
}
if (
  !preloadSource.includes("isTypedAgentOnlyRequest(url, init)") ||
  !preloadSource.includes('canonical === "/agent/chat"') ||
  !preloadSource.includes('canonical.startsWith("/agent/chat/")') ||
  !preloadSource.includes('canonical === "/agent/skills/catalog"') ||
  !preloadSource.includes('canonical.startsWith("/agent/skills/catalog/")') ||
  !preloadSource.includes('canonical === "/agent/turns"') ||
  !preloadSource.includes('canonical.startsWith("/agent/turns/")') ||
  !preloadSource.includes('method === "PUT"') ||
  !preloadSource.includes('canonical.startsWith("/agent/threads/")')
) {
  throw new Error(
    "legacy compatibility fetch must not bypass typed Agent chat/catalog/create/recovery boundaries"
  );
}
if (
  !mainSource.includes("event.sender !== mainWindow.webContents") ||
  !mainSource.includes("event.senderFrame !== event.sender.mainFrame")
) {
  throw new Error(
    "Desktop Login Transaction IPC must be restricted to the main window main frame"
  );
}
const loginTransactionMainSource = readFileSync(
  resolve(repoRoot, "desktop/electron/src/login-transaction.ts"),
  "utf8"
);
if (
  !loginTransactionMainSource.includes('"X-WorkMax-Login-Flow"') ||
  !loginTransactionMainSource.includes("class MainLoginTransactionSession") ||
  !loginTransactionMainSource.includes("beginCandidateFlowID") ||
  !loginTransactionMainSource.includes("beginInFlight") ||
  !mainSource.includes('randomBytes(32).toString("base64url")') ||
  !mainSource.includes("new MainLoginTransactionSession(") ||
  !mainSource.includes("clearLoginPasswordIPCValue(rawInput)")
) {
  throw new Error(
    "Desktop Login Transaction flow generation must remain Main-owned, generation-bound, and credential-clearing"
  );
}
const rendererSource = readFileSync(
  resolve(repoRoot, "desktop/renderer/en/desktop/renderer.js"),
  "utf8"
);
if (
  /["']\/auth\/start["']/u.test(rendererSource) ||
  /["']\/auth\/login-transaction(?:\/password)?["']/u.test(rendererSource) ||
  /openOAuthWindow|authorize_url|auth_port|oauth\/callback/u.test(rendererSource)
) {
  throw new Error("bundled Renderer must not observe login transaction material");
}
for (const [label, source] of [
  ["preload", preloadSource],
  ["typed bridge", typedBridgeSource],
  ["bundled Renderer", rendererSource],
]) {
  if (source.includes("X-WorkMax-Login-Flow")) {
    throw new Error(`${label} must not observe the Main/Sidecar login flow ID`);
  }
}

const serverLoginRouteIDs = new Set(serverLoginRoutes.map((route) => route.id));
for (const entry of [...typedBridge.routeMethods, ...typedBridge.privilegedRoutes]) {
  if (serverLoginRouteIDs.has(entry.routeId)) {
    throw new Error(
      `Server Desktop login route must not be exposed through Renderer bridge: ${entry.routeId}`
    );
  }
}
const sidecarOnlySourceMarkers = [
  "/api/v1/desktop/identity/login-transactions",
  "transaction_secret",
  "transactionSecret",
  "exchange_token",
  "exchangeToken",
  "DesktopLogin",
  "DesktopExchange",
  "authorization_code",
  "redirect_location",
];
const rendererBoundarySources = collectRuntimeSources(
  resolve(repoRoot, "desktop/electron/src")
);
for (const [sourcePath, source] of collectRuntimeSources(
  resolve(repoRoot, "desktop/renderer/en/desktop")
)) {
  rendererBoundarySources.set(sourcePath, source);
}
for (const [label, source] of rendererBoundarySources) {
  for (const marker of sidecarOnlySourceMarkers) {
    if (source.includes(marker)) {
      throw new Error(`${label} must not contain Sidecar-only login material: ${marker}`);
    }
  }
}

const deferredRouteIDs = assertUnique(
  typedBridge.deferredRoutes,
  (entry) => entry.routeId,
  "typed bridge deferred routes"
);
for (const entry of typedBridge.deferredRoutes) {
  if (!loopbackByID.has(entry.routeId) || typeof entry.reason !== "string" || entry.reason === "") {
    throw new Error(`typed bridge deferred route ${entry.routeId} is invalid`);
  }
}
const classifiedRouteIDs = new Set([
  ...typedBridge.routeMethods.map((entry) => entry.routeId),
  ...privilegedRouteIDs,
  ...deferredRouteIDs,
]);
assertSameSet("typed bridge route classification", new Set(loopbackByID.keys()), classifiedRouteIDs);
assertUnique(
  typedBridge.unsupportedNamespaces,
  (entry) => entry.namespace,
  "typed bridge unsupported namespaces"
);
assertSameSet(
  "typed bridge unsupported namespace",
  new Set(["artifact"]),
  new Set(typedBridge.unsupportedNamespaces.map((entry) => entry.namespace))
);
for (const entry of typedBridge.unsupportedNamespaces) {
  if (typeof entry.reason !== "string" || entry.reason === "") {
    throw new Error(`typed bridge unsupported namespace ${entry.namespace} needs a reason`);
  }
}
const targetGaps = assertUnique(manifest.targetGaps, (gap) => gap, "target gaps");
for (const requiredGap of [
  "durable-turn-attach-replay",
]) {
  if (!targetGaps.has(requiredGap)) {
    throw new Error(`Desktop Agent target gap is missing: ${requiredGap}`);
  }
}
for (const obsoleteGap of [
  "typed-preload-bridge-migration",
  "start-attach-replay-cancel-terminal",
  "new-thread-create-local-upsert",
]) {
  if (targetGaps.has(obsoleteGap)) {
    throw new Error(`completed Desktop Agent gap must be removed: ${obsoleteGap}`);
  }
}

function assertSameSet(label, expected, actual) {
  const missing = [...expected].filter((value) => !actual.has(value));
  const extra = [...actual].filter((value) => !expected.has(value));
  if (missing.length || extra.length) {
    throw new Error(
      `${label} drift: missing=${JSON.stringify(missing)} extra=${JSON.stringify(extra)}`
    );
  }
}

process.stdout.write(
  `${JSON.stringify({
    status: "ok",
    schemaVersion: manifest.schemaVersion,
    repositoryClients: repositorySurfaces.clients,
    repositoryServices: repositorySurfaces.services,
    loopbackRoutes: loopbackActual.size,
    cloudRoutes: cloudRouteIdentities.size,
    sidecarProxyCloudRoutes: sidecarProxyActual.size,
    sidecarConsumedCloudRoutes: sidecarConsumedActual.size,
    serverDesktopLoginRoutes: serverLoginRoutes.length,
    ipcCommands: ipcActual.size,
    typedBridgeMethods: typedSourceSpecs.size,
    typedBridgePrivilegedRoutes: privilegedRouteIDs.size,
    typedBridgeDeferredRoutes: deferredRouteIDs.size,
    targetGaps: manifest.targetGaps.length,
  })}\n`
);
