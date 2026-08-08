import assert from "node:assert/strict";
import { describe, it } from "node:test";

import {
  assertDesktopRendererURL,
  assertTrustedRendererSenderURL,
  isRendererNavigationAllowed,
  isURLWithinRendererRoute,
  normalizeExternalHTTPURL,
  redactSidecarLogLine,
  validateOpenOAuthArgs,
} from "./security-helpers";

const RENDERER_URL = "https://workmax.app/en/desktop";
const DEFAULT_CLOUD_BASE = "https://workmax.app";
const BUNDLED_RENDERER_URL =
  "file:///Applications/workmax%20Desktop.app/Contents/Resources/renderer/en/desktop/index.html";
const REDIRECT_URI = encodeURIComponent("http://127.0.0.1:49152/oauth/callback");

describe("renderer route guards", () => {
  it("accepts only explicit desktop renderer routes for bridge-bearing windows", () => {
    assert.doesNotThrow(() => assertDesktopRendererURL("https://workmax.app/en/desktop"));
    assert.doesNotThrow(() => assertDesktopRendererURL("http://localhost:3000/desktop/"));
    assert.doesNotThrow(() => assertDesktopRendererURL(BUNDLED_RENDERER_URL));

    assert.throws(
      () => assertDesktopRendererURL("https://workmax.app/"),
      /must point to a \/desktop route/
    );
    assert.throws(
      () => assertDesktopRendererURL("https://workmax.app/en/desktop/settings"),
      /must point to a \/desktop route/
    );
    assert.throws(
      () => assertDesktopRendererURL("file:///Applications/workmax/index.html"),
      /must use http, https, or the bundled file renderer entry/
    );
    assert.throws(
      () =>
        assertDesktopRendererURL(
          "file:///Applications/workmax%20Desktop.app/Contents/Resources/renderer/en/desktop/index.html?debug=1"
        ),
      /must use http, https, or the bundled file renderer entry/
    );
    assert.throws(
      () =>
        assertDesktopRendererURL(
          "file:///Applications/workmax%20Desktop.app/Contents/Resources/renderer/en/desktop/settings.html"
        ),
      /must use http, https, or the bundled file renderer entry/
    );
    assert.throws(
      () => assertDesktopRendererURL("not a url"),
      /invalid desktop renderer URL/
    );
    assert.throws(
      () => assertDesktopRendererURL("https://workmax.app/en/%64esktop"),
      /must point to a \/desktop route/
    );
    assert.throws(
      () => assertDesktopRendererURL("https://workmax.app/en/desktop/../account"),
      /must point to a \/desktop route/
    );
    assert.throws(
      () => assertDesktopRendererURL("https://user:pass@workmax.app/en/desktop"),
      /must not include credentials/
    );
  });

  it("allows the configured route and descendants only", () => {
    assert.equal(
      isURLWithinRendererRoute("https://workmax.app/en/desktop", RENDERER_URL),
      true
    );
    assert.equal(
      isURLWithinRendererRoute("https://workmax.app/en/desktop/settings", RENDERER_URL),
      true
    );
    assert.equal(
      isURLWithinRendererRoute("https://workmax.app/en/desktopish", RENDERER_URL),
      false
    );
    assert.equal(
      isURLWithinRendererRoute("https://evil.example/en/desktop", RENDERER_URL),
      false
    );
    assert.equal(
      isURLWithinRendererRoute("https://user:pass@workmax.app/en/desktop", RENDERER_URL),
      false
    );
    assert.equal(
      isURLWithinRendererRoute(
        "https://workmax.app/en/desktop",
        "https://user:pass@workmax.app/en/desktop"
      ),
      false
    );
  });

  it("allows only the exact bundled file renderer entry", () => {
    assert.equal(
      isURLWithinRendererRoute(BUNDLED_RENDERER_URL, BUNDLED_RENDERER_URL),
      true
    );
    assert.equal(
      isURLWithinRendererRoute(
        "file:///Applications/workmax%20Desktop.app/Contents/Resources/renderer/en/desktop/index.html?debug=1",
        BUNDLED_RENDERER_URL
      ),
      false
    );
    assert.equal(
      isURLWithinRendererRoute(
        "file:///Applications/workmax%20Desktop.app/Contents/Resources/renderer/en/desktop/settings.html",
        BUNDLED_RENDERER_URL
      ),
      false
    );
    assert.equal(
      isURLWithinRendererRoute(
        "file:///Applications/workmax%20Desktop.app/Contents/Resources/renderer/en/desktop/index.html/extra",
        BUNDLED_RENDERER_URL
      ),
      false
    );
    assert.equal(
      isURLWithinRendererRoute("https://workmax.app/en/desktop", BUNDLED_RENDERER_URL),
      false
    );
  });

  it("normalizes trailing slashes on the configured renderer route", () => {
    const rendererURL = "https://workmax.app/en/desktop/";

    assert.equal(
      isURLWithinRendererRoute("https://workmax.app/en/desktop", rendererURL),
      true
    );
    assert.equal(
      isURLWithinRendererRoute("https://workmax.app/en/desktop/settings", rendererURL),
      true
    );
    assert.equal(
      isURLWithinRendererRoute("https://workmax.app/en/desktopish", rendererURL),
      false
    );
  });

  it("allows query and hash changes without widening the route", () => {
    assert.equal(
      isURLWithinRendererRoute(
        "https://workmax.app/en/desktop?panel=diagnostics#logs",
        RENDERER_URL
      ),
      true
    );
    assert.equal(
      isURLWithinRendererRoute(
        "https://workmax.app/en/desktop/settings?tab=account",
        RENDERER_URL
      ),
      true
    );
    assert.equal(
      isURLWithinRendererRoute(
        "https://workmax.app/en/desktopish?panel=diagnostics",
        RENDERER_URL
      ),
      false
    );
  });

  it("does not trust encoded or dot-segment route lookalikes", () => {
    assert.equal(
      isURLWithinRendererRoute("https://workmax.app/en/%64esktop", RENDERER_URL),
      false
    );
    assert.equal(
      isURLWithinRendererRoute("https://workmax.app/en/desktop/../account", RENDERER_URL),
      false
    );
    assert.equal(
      isURLWithinRendererRoute("https://workmax.app/en/desktop/%2e%2e/account", RENDERER_URL),
      false
    );
  });

  it("does not widen an origin-root renderer route to every same-origin path", () => {
    assert.equal(
      isURLWithinRendererRoute("https://workmax.app/", "https://workmax.app/"),
      true
    );
    assert.equal(
      isURLWithinRendererRoute("https://workmax.app/en/desktop", "https://workmax.app/"),
      false
    );
    assert.equal(
      isURLWithinRendererRoute("https://evil.example/en/desktop", "https://workmax.app/"),
      false
    );
  });

  it("rejects about:blank for bridge-bearing renderer navigation", () => {
    assert.equal(isRendererNavigationAllowed("about:blank", RENDERER_URL), false);
    assert.equal(isURLWithinRendererRoute("about:blank", RENDERER_URL), false);
  });

  it("rejects privileged IPC sender URLs outside the configured route", () => {
    assert.doesNotThrow(() =>
      assertTrustedRendererSenderURL("https://workmax.app/en/desktop", RENDERER_URL)
    );
    assert.doesNotThrow(() =>
      assertTrustedRendererSenderURL("https://workmax.app/en/desktop?panel=diagnostics", RENDERER_URL)
    );
    assert.doesNotThrow(() =>
      assertTrustedRendererSenderURL("https://workmax.app/en/desktop/settings", RENDERER_URL)
    );
    assert.doesNotThrow(() =>
      assertTrustedRendererSenderURL(BUNDLED_RENDERER_URL, BUNDLED_RENDERER_URL)
    );

    for (const senderURL of [
      "https://workmax.app/",
      "https://workmax.app/en/desktopish",
      "https://evil.example/en/desktop",
      "https://user:pass@workmax.app/en/desktop",
      "https://workmax.app/en/%64esktop",
      "https://workmax.app/en/desktop/%2e%2e/account",
      "file:///Applications/workmax%20Desktop.app/Contents/Resources/renderer/en/desktop/settings.html",
      "about:blank",
    ]) {
      assert.throws(
        () => assertTrustedRendererSenderURL(senderURL, RENDERER_URL),
        /untrusted renderer origin/
      );
    }
  });
});

