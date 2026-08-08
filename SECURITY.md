# Security Policy

## Supported versions

WorkMax is currently pre-1.0. Security fixes are applied to the default branch
and, when practical, the latest published pre-release. Older snapshots and
pre-release binaries are not supported.

| Version | Supported |
|---|---|
| Default branch | Yes |
| Latest pre-release | Best effort |
| Older snapshots or pre-releases | No |

## Reporting a vulnerability

Do not disclose a suspected vulnerability in a public issue, discussion, pull
request, or social-media post.

Use GitHub's private vulnerability reporting for this repository when it is
available. Otherwise email `support@workmax.app` with `[SECURITY]` at the start
of the subject. Include:

- the affected version, commit, component, and configuration;
- reproduction steps or a minimal proof of concept;
- the expected and observed behavior;
- the likely impact and any known mitigations; and
- a safe way to contact you for follow-up.

Do not include real user data, live credentials, or destructive payloads. Use
the minimum access necessary to demonstrate the problem.

The maintainers will acknowledge the report, assess severity and affected
versions, and coordinate remediation and disclosure. Timing depends on impact
and fix complexity. Please allow a reasonable remediation period before public
disclosure.

## Scope

Reports about WorkMax source code, official release artifacts, authentication,
credential handling, data isolation, update integrity, and Server/Desktop trust
boundaries are in scope. Vulnerabilities that exist only in an upstream service
or dependency should normally be reported to that project's security contact;
please notify WorkMax as well when the issue is exploitable through WorkMax.

Account support, billing questions, and ordinary bugs are not security reports.
Use [SUPPORT.md](SUPPORT.md) or the public issue templates for those topics.
