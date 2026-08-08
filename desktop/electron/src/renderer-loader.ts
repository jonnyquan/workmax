import { assertDesktopRendererURL } from "./security-helpers";

export type RendererChannel =
  | "packaged-bundled"
  | "development-bundled"
  | "development-loopback"
  | "development-trusted-remote";

export interface ResolveDesktopRendererOptions {
  isPackaged: boolean;
  bundledRendererURL: string;
  bundledRendererExists: boolean;
  configuredRendererURL?: string;
  trustedRendererOrigins?: string;
}

export interface DesktopRendererSelection {
  url: string;
  channel: RendererChannel;
}

/**
 * Select the only renderer URL that may receive the privileged preload bridge.
 *
 * Packaged applications are deliberately immutable at runtime: the renderer
 * must be the file shipped in Resources/renderer. Development may use that
 * same local file, an explicit loopback server, or an HTTPS origin listed in
 * WORKMAX_DESKTOP_TRUSTED_RENDERER_ORIGINS.
 */
export function resolveDesktopRenderer(
  opts: ResolveDesktopRendererOptions
): DesktopRendererSelection {
  assertDesktopRendererURL(opts.bundledRendererURL);

  if (opts.isPackaged) {
    if (opts.configuredRendererURL !== undefined) {
      // Validate first so existing negative package smoke tests continue to
      // report malformed route input precisely. A valid override is still
      // forbidden below.
      assertDesktopRendererURL(opts.configuredRendererURL);
      throw new Error(
        "packaged Desktop forbids WORKMAX_DESKTOP_RENDERER_URL; the bundled renderer is mandatory"
      );
    }
    if (!opts.bundledRendererExists) {
      throw new Error(
        "packaged Desktop renderer is missing; refusing remote renderer fallback"
      );
    }
    return { url: opts.bundledRendererURL, channel: "packaged-bundled" };
  }

  const configuredURL = opts.configuredRendererURL;
  if (configuredURL === undefined || configuredURL === "") {
    throw new Error(
      "development Desktop requires WORKMAX_DESKTOP_RENDERER_URL; use dev.sh or configure an explicit trusted renderer"
    );
  }
  assertDesktopRendererURL(configuredURL);

  if (configuredURL === opts.bundledRendererURL) {
    if (!opts.bundledRendererExists) {
      throw new Error("development bundled Desktop renderer is missing");
    }
    return { url: configuredURL, channel: "development-bundled" };
  }

  const renderer = new URL(configuredURL);
  if (renderer.protocol === "file:") {
    throw new Error(
      "development file renderer must be the repository bundled Desktop entry"
    );
  }
  if (isExplicitLoopbackRenderer(renderer)) {
    return { url: configuredURL, channel: "development-loopback" };
  }
  if (renderer.protocol !== "https:") {
    throw new Error("remote development renderer must use HTTPS");
  }

  const trustedOrigins = parseTrustedRendererOrigins(opts.trustedRendererOrigins);
  if (!trustedOrigins.has(renderer.origin)) {
    throw new Error(
      `development renderer origin is not trusted: ${renderer.origin}`
    );
  }
  return { url: configuredURL, channel: "development-trusted-remote" };
}

function isExplicitLoopbackRenderer(url: URL): boolean {
  if (url.protocol !== "http:" && url.protocol !== "https:") {
    return false;
  }
  return (
    url.hostname === "localhost" ||
    url.hostname === "127.0.0.1" ||
    url.hostname === "[::1]"
  );
}

function parseTrustedRendererOrigins(rawOrigins?: string): Set<string> {
  const trusted = new Set<string>();
  if (rawOrigins === undefined || rawOrigins === "") {
    return trusted;
  }

  for (const rawOrigin of rawOrigins.split(",")) {
    const candidate = rawOrigin.trim();
    if (candidate === "") {
      throw new Error("trusted renderer origins must not contain empty entries");
    }
    let origin: URL;
    try {
      origin = new URL(candidate);
    } catch {
      throw new Error(`invalid trusted renderer origin: ${candidate}`);
    }
    if (
      origin.protocol !== "https:" ||
      origin.username !== "" ||
      origin.password !== "" ||
      origin.pathname !== "/" ||
      origin.search !== "" ||
      origin.hash !== ""
    ) {
      throw new Error(
        `trusted renderer origin must be a bare HTTPS origin: ${candidate}`
      );
    }
    trusted.add(origin.origin);
  }
  return trusted;
}
