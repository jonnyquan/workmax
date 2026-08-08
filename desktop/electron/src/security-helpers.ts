export interface OAuthArgs {
  authorizeURL: string;
  authPort: number;
}

export interface ValidateOAuthArgsOptions {
  // Renderer URL is useful caller context, but it is deliberately not an
  // authorize-origin allowlist. OAuth authorize URLs must come from the cloud
  // base the sidecar is configured to talk to, not whichever origin currently
  // hosts the bridge-bearing renderer.
  rendererUrl: string;
  defaultCloudBase: string;
  cloudBase?: string;
}

export function isRendererNavigationAllowed(
  targetURL: string,
  rendererURL: string
): boolean {
  return isURLWithinRendererRoute(targetURL, rendererURL);
}

export function assertTrustedRendererSenderURL(
  senderURL: string,
  rendererURL: string
): void {
  if (!isURLWithinRendererRoute(senderURL, rendererURL)) {
    throw new Error("untrusted renderer origin");
  }
}

export function isURLWithinRendererRoute(targetURL: string, rendererURL: string): boolean {
  try {
    const target = new URL(targetURL);
    const renderer = new URL(rendererURL);
    if (hasURLCredentials(target) || hasURLCredentials(renderer)) {
      return false;
    }
    if (renderer.protocol === "file:") {
      return isURLWithinBundledRendererEntry(target, renderer);
    }
    if (target.protocol !== "http:" && target.protocol !== "https:") {
      return false;
    }
    const targetPath = normalizeRoutePath(target.pathname);
    const rendererPath = normalizeRoutePath(renderer.pathname);
    return (
      target.origin === renderer.origin &&
      (targetPath === rendererPath || targetPath.startsWith(`${rendererPath}/`))
    );
  } catch {
    return false;
  }
}

export function assertDesktopRendererURL(rendererURL: string): void {
  let parsed: URL;
  try {
    parsed = new URL(rendererURL);
  } catch {
    throw new Error("invalid desktop renderer URL");
  }
  if (parsed.protocol !== "http:" && parsed.protocol !== "https:") {
    if (parsed.protocol === "file:" && isBundledRendererEntryPath(parsed)) {
      return;
    }
    throw new Error("desktop renderer URL must use http, https, or the bundled file renderer entry");
  }
  if (hasURLCredentials(parsed)) {
    throw new Error("desktop renderer URL must not include credentials");
  }
  const path = normalizeRoutePath(parsed.pathname);
  const segments = path.split("/").filter(Boolean);
  if (segments[segments.length - 1] !== "desktop") {
    throw new Error("desktop renderer URL must point to a /desktop route");
  }
}

function isURLWithinBundledRendererEntry(target: URL, renderer: URL): boolean {
  if (target.protocol !== "file:") {
    return false;
  }
  if (!isBundledRendererEntryPath(renderer) || !isBundledRendererEntryPath(target)) {
    return false;
  }
  return target.pathname === renderer.pathname;
}

function isBundledRendererEntryPath(url: URL): boolean {
  if (url.protocol !== "file:") {
    return false;
  }
  if (url.hostname !== "" || url.search !== "" || url.hash !== "") {
    return false;
  }
  return normalizeRoutePath(url.pathname).endsWith("/renderer/en/desktop/index.html");
}

function normalizeRoutePath(pathname: string): string {
  if (pathname === "/") return pathname;
  return pathname.replace(/\/+$/, "");
}

function hasURLCredentials(url: URL): boolean {
  return url.username !== "" || url.password !== "";
}

export function validateOpenOAuthArgs(
  args: OAuthArgs,
  opts: ValidateOAuthArgsOptions
): void {
  if (!Number.isInteger(args.authPort) || args.authPort < 1 || args.authPort > 65535) {
    throw new Error("invalid OAuth callback port");
  }

  const authorizeURL = new URL(args.authorizeURL);
  if (authorizeURL.protocol !== "http:" && authorizeURL.protocol !== "https:") {
    throw new Error("invalid OAuth authorize URL protocol");
  }
  if (authorizeURL.username !== "" || authorizeURL.password !== "") {
    throw new Error("invalid OAuth authorize URL credentials");
  }
  if (authorizeURL.hash !== "") {
    throw new Error("invalid OAuth authorize URL fragment");
  }
  if (authorizeURL.pathname !== "/api/desktop/oauth/authorize") {
    throw new Error("invalid OAuth authorize URL path");
  }
  const redirectURIs = authorizeURL.searchParams.getAll("redirect_uri");
  if (redirectURIs.length !== 1 || !isExactOAuthRedirectURI(redirectURIs[0], args.authPort)) {
    throw new Error("invalid OAuth redirect_uri");
  }

  const allowedOrigins = new Set<string>([
    parseBareHTTPOrigin(opts.defaultCloudBase, "default OAuth cloud base"),
  ]);
  if (opts.cloudBase) {
    allowedOrigins.add(parseBareHTTPOrigin(opts.cloudBase, "OAuth cloud base"));
  }
  if (!allowedOrigins.has(authorizeURL.origin)) {
    throw new Error("invalid OAuth authorize URL origin");
  }
}

