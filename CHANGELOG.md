# Changelog

All notable changes to this project are documented here. The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.1.0] - 2026-10-25

### Added

#### Server and safety

- MCP server over stdio and Streamable HTTP (bearer token, Origin check, loopback by default, TLS for remote).
- Read-only by default: write and destructive tools are withheld until writes are enabled by flag, env, profile, or at runtime with `toolsets` `enable_writes` and human approval. Next steps that need a hidden write tool are marked `locked: "writes_disabled"` with the unlock call.
- Read, write and destructive classes with MCP annotations, checked against tool names by a test.
- Preview and confirm for mutations: `confirm:true` for simple tools, `plan_id` for plan tools (bound to arguments, profile, session and a state fingerprint; single use; 15 minutes).
- Human approval through elicitation for public, irreversible and money actions: review replies, submit/cancel/publish, build expiry, tester removal, pricing, offers and offer codes, users, webhooks, signing revokes, every `asc_api_request` mutation, and external TestFlight distribution. The approval form shows the change first and untrusted text fenced after it. Results report `_meta.hamn.approved_by`. Clients that can't elicit (including protocol `2026-07-28`+) are refused unless the profile sets `allow_no_elicit`.
- HTTP mode keeps write mode per session.
- Third-party text wrapped as `untrusted` (reviews, tester feedback, testers and crash logs in full; free-text fields elsewhere; store listings); redaction of keys, JWTs and signed URLs; URL policy for presigned URLs; file access confined to the project root through `os.Root`.
- One-time offer codes are written to `offer-codes/<batch_id>.csv`, never returned to the agent.
- Toolsets with runtime enable and disable (`tools/list_changed`) and a discovery mode.

#### Tools

- 68 tools in 14 toolsets: core, release, testflight, metadata, reviews, reports (default), plus screenshots, uploads, monetization, signing, xcodecloud, webhooks, admin and store.
- Spec-backed generic layer: `asc_api_search`, `asc_api_describe`, `asc_api_get`, `asc_api_request`, validated against Apple's pinned OpenAPI spec.
- Release preflight, preparation, submission, phased release; TestFlight distribution and feedback with images; metadata pull/plan/apply with fastlane and Expo formats; decoded sales, finance and analytics reports; screenshot sync; IPA/PKG uploads without Xcode.
- Name resolution for apps, versions, builds, locales and groups.

#### Client

- Method-aware retries, `Retry-After` handling, 401 token refresh, rate-limit reporting, validated pagination cursors, compact capped results, error hints.
- Team and individual API keys; profiles in `config.toml`, keychain storage, per-project `.hamn.toml` pins that can only narrow permissions.

#### CLI and distribution

- `hamn auth`, `hamn doctor`, `hamn config snippet|install`, `hamn skills list|install`.
- Agent skills and a Claude Code plugin with a marketplace entry.
- Release binaries for macOS, Linux and Windows with checksums, SBOM and build provenance; install script that verifies the sha256 and, with `gh`, the provenance attestation (refusing to install on failure); Homebrew formula; Docker image; Claude Desktop `.mcpb` bundle; MCP Registry entry.
- Weekly spec drift check.

[Unreleased]: https://github.com/idoo/hamn/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/idoo/hamn/releases/tag/v0.1.0
