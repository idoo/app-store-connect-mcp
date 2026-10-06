# Changelog

Format: [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). Versions: [SemVer](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

[Unreleased]: https://github.com/idoo/app-store-connect-mcp/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/idoo/app-store-connect-mcp/compare/v0.1.3...v0.2.0
[0.1.3]: https://github.com/idoo/app-store-connect-mcp/compare/v0.1.2...v0.1.3
[0.1.2]: https://github.com/idoo/app-store-connect-mcp/releases/tag/v0.1.2
