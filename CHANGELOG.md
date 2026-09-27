# Changelog

All notable changes to this project are documented here. The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.1.1] - 2026-10-26

### Added

- `hamn serve --root DIR` and `HAMN_ROOT` choose the directory file tools may use. Without them hamn uses the client's MCP roots, then the git root, and refuses an automatically picked `/` or home directory. `doctor` shows the root as `file_root`.
- HTTP session limits: `--http-max-sessions` / `HAMN_HTTP_MAX_SESSIONS` (default 64; past the cap a new session gets 503 with `Retry-After`) and `--http-session-timeout` / `HAMN_HTTP_SESSION_TIMEOUT` (default 30 minutes idle).
- `hamn auth add`/`import` ask whether the key is a team or individual key (or take `--kind`), require the issuer ID for team keys, and test the key against Apple before saving (`--no-verify` skips it).
- `hamn auth set env vendor_number=<number>` stores the vendor number for people using `ASC_*` environment variables.
- `--help` on every subcommand prints usage and does nothing else.
- `iap_upsert` and `subscriptions_upsert` take an `availability` argument (territories or all, plus availability in new territories).
- `release_submit` attaches ready first-time IAPs and subscriptions by default and lists them in the preview; opt out with `attach_first_time_products: false`.
- `pricing_get` returns prices for up to 10 requested territories, plus the territories with no price.
- `metadata_pull` takes a `fields` filter; `screenshots_list` reports per-locale coverage of required display types.
- Metadata plans warn about keyword spaces, empty or duplicate keywords and keywords already in the name, unknown fastlane/Expo keys, and locales folded onto another (`locale_mapped`).
- `previews_upload` checks the video before reserving an upload: duration must be 15–30 s and the poster time inside the video; an unexpected resolution or unreadable container is a warning.
- A `restore` sales category for `IA3`/`IA3-M` rows.
- OpenSSF Scorecard workflow and README badge; Windows test leg in CI.

### Changed

- Finance reports no longer estimate USD totals. Apple only shows its exchange rates on the website's Payments page, so `finance_report` returns per-currency totals and a note, and `sales_report` dropped `with_usd_estimate`. Without a month, `finance_report` falls back to the month before the last closed one when that isn't published yet.
- `insights_summary` compares windows of equal length and reports how many days each covers.
- Sales units are exact decimals, so fractional and negative rows add up.
- `reviews_reply` replaces an existing response with a single POST and reports `replaced_response_id`; it never deletes first.
- `release_submit` runs the full preflight first and refuses while any check blocks.
- `release_status` and `release_prepare` match `latest` builds to the version string, and block when the attached build belongs to another version. Unanswered age-rating questions now block. An iOS version with no iPad screenshots gets a warning instead of `ok`. Checks on a released version are informational.
- `release_prepare` sets What's New on a version it just created in the same call.
- `pricing_plan` for subscriptions keeps existing subscribers' prices by default (`preserve_current_price`), says what happens to them, and counts increases and decreases. Moving an app's base price re-equalizes the old base territory; `reset_manual_prices` drops manual prices.
- `offers_create` treats an identical intro offer as `already`, and replaces one with different terms only when `start_date` lets it end the old offer first. `offer_codes_generate` returns a matching batch created in the last hour as `already`.
- `builds_wait` caps the wait at 50 s when the client sent no progress token, then returns `still_processing` with a resume step.
- `xcc_build_status` picks the app's only workflow when none is named, and lists candidates when there are several.
- `toolsets` is annotated as a write tool (it can turn writes on) and stays listed in read-only mode; enable and disable return only the toolsets that changed. `asc_api_search` hits carry `operation_id`.
- A `writes_disabled` error from any tool now points at `toolsets` `enable_writes` instead of a restart. The server instructions name `asc_api_get` as the read path.
- Metadata: screenshot display slots prefer current devices and read device names from file names; app info is edited only in editable states; on a live version only promotional text changes; clearing a field sends `null`; folders sort naturally.
- Review and feedback cursors are stored one file per cursor under the state dir's `cursors/`; a 0.1.0 `cursors.json` is still read.
- `config install` writes the `hamn` on your PATH (Homebrew's `bin/hamn`, not the Cellar path), skips unchanged files, and for `claude-code` runs `claude mcp add` itself. Codex configs get `startup_timeout_sec` and `tool_timeout_sec`.
- `~` is expanded in `key_path` and `ASC_PRIVATE_KEY_PATH`. Config parse errors show line and column.
- Input schemas no longer use `["null", T]` type arrays, which some clients reject.
- Release artifacts: a `.mcpb.sha256`, a universal macOS binary and Linux arm64 in the `.mcpb`, a cosign signature on `checksums.txt` (checked by `install.sh` when cosign is installed; `HAMN_REQUIRE_COSIGN=1` makes it mandatory), and an attested Docker image.

### Fixed

- A truncated list result drops `next_cursor` instead of handing out a cursor that skips items.
- HTTP mode built a new server for almost every request; it now builds one per session.
- Clients on MCP `2026-07-28` couldn't show approval forms. hamn now advertises protocol versions up to `2025-11-25`, so those clients fall back and approvals work.
- The rate-limit gauge no longer jumps back up on a stale reading, and hamn stops calling Apple once the hourly quota is spent.
- Concurrent 401s refresh the token once; a 401 caused by clock skew reports the skew instead of refreshing in a loop.
- `testflight_distribute` reports a rejected beta review as a failure, not `already`.
- IAPs in `READY_TO_SUBMIT` count as ready; rejected versioned products no longer disappear from preflight.
- Subscription event and subscriber rows match the app by App Apple ID.
- Without credentials, tools return `no_credentials` with setup steps instead of a generic error.
- `doctor` warns when the key id doesn't match the `AuthKey_<ID>.p8` file name or the issuer isn't a UUID, and its 401 output is a checklist.

### Security

- Human approval is now also required for `iap_upsert` / `subscriptions_upsert` creating a product, turning family sharing on or changing a subscription's level; `release_phased` with `complete`; and `signing_capabilities_set` disabling a capability.
- `iap_upsert` and `subscriptions_upsert` preview every change and need `confirm` before overwriting; they PATCH only changed fields.
- `asc_api_get` and `asc_api_request` refuse API paths containing `%` or `.` segments.
- The binary plist decoder used on IPA uploads has a node budget, so a crafted plist can't exhaust memory.
- A config directory writable by group or others is refused.
- A test scans every committed golden, fixture and `docs/tools.json` for secrets.
- List tools take their page size as `limit`; a truncated result's hint says to re-run with a smaller `limit`.
- CI: pinned Go toolchain, Scorecard, spec-drift regeneration split from the job that can open PRs, Docker base images pinned by digest.

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

- 68 tools in 14 toolsets: core, release, testflight, metadata, reviews, reports and screenshots (default), plus uploads, monetization, signing, xcodecloud, webhooks, admin and store.
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

[Unreleased]: https://github.com/idoo/hamn/compare/v0.1.1...HEAD
[0.1.1]: https://github.com/idoo/hamn/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/idoo/hamn/releases/tag/v0.1.0
