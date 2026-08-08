# Third-Party Notices

WorkMax incorporates third-party software and data. WorkMax's own source code
is licensed under `AGPL-3.0-only`; that license does not replace the licenses
listed here or the licenses shipped with other dependencies.

Exact dependency versions are pinned in `server/go.mod`, `server/go.sum`, and
`desktop/wails/go.sum`. The Desktop release process generates a
`third-party-licenses/` bundle for statically linked Go dependencies and copies
the shell's dependency notices into every packaged app.

## Components requiring explicit attribution

### Claude Agent SDK Go

- Component: `github.com/jonnyquan/claude-agent-sdk-go`
- Version: `v0.0.0-20260725181726-31b018f42ae4`
- Source: <https://github.com/jonnyquan/claude-agent-sdk-go/tree/31b018f42ae493eefe0987995dde054993f2d609>
- License: MIT
- Copyright (c) 2025 Jonny Quan
- Copyright (c) 2025 Claude Agent SDK Go Contributors

### ip2region

- Components: `github.com/lionsoul2014/ip2region/binding/golang` and
  `server/resource/ip2region/ip2region.xdb`
- Go module version: `v0.0.0-20250508043914-ed57fa5c5274`
- Bundled database SHA-256:
  `867b619b567f51bb9dd3c384a4cbf7c33e71a178aa58f13201499aadaf2cf78e`
- Source: <https://github.com/lionsoul2014/ip2region>
- Upstream license expression: `Apache-2.0 OR MIT`; WorkMax elects MIT for
  redistribution of this component.
- Copyright (c) 2015 Lionsoulchenxin619315@gmail.com

### go-vtracer

- Component: `github.com/yclw/go-vtracer`
- Version: `v0.1.1`
- Source: <https://github.com/yclw/go-vtracer/tree/v0.1.1>
- License: MIT
- Copyright (c) 2025 yoclo

### Wails

- Component: Wails v3
- Version: v3.0.0-beta.5
- Source: <https://github.com/wailsapp/wails/tree/v3.0.0-beta.5>
- License: MIT
- Note: replaced Electron as the desktop shell on 2026-08-08. The Chromium
  notices that accompanied Electron no longer apply — the shell uses the
  operating system's WebView.
- Copyright (c) Lea Anthony and Wails contributors

