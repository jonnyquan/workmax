# Contributing to WorkMax

Thank you for helping improve WorkMax. Contributions of code, documentation,
tests, bug reports, and design feedback are welcome.

## Before you start

- Search existing issues before opening a new one.
- For a substantial feature, architecture change, or new dependency, open an
  issue first so scope and design can be agreed before implementation.
- Keep changes within the current product boundary: the Go Server is in
  `server/`, the official client is in `desktop/`, and architecture documents
  are in `ProjectDocs/`.
- Never include credentials, live configuration, user uploads, databases,
  caches, build products, release artifacts, or signing material.

Security vulnerabilities must follow [SECURITY.md](SECURITY.md) and must not be
reported in a public issue.

## Development setup

The required Go and Node.js versions and configuration model are documented in
the [README](README.md). From the repository root, run:

```bash
make doctor
make baseline-audit
make bootstrap
make verify-core
```

## Making a change

1. Create a focused branch from the current default branch.
2. Add or update tests for behavior changes.
3. Keep unrelated formatting and refactors out of the same change.
4. Update public documentation when commands, configuration, APIs, or user
   behavior change.
5. Run `make license-audit` when changing a dependency or bundled third-party
   asset, and update `THIRD_PARTY_NOTICES.md` when attribution changes.
6. Run the smallest relevant test targets, then `make verify-core` when the
   local environment supports the complete suite.

Useful focused targets are listed in the README. Run `make baseline-audit`
before preparing files for review. The audit is read-only; review every path
you stage and avoid broad staging commands.

## Pull requests

A pull request should:

- explain the problem and the chosen solution;
- link related issues or design documents;
- identify compatibility, migration, security, and privacy effects;
- list the verification commands that ran and any checks that could not run;
- include screenshots or recordings for user-visible Desktop changes; and
- remain small enough to review safely whenever practical.

Maintainers may ask for changes before merging. A contribution is not accepted
until it is reviewed and merged.

## Licensing of contributions

Unless you explicitly state otherwise, any contribution intentionally submitted
for inclusion in WorkMax is provided under the
[GNU Affero General Public License v3.0 only](LICENSE) (`AGPL-3.0-only`). Submit
only work that you have the right to license, and preserve required third-party
copyright and attribution notices.
