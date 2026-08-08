import assert from "node:assert/strict";
import { describe, it } from "node:test";

import { resolveDesktopRenderer } from "./renderer-loader";

const PACKAGED_BUNDLED_URL =
  "file:///Applications/workmax%20Desktop.app/Contents/Resources/renderer/en/desktop/index.html";
const DEVELOPMENT_BUNDLED_URL =
  "file:///workspace/workmax/desktop/renderer/en/desktop/index.html";

describe("Desktop renderer selection", () => {
  it("uses only the bundled renderer in packaged applications", () => {
    assert.deepEqual(
      resolveDesktopRenderer({
        isPackaged: true,
        bundledRendererURL: PACKAGED_BUNDLED_URL,
        bundledRendererExists: true,
      }),
      {
        url: PACKAGED_BUNDLED_URL,
        channel: "packaged-bundled",
      }
    );

    assert.throws(
      () =>
        resolveDesktopRenderer({
          isPackaged: true,
          bundledRendererURL: PACKAGED_BUNDLED_URL,
          bundledRendererExists: true,
          configuredRendererURL: "https://workmax.app/en/desktop",
          trustedRendererOrigins: "https://workmax.app",
        }),
      /packaged Desktop forbids WORKMAX_DESKTOP_RENDERER_URL/
    );
  });

  it("fails closed when the packaged renderer is missing", () => {
    assert.throws(
      () =>
        resolveDesktopRenderer({
          isPackaged: true,
          bundledRendererURL: PACKAGED_BUNDLED_URL,
          bundledRendererExists: false,
        }),
      /refusing remote renderer fallback/
    );
  });

  it("keeps precise validation for malformed packaged overrides", () => {
    assert.throws(
      () =>
        resolveDesktopRenderer({
          isPackaged: true,
          bundledRendererURL: PACKAGED_BUNDLED_URL,
          bundledRendererExists: true,
          configuredRendererURL: "https://workmax.app/",
        }),
      /must point to a \/desktop route/
    );
  });

  it("requires an explicit development renderer", () => {
    for (const configuredRendererURL of [undefined, ""]) {
      assert.throws(
        () =>
          resolveDesktopRenderer({
            isPackaged: false,
            bundledRendererURL: DEVELOPMENT_BUNDLED_URL,
            bundledRendererExists: true,
            configuredRendererURL,
          }),
        /requires WORKMAX_DESKTOP_RENDERER_URL/
      );
    }
  });

  it("accepts the exact repository bundled renderer in development", () => {
    assert.deepEqual(
      resolveDesktopRenderer({
        isPackaged: false,
        bundledRendererURL: DEVELOPMENT_BUNDLED_URL,
        bundledRendererExists: true,
        configuredRendererURL: DEVELOPMENT_BUNDLED_URL,
      }),
      {
        url: DEVELOPMENT_BUNDLED_URL,
        channel: "development-bundled",
      }
    );

    assert.throws(
      () =>
        resolveDesktopRenderer({
          isPackaged: false,
          bundledRendererURL: DEVELOPMENT_BUNDLED_URL,
          bundledRendererExists: false,
          configuredRendererURL: DEVELOPMENT_BUNDLED_URL,
        }),
      /development bundled Desktop renderer is missing/
    );
    assert.throws(
      () =>
        resolveDesktopRenderer({
          isPackaged: false,
          bundledRendererURL: DEVELOPMENT_BUNDLED_URL,
          bundledRendererExists: true,
          configuredRendererURL:
            "file:///tmp/renderer/en/desktop/index.html",
        }),
      /must be the repository bundled Desktop entry/
    );
  });

  it("accepts explicit loopback development servers only", () => {
    for (const configuredRendererURL of [
      "http://localhost:3000/en/desktop",
      "http://127.0.0.1:3000/en/desktop",
      "https://[::1]:3000/en/desktop",
    ]) {
      assert.equal(
        resolveDesktopRenderer({
          isPackaged: false,
          bundledRendererURL: DEVELOPMENT_BUNDLED_URL,
          bundledRendererExists: true,
          configuredRendererURL,
        }).channel,
        "development-loopback"
      );
    }

    for (const configuredRendererURL of [
      "http://localhost.example:3000/en/desktop",
      "http://127.0.0.2:3000/en/desktop",
      "http://192.168.1.20:3000/en/desktop",
    ]) {
      assert.throws(
        () =>
          resolveDesktopRenderer({
            isPackaged: false,
            bundledRendererURL: DEVELOPMENT_BUNDLED_URL,
            bundledRendererExists: true,
            configuredRendererURL,
          }),
        /remote development renderer must use HTTPS/
      );
    }
  });

  it("requires remote HTTPS origins to be explicitly trusted", () => {
    assert.deepEqual(
      resolveDesktopRenderer({
        isPackaged: false,
        bundledRendererURL: DEVELOPMENT_BUNDLED_URL,
        bundledRendererExists: true,
        configuredRendererURL: "https://desktop.dev.example/en/desktop",
        trustedRendererOrigins:
          "https://other.dev.example, https://desktop.dev.example",
      }),
      {
        url: "https://desktop.dev.example/en/desktop",
        channel: "development-trusted-remote",
      }
    );

    assert.throws(
      () =>
        resolveDesktopRenderer({
          isPackaged: false,
          bundledRendererURL: DEVELOPMENT_BUNDLED_URL,
          bundledRendererExists: true,
          configuredRendererURL: "https://desktop.dev.example/en/desktop",
        }),
      /origin is not trusted/
    );
  });

  it("rejects malformed trusted-origin configuration", () => {
    for (const trustedRendererOrigins of [
      "http://desktop.dev.example",
      "https://desktop.dev.example/path",
      "https://user:pass@desktop.dev.example",
      "https://desktop.dev.example,,https://other.dev.example",
      "not a URL",
    ]) {
      assert.throws(() =>
        resolveDesktopRenderer({
          isPackaged: false,
          bundledRendererURL: DEVELOPMENT_BUNDLED_URL,
          bundledRendererExists: true,
          configuredRendererURL: "https://desktop.dev.example/en/desktop",
          trustedRendererOrigins,
        })
      );
    }
  });
});
