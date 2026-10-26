# Changelog

Format: [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). Versions: [SemVer](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.5.0] - 2026-10-26

### Added

- New `insights` toolset (off by default):
  - `release_health_report` compares hangs, launches, disk writes, performance regressions, TestFlight crashes and ratings with the previous version, and says `healthy`, `watch`, `breach`, or `unknown` while Apple has no production data yet.
  - `digest_summary` shows what changed since you last asked: reviews, TestFlight feedback, review and build states, yesterday's sales.
  - `analytics_funnel_report` answers impressions → product page views → downloads by source and territory, period over period.
- `app-store-connect-mcp guard` runs the health check from cron or CI (exit 0 healthy, 3 watch, 4 breach, 5 no data yet) and with `--pause-on-breach` can pause an active phased rollout. It never resumes, completes or starts one.
- `store_ranks_report` (store toolset) and `app-store-connect-mcp aso` track your search rank per keyword and country over time.
- `release_status` warns about missing Accessibility Nutrition Labels and inconsistent 2025 age-rating answers. `appinfo_update` creates and publishes accessibility declarations, with your approval.
- In clients with MCP Apps (Claude Desktop, claude.ai, ChatGPT, VS Code), release readiness, metadata plans, screenshots, reviews and the analytics funnel open as interactive views. Buttons go through the same preview and approval steps.
- Large outputs (report rows, crash logs, oversized results) come back as resource links instead of filling the chat. Prompts and resources autocomplete apps, platforms and API operations.
- New prompts `daily_digest` and `post_release_check`.
- CI: `ASC_MCP_ALLOW_NO_ELICIT=1` together with `ASC_MCP_CI=1` lets a pipeline run approval-gated actions. `doctor --json` gives a machine-readable report. `serve --allow-origin` accepts remote browser clients.

### Fixed

- Batch actions (expire builds, distribute, register devices, capabilities, IAP and subscription upserts, review replies) report an error when nothing was applied, and mark items that may have been applied anyway.
- A create that succeeded but returned a broken response is reported as "may have been applied", so the agent won't create it twice. A retried delete that already went through counts as done. Deletes and updates that run out of retries say the outcome is unknown.
- `reviews_list mark_seen` with filters and `reviews_reply` no longer mark unseen reviews as seen.
- Metadata plans for iOS+macOS apps work while the other platform is in review, refuse new languages that can't get a store name yet, and say which platform they cover. `translate_metadata` takes a `platform`.
- Big inline metadata plans no longer echo the whole listing back. `screenshots_list` makes far fewer API calls. Two screenshot folders for the same locale are flagged.
- A generated webhook secret is saved before the request, so it survives a lost response. A repo's `webhook-secrets/.gitignore` must actually ignore secrets.
- Tester device fields are checked against their real shapes. App and group names in approval prompts are quoted.
- The server starts when the state directory isn't writable; only cursors and history are unavailable, and `doctor` says so.
- An empty `ASC_PRIVATE_KEY` is reported as a missing secret. The Dockerfile HTTP example works. HTTP-mode advice says to restart the server. The env-profile vendor hint names `ASC_VENDOR_NUMBER`. Oversized and non-JSON responses get their own error codes.

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

[Unreleased]: https://github.com/idoo/app-store-connect-mcp/compare/v0.5.0...HEAD
[0.5.0]: https://github.com/idoo/app-store-connect-mcp/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/idoo/app-store-connect-mcp/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/idoo/app-store-connect-mcp/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/idoo/app-store-connect-mcp/compare/v0.1.3...v0.2.0
[0.1.3]: https://github.com/idoo/app-store-connect-mcp/compare/v0.1.2...v0.1.3
[0.1.2]: https://github.com/idoo/app-store-connect-mcp/releases/tag/v0.1.2
