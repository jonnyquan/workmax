# Third-Party Notices

WorkMax incorporates third-party software and data. WorkMax's own source code
is licensed under `AGPL-3.0-only`; that license does not replace the licenses
listed here or the licenses shipped with other dependencies.

Exact dependency versions are pinned in `server/go.mod`, `server/go.sum`, and
`desktop/electron/package-lock.json`. The Desktop release process generates a
`third-party-licenses/` bundle for statically linked Go dependencies and copies
Electron's upstream license and Chromium notices into every packaged app.

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

### Electron

- Component: Electron
- Version: `40.10.0`
- Source: <https://github.com/electron/electron/tree/v40.10.0>
- License: MIT; Chromium and bundled components have additional notices that
  are copied from Electron's distribution into Desktop release artifacts.
- Copyright (c) Electron contributors
- Copyright (c) 2013-2020 GitHub Inc.

## MIT License terms

The MIT-licensed components identified above are provided under these terms:

Permission is hereby granted, free of charge, to any person obtaining a copy of
this software and associated documentation files (the "Software"), to deal in
the Software without restriction, including without limitation the rights to
use, copy, modify, merge, publish, distribute, sublicense, and/or sell copies of
the Software, and to permit persons to whom the Software is furnished to do so,
subject to the following conditions:

The copyright notices above and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.

## Maintaining this file

Run `make license-audit` whenever dependencies or bundled data change. Before a
release, follow `RELEASING.md` and inspect the generated license bundle. A tool
report assists review but does not replace verifying the provenance and license
of copied source, generated data, fonts, images, models, templates, or other
non-package-manager assets.
