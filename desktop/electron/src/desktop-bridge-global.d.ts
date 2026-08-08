import type { DesktopBridge, LegacyBridgeResponse } from "./desktop-bridge";

declare global {
  interface Window {
    desktopBridge?: DesktopBridge;
    workmaxLocal?: {
      port: number;
      sidecarVersion: string;
      appVersion: string;
      platform: NodeJS.Platform;
      fetch: (path: string, init?: RequestInit) => Promise<LegacyBridgeResponse>;
      revealDataDir: () => Promise<{
        ok: boolean;
        path?: string;
        error?: string;
      }>;
    };
  }
}

export {};