function parseBareHTTPOrigin(rawURL: string, label: string): string {
  const parsed = new URL(rawURL);
  if (parsed.protocol !== "http:" && parsed.protocol !== "https:") {
    throw new Error(`invalid ${label} protocol`);
  }
  if (parsed.username !== "" || parsed.password !== "") {
    throw new Error(`invalid ${label} credentials`);
  }
  if (parsed.pathname !== "/" || parsed.search !== "" || parsed.hash !== "") {
    throw new Error(`invalid ${label}: expected bare origin`);
  }
  return parsed.origin;
}

function isExactOAuthRedirectURI(url: string, authPort: number): boolean {
  try {
    const target = new URL(url);
    return (
      target.protocol === "http:" &&
      target.hostname === "127.0.0.1" &&
      target.port === String(authPort) &&
      target.pathname === "/oauth/callback" &&
      !hasURLCredentials(target) &&
      target.search === "" &&
      target.hash === ""
    );
  } catch {
    return false;
  }
}

export function normalizeExternalHTTPURL(url: string): string | null {
  try {
    const target = new URL(url);
    if (target.protocol !== "http:" && target.protocol !== "https:") {
      return null;
    }
    if (hasURLCredentials(target)) {
      return null;
    }
    if (isLocalOrPrivateHostname(target.hostname)) {
      return null;
    }
    return target.toString();
  } catch {
    return null;
  }
}

function isLocalOrPrivateHostname(hostname: string): boolean {
  const host = hostname.toLowerCase();
  if (host === "localhost" || host.endsWith(".localhost")) {
    return true;
  }
  if (host === "::1" || host === "[::1]") {
    return true;
  }
  if (/^127(?:\.\d{1,3}){3}$/u.test(host)) {
    return true;
  }
  if (/^10(?:\.\d{1,3}){3}$/u.test(host)) {
    return true;
  }
  if (/^192\.168(?:\.\d{1,3}){2}$/u.test(host)) {
    return true;
  }
  const match172 = /^172\.(\d{1,3})(?:\.\d{1,3}){2}$/u.exec(host);
  if (match172) {
    const second = Number(match172[1]);
    if (second >= 16 && second <= 31) {
      return true;
    }
  }
  if (/^169\.254(?:\.\d{1,3}){2}$/u.test(host)) {
    return true;
  }
  return false;
}

export function redactSidecarLogLine(line: string): string {
  return line
    .replace(/(https?:\/\/)[^/\s:@]+(?::[^/\s@]*)?@/gi, "$1[REDACTED]@")
    .replace(/(generated ephemeral token:\s*)\S+/gi, "$1[REDACTED]")
    .replace(/(WORKMAX_LOCAL_TOKEN=)\S+/gi, "$1[REDACTED]")
    .replace(/(X-Local-Token[:=]\s*)\S+/gi, "$1[REDACTED]")
    .replace(/(Authorization:\s*(?:Bearer|Basic)\s+)\S+/gi, "$1[REDACTED]")
    .replace(/\bBearer\s+[A-Za-z0-9._~+/=-]+/gi, "Bearer [REDACTED]")
    .replace(/\bBasic\s+[A-Za-z0-9._~+/=-]+/gi, "Basic [REDACTED]")
    .replace(
      /((?:access|refresh|id)_token["']?\s*[:=]\s*["']?)[^"',&\s]+/gi,
      "$1[REDACTED]"
    )
    .replace(/(api[_-]?key["']?\s*[:=]\s*["']?)[^"',&\s]+/gi, "$1[REDACTED]")
    .replace(/(apikey["']?\s*[:=]\s*["']?)[^"',&\s]+/gi, "$1[REDACTED]")
    .replace(/(client_secret["']?\s*[:=]\s*["']?)[^"',&\s]+/gi, "$1[REDACTED]")
    .replace(/(password["']?\s*[:=]\s*["']?)[^"',&\s]+/gi, "$1[REDACTED]")
    .replace(/(secret["']?\s*[:=]\s*["']?)[^"',&\s]+/gi, "$1[REDACTED]");
}
