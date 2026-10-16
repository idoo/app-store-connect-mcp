# Changelog

All notable changes to this project are documented here. The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.1.0] - 2026-10-25

### Added

#### Server and safety

- MCP server over stdio and Streamable HTTP (bearer token, Origin check, loopback by default, TLS for remote).
- Read-only by default: write and destructive tools are withheld until writes are enabled by flag, env, profile, or at runtime with human approval.
- Read, write and destructive classes with MCP annotations, checked against tool names by a test.
- Preview and confirm for mutations: `confirm:true` previews, `plan_id` plans bound to arguments, session and a state fingerprint, human approval through elicitation for public and irreversible actions.
- Third-party text wrapped as `untrusted`; redaction of keys, JWTs and signed URLs; URL policy for presigned URLs; file access confined to the project root.
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
- Release binaries for macOS, Linux and Windows with checksums, SBOM and build provenance; install script; Homebrew formula; Docker image; Claude Desktop `.mcpb` bundle; MCP Registry entry.
- Weekly spec drift check.

[Unreleased]: https://github.com/idoo/hamn/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/idoo/hamn/releases/tag/v0.1.0
