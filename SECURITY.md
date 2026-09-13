# Security policy

## Supported versions

hamn is young. Security fixes go into the latest release only; please upgrade before reporting.

| Version | Supported |
|---|---|
| latest 0.x release | ✓ |
| older | ✗ |

## Reporting a vulnerability

Please use GitHub's private vulnerability reporting: **Security → Report a vulnerability** on [github.com/idoo/hamn](https://github.com/idoo/hamn/security/advisories/new). Don't open a public issue for anything exploitable.

I'm one person, so expect an acknowledgement within a few days and a fix or a plan within two weeks for anything serious. I'll credit you in the advisory unless you'd rather not be named.

## In scope

- A way for tool input, Apple-sourced text or a repository file to cause a write, a public action or a destructive action without the documented preview and approval.
- Key material, JWTs or signed URLs leaking into tool results, errors or logs.
- The JWT being sent anywhere other than the App Store Connect API.
- Bypassing the URL policy for presigned uploads and downloads, the pagination cursor checks or the file confinement.
- Bypassing the HTTP transport's token, Origin or loopback checks.
- Problems in release artifacts, checksums or provenance.

## Out of scope

- A model following injected instructions *and* a human approving the result. hamn reduces this risk; it can't remove it. See [docs/SAFETY.md](docs/SAFETY.md).
- Configurations that turn safety off on purpose: auto-approving every tool, `allow_no_elicit`, `--no-confirm` with `HAMN_CI=1`.
- Other processes running as your user reading your keychain or key files.
- Apple's API behavior and permissions.

## How hamn handles keys

- Keys are read from a file path, an environment variable or the OS keychain. The config file stores paths or keychain references, never key material.
- Only the `hamn auth` CLI can add or change credentials. No MCP tool accepts, returns or edits them.
- JWTs are signed locally, cached in memory per profile for at most 19 minutes and only sent to `api.appstoreconnect.apple.com`.
- JWTs, PEM blocks, `Authorization` headers and signed URL queries are redacted from results, errors and logs.
- hamn has no telemetry and talks to nothing except Apple (and the public iTunes Search API for `store_lookup`).
