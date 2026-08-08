# Changelog

Notable changes to WorkMax will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and the project intends to follow [Semantic Versioning](https://semver.org/) once
its public API and release channels stabilize.

## [Unreleased]

### Added

- Open-source governance, security, support, issue, and pull-request guidance.
- AGPL source-code access from the bundled Desktop renderer.
- CI, full-history secret scanning, dependency updates, and license auditing.
- Third-party notices and release-time license bundling.
- Product runtime docs: local-first Desktop + `https://workmax.app` auth, and
  WorkMax Plus commercial sibling boundary
  (`ProjectDocs/oss-local-desktop-runtime-mode-2026-08.md`,
  `ProjectDocs/adr/2026-08-08-workmax-plus-repository-boundary.md`).
- Desktop model route settings: Sidecar `GET|PUT /settings/model-route`,
  Keychain API key storage, typed bridge `settings.*` (`1.0.0-alpha.7`), and
  bundled Models UI (local inference not wired yet).

### Changed

- Licensed WorkMax under `AGPL-3.0-only`.
- Replaced the developer-local Claude Agent SDK override with a pinned,
  publicly resolvable Go module version.
- Desktop loopback inventory is 24 routes (was 22 at Alpha.6 recovery);
  living design baseline bumped to v1.46 for alpha.7 / settings / OSS mode.

[Unreleased]: https://github.com/jonnyquan/workmax/compare/HEAD