describe("OAuth authorize response validation", () => {
  it("accepts packaged/default and configured cloud origins", () => {
    assert.doesNotThrow(() =>
      validateOpenOAuthArgs(
        {
          authorizeURL: `https://workmax.app/api/desktop/oauth/authorize?state=ok&redirect_uri=${REDIRECT_URI}`,
          authPort: 49152,
        },
        { rendererUrl: RENDERER_URL, defaultCloudBase: DEFAULT_CLOUD_BASE }
      )
    );
    assert.doesNotThrow(() =>
      validateOpenOAuthArgs(
        {
          authorizeURL: `https://staging.workmax.app/api/desktop/oauth/authorize?redirect_uri=${REDIRECT_URI}`,
          authPort: 49152,
        },
        {
          rendererUrl: RENDERER_URL,
          defaultCloudBase: DEFAULT_CLOUD_BASE,
          cloudBase: "https://staging.workmax.app",
        }
      )
    );
  });

  it("does not treat the renderer origin as an OAuth authorize origin", () => {
    assert.throws(
      () =>
        validateOpenOAuthArgs(
          {
            authorizeURL: `http://localhost:3000/api/desktop/oauth/authorize?redirect_uri=${REDIRECT_URI}`,
            authPort: 49152,
          },
          {
            rendererUrl: "http://localhost:3000/en/desktop",
            defaultCloudBase: DEFAULT_CLOUD_BASE,
          }
        ),
      /invalid OAuth authorize URL origin/
    );

    assert.doesNotThrow(() =>
      validateOpenOAuthArgs(
        {
          authorizeURL: `http://localhost:3000/api/desktop/oauth/authorize?redirect_uri=${REDIRECT_URI}`,
          authPort: 49152,
        },
        {
          rendererUrl: "http://localhost:3000/en/desktop",
          defaultCloudBase: DEFAULT_CLOUD_BASE,
          cloudBase: "http://localhost:3000",
        }
      )
    );
  });

  it("rejects unsafe OAuth callback ports and authorize URLs", () => {
    assert.throws(
      () =>
        validateOpenOAuthArgs(
          {
            authorizeURL: `https://workmax.app/api/desktop/oauth/authorize?redirect_uri=${REDIRECT_URI}`,
            authPort: 0,
          },
          { rendererUrl: RENDERER_URL, defaultCloudBase: DEFAULT_CLOUD_BASE }
        ),
      /invalid OAuth callback port/
    );
    assert.throws(
      () =>
        validateOpenOAuthArgs(
          {
            authorizeURL: `file:///api/desktop/oauth/authorize?redirect_uri=${REDIRECT_URI}`,
            authPort: 49152,
          },
          { rendererUrl: RENDERER_URL, defaultCloudBase: DEFAULT_CLOUD_BASE }
        ),
      /invalid OAuth authorize URL protocol/
    );
    assert.throws(
      () =>
        validateOpenOAuthArgs(
          {
            authorizeURL: `https://workmax.app/api/desktop/oauth/token?redirect_uri=${REDIRECT_URI}`,
            authPort: 49152,
          },
          { rendererUrl: RENDERER_URL, defaultCloudBase: DEFAULT_CLOUD_BASE }
        ),
      /invalid OAuth authorize URL path/
    );
    assert.throws(
      () =>
        validateOpenOAuthArgs(
          {
            authorizeURL: `https://evil.example/api/desktop/oauth/authorize?redirect_uri=${REDIRECT_URI}`,
            authPort: 49152,
          },
          { rendererUrl: RENDERER_URL, defaultCloudBase: DEFAULT_CLOUD_BASE }
        ),
      /invalid OAuth authorize URL origin/
    );
    assert.throws(
      () =>
        validateOpenOAuthArgs(
          {
            authorizeURL: `https://user:pass@workmax.app/api/desktop/oauth/authorize?redirect_uri=${REDIRECT_URI}`,
            authPort: 49152,
          },
          { rendererUrl: RENDERER_URL, defaultCloudBase: DEFAULT_CLOUD_BASE }
        ),
      /invalid OAuth authorize URL credentials/
    );
    assert.throws(
      () =>
        validateOpenOAuthArgs(
          {
            authorizeURL: `https://workmax.app/api/desktop/oauth/authorize?redirect_uri=${REDIRECT_URI}#token`,
            authPort: 49152,
          },
          { rendererUrl: RENDERER_URL, defaultCloudBase: DEFAULT_CLOUD_BASE }
        ),
      /invalid OAuth authorize URL fragment/
    );
  });

  it("requires authorize URL redirect_uri to match the sidecar callback port", () => {
    for (const redirectURI of [
      "",
      "http://127.0.0.1:49153/oauth/callback",
      "http://localhost:49152/oauth/callback",
      "https://127.0.0.1:49152/oauth/callback",
      "http://127.0.0.1:49152/oauth/callback/extra",
      "http://127.0.0.1:49152/oauth/callback?code=leak",
    ]) {
      const query = redirectURI === "" ? "" : `?redirect_uri=${encodeURIComponent(redirectURI)}`;
      assert.throws(
        () =>
          validateOpenOAuthArgs(
            {
              authorizeURL: `https://workmax.app/api/desktop/oauth/authorize${query}`,
              authPort: 49152,
            },
            { rendererUrl: RENDERER_URL, defaultCloudBase: DEFAULT_CLOUD_BASE }
          ),
        /invalid OAuth redirect_uri/
      );
    }

    assert.throws(
      () =>
        validateOpenOAuthArgs(
          {
            authorizeURL: `https://workmax.app/api/desktop/oauth/authorize?redirect_uri=${encodeURIComponent(REDIRECT_URI)}&redirect_uri=${encodeURIComponent(REDIRECT_URI)}`,
            authPort: 49152,
          },
          { rendererUrl: RENDERER_URL, defaultCloudBase: DEFAULT_CLOUD_BASE }
        ),
      /invalid OAuth redirect_uri/
    );
  });

  it("rejects malformed configured cloud origins instead of silently widening access", () => {
    assert.throws(
      () =>
        validateOpenOAuthArgs(
          {
            authorizeURL: `https://workmax.app/api/desktop/oauth/authorize?redirect_uri=${REDIRECT_URI}`,
            authPort: 49152,
          },
          {
            rendererUrl: RENDERER_URL,
            defaultCloudBase: DEFAULT_CLOUD_BASE,
            cloudBase: "not a url",
          }
        ),
      /Invalid URL/
    );
    for (const cloudBase of [
      "https://staging.workmax.app/path",
      "https://staging.workmax.app/%2e%2e/path",
      "https://staging.workmax.app?env=desktop",
      "https://staging.workmax.app#desktop",
      "https://user:pass@staging.workmax.app",
      "https://staging.workmax.app@evil.example",
      "file:///tmp/workmax",
    ]) {
      assert.throws(
        () =>
          validateOpenOAuthArgs(
            {
              authorizeURL: `https://staging.workmax.app/api/desktop/oauth/authorize?redirect_uri=${REDIRECT_URI}`,
              authPort: 49152,
            },
            {
              rendererUrl: RENDERER_URL,
              defaultCloudBase: DEFAULT_CLOUD_BASE,
              cloudBase,
            }
          ),
        /invalid OAuth cloud base/
      );
    }
  });
});

