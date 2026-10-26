# Changelog

Format: [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). Versions: [SemVer](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.4.0] - 2026-10-26

- A `.asc-mcp.toml` or `.hamn.toml` project pin now stops the server with the `mv` command to rename it, instead of being ignored.
- `doctor` and startup point at a 0.2 config in `~/.config/asc-mcp` or `~/.config/hamn` and print the `mv` to move it.
- Approval prompts show the profile and the exact text Apple will receive; nothing in them is redacted.
- `metadata_pull export_dir` never overwrites a local file that differs; it lists those files under `conflicts`.
- Keyword limits count characters, as Apple does. Non-Latin keyword sets are no longer refused or flagged.
- A batch where every item failed (`metadata_apply`, `appinfo_update`, `pricing_apply`, `screenshots_sync`) is now an error, and each failed item carries its error code.
- A request cancelled before it was sent is reported as cancelled, not as "may have been applied".
- `release_status` without a platform names the app's other in-progress platforms. `apps_get` shows the build attached to each in-flight version.
- `reviews_list mark_seen` no longer skips reviews it didn't return, and multi-app results list the most recent app first.
- `webhooks_upsert` writes a generated secret to `webhook-secrets/` instead of returning it in chat.
- `config install claude-code` succeeds when the server is already registered and prints how to replace it.
- `auth add` and `auth import` check `default_app` against your apps (id, bundle id or exact name) and ask again, or refuse to save without a terminal, when it matches none.
- `metadata_apply` and `screenshots_sync` are marked destructive, and `reviews_list` and `testflight_feedback_list` are marked non-idempotent. TestFlight screenshots are labelled as third-party content.

## [0.3.0] - 2026-10-26

The binary is now `app-store-connect-mcp`. `asc-mcp` collided with two other App Store Connect servers: the npm package `asc-mcp` and zelentsov's Swift server, which also uses `~/.config/asc-mcp` and the client-config key `asc-mcp`. Update your setup:

- **Homebrew:** `brew uninstall asc-mcp && brew install idoo/tap/app-store-connect-mcp`.
- **Client config:** run `app-store-connect-mcp config install <client>`. The server key is now `app-store-connect-mcp`, so Claude Code permissions read `mcp__app-store-connect-mcp__<tool>`. Remove the old `asc-mcp` entry yourself; another server may own it.
- **Renamed:** `~/.config/asc-mcp/config.toml` → `~/.config/app-store-connect-mcp/config.toml`, `.asc-mcp.toml` → `.app-store-connect-mcp.toml`, keychain service `asc-mcp` → `app-store-connect-mcp` (run `app-store-connect-mcp auth import` again), skills `asc-mcp-*` → `app-store-connect-mcp-*`, result metadata `_meta.asc-mcp` → `_meta.app-store-connect-mcp`.
- **Unchanged:** `ASC_MCP_*` and `ASC_*` env vars.

## [0.2.0] - 2026-10-26

Hamn is now **app-store-connect-mcp**, and the binary is `asc-mcp`. Update your setup:

- **Homebrew:** `brew uninstall hamn && brew install idoo/tap/asc-mcp`.
- **Client config:** run `asc-mcp config install <client>` again. The server name is now `asc-mcp`.
- **Renamed:** env vars `HAMN_*` → `ASC_MCP_*`, `.hamn.toml` → `.asc-mcp.toml`, `~/.config/hamn` → `~/.config/asc-mcp`, keychain service `hamn` → `asc-mcp`. Move your config or run `asc-mcp auth import` again.

- Promotional text on a live version, removing IAP or subscription territories, `release_phased remove` and registering new devices now need your approval in the client.
- Approval forms: pressing Accept approves. Clients that only support URL elicitation get setup instructions instead of an error.
- `screenshots_sync` defaults to `add` and deletes nothing unless you pass `mode: replace`.
- `latest-valid` now really picks a VALID build. `release_status` reports a missing build as a check instead of failing, and build lists name other platforms with builds.
- Report decoding streams records within its limits, keeps every section of multi-section reports, and fails instead of dropping rows.
- Fixed the percentage-change sign after a negative period, and subscriber refunds are now counted as a positive number separate from units.
- Client roots that contain your home directory are refused, and `offer-codes/` is git-ignored on first write.
- An empty issuer ID now means an Individual key everywhere. Docs gained Docker recipes and brew caveats, `tools.json` is attested, and `make generate` became `make docs` / `make spec`.

## [0.1.3] - 2026-10-26

- Calling a hidden tool now returns how to unlock it (writes or toolset) instead of "unknown tool".
- A `.hamn.toml` outside `trusted_pins` can no longer pick the account when writes are on, and the account it picks can't be switched to writes at runtime.
- `confirm:true` is refused unless a preview of the same arguments came first, or a human approved in the client.
- `release_prepare` keeps each failed locale's error code and hint for What's New.
- `doctor` says when `ASC_KEY_PATH` is set but `ASC_PRIVATE_KEY_PATH` is not.
- INSTALL says where to find the vendor number.

## [0.1.2] - 2026-10-25

First public release.

- MCP server for App Store Connect over stdio or Streamable HTTP. 68 tools in 14 toolsets, 7 of them on by default.
- Read-only until you enable writes. Anything public, permanent or costing money shows a preview first and then waits for a human yes.
- Releases with a preflight check, TestFlight, metadata in fastlane or Expo format, screenshots, reviews, decoded sales and finance reports, IAPs, signing, Xcode Cloud and IPA/PKG uploads.
- For whatever the workflow tools don't cover: raw API access checked against Apple's pinned OpenAPI spec.
- Team and individual API keys, profiles, keychain storage, and per-project `.hamn.toml` pins that can only narrow permissions.
- Ships via Homebrew, signed release binaries, Docker, a Claude Desktop `.mcpb` and the MCP Registry, plus agent skills you install with `hamn skills install`.

[Unreleased]: https://github.com/idoo/app-store-connect-mcp/compare/v0.4.0...HEAD
[0.4.0]: https://github.com/idoo/app-store-connect-mcp/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/idoo/app-store-connect-mcp/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/idoo/app-store-connect-mcp/compare/v0.1.3...v0.2.0
[0.1.3]: https://github.com/idoo/app-store-connect-mcp/compare/v0.1.2...v0.1.3
[0.1.2]: https://github.com/idoo/app-store-connect-mcp/releases/tag/v0.1.2