describe("external URL handoff", () => {
  it("normalizes only non-credential HTTP(S) URLs for OS browser handoff", () => {
    assert.equal(
      normalizeExternalHTTPURL("https://example.com/path?q=1#section"),
      "https://example.com/path?q=1#section"
    );
    assert.equal(normalizeExternalHTTPURL("http://example.com/"), "http://example.com/");

    for (const url of [
      "file:///tmp/workmax",
      "mailto:support@workmax.app",
      "not a url",
      "https://user:pass@example.com/path",
      "http://token@example.com/path",
      "http://127.0.0.1:49152/oauth/callback?code=secret",
      "http://localhost:3000/en/desktop",
      "http://[::1]:49152/oauth/callback",
      "http://10.0.0.2/admin",
      "http://172.16.0.2/admin",
      "http://192.168.0.2/admin",
      "http://169.254.169.254/latest/meta-data",
    ]) {
      assert.equal(normalizeExternalHTTPURL(url), null);
    }
  });
});

describe("sidecar log redaction", () => {
  it("redacts local and cloud bearer tokens", () => {
    const redacted = redactSidecarLogLine(
      'generated ephemeral token: local-secret WORKMAX_LOCAL_TOKEN=abc X-Local-Token: xyz X-Local-Token=equals-secret Authorization: Bearer jwt Authorization: Basic basic-header Basic bare-basic Bearer bare-token access_token="access" refresh_token=refresh id_token:id api_key=api-secret apikey=compact-secret client_secret=client-secret password=password-secret secret=generic-secret'
    );

    assert.match(redacted, /generated ephemeral token: \[REDACTED\]/);
    assert.match(redacted, /WORKMAX_LOCAL_TOKEN=\[REDACTED\]/);
    assert.match(redacted, /X-Local-Token: \[REDACTED\]/);
    assert.match(redacted, /X-Local-Token=\[REDACTED\]/);
    assert.match(redacted, /Authorization: Bearer \[REDACTED\]/);
    assert.match(redacted, /Authorization: Basic \[REDACTED\]/);
    assert.match(redacted, /Bearer \[REDACTED\]/);
    assert.match(redacted, /Basic \[REDACTED\]/);
    assert.match(redacted, /access_token="\[REDACTED\]/);
    assert.match(redacted, /refresh_token=\[REDACTED\]/);
    assert.match(redacted, /id_token:\[REDACTED\]/);
    assert.match(redacted, /api_key=\[REDACTED\]/);
    assert.match(redacted, /apikey=\[REDACTED\]/);
    assert.match(redacted, /client_secret=\[REDACTED\]/);
    assert.match(redacted, /password=\[REDACTED\]/);
    assert.match(redacted, /secret=\[REDACTED\]/);
    assert.doesNotMatch(redacted, /local-secret|abc|xyz|equals-secret|Bearer jwt|bare-token|basic-header|bare-basic|api-secret|compact-secret|client-secret|password-secret|generic-secret|refresh\b/);
  });
});
