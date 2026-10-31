# Changelog

Format: [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). Versions: [SemVer](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.17.0] - 2026-10-28

### Added

- `finance_report` works with a key that has no Finance or Admin role, once you've signed in with `app-store-connect-mcp web login`. When Apple refuses `/v1/financeReports`, it reads the month from the web Payments page instead (`data_source: "web_payments"`): each currency's earnings and units, plus `payments[]` with status, bank currency and amount, and per region the balance carried forward, taxes and fees, adjustments, amount owed, bank amount and exchange rate once Apple sets one. That's more than the API gives, which has no exchange rates. A month Apple hasn't closed yet is `pending`. Per-product rows still need the Finance role, and `region`, `report_type`, `top_n` and `include_rows` don't apply on this path, so passing them adds a warning. Without a web session the 403 hint now also suggests `web_login`.
- `release_health_report` takes production crashes from the App Analytics dashboard when Analytics Reports aren't set up or the key isn't Admin (`data_source: "web_dashboard"`, with the window in whole Pacific days). The dashboard lags about 2 days, so a window that starts after its last data day is `insufficient_data`, and so is an app that was never on the App Store. `analytics_setup` is only suggested when neither source works. Analytics crashes still never decide the verdict.

### Changed

- `release_health_report` says `unavailable` (needs Admin) instead of `error` when Apple refuses the crash report to the key.

## [0.16.0] - 2026-10-28

### Added

- Stats work without an Admin key or a vendor number, once you've signed in with `app-store-connect-mcp web login`:
  - Sales and finance tools, the digest and `doctor` read the vendor number from Payments and Financial Reports when the profile has none, and remember it in the state dir. Results then say `vendor_number_source: "web_session"`. Several vendor numbers are listed in a `vendor_number_ambiguous` error instead of picking one.
  - `analytics_funnel_report` reads the App Analytics dashboard when the Analytics Reports API isn't set up for the app or refuses the key (no Admin role). That covers funnel, sources, territories, sessions, crashes and installs vs deletions, plus downloads, redownloads, conversion rate and proceeds. Results say `data_source: "web_dashboard"`, and normal ones say `data_source: "analytics_reports"`. Periods end on the dashboard's last day with data, and a later `to` is cut back with a note.
  - `insights_summary` without an app lists every app's impressions, page views, downloads, sessions, crashes, proceeds and conversion rate against the previous period, sorted by downloads, with account totals. Apps that were never on the App Store are listed under `no_data`, and one app's failure doesn't hide the others. With an app, it uses the dashboard when the Reports API can't answer.
- An app that has never been on the App Store gets `status: "no_data"` from `analytics_funnel_report` instead of an error.

### Changed

- `vendor_number_missing`, `analytics_not_set_up` and the `analytics_setup` 403 now name `web_login` as the quickest fix. The 403 also says Analytics Reports need an Admin key.
- `finance_report` 403 with a vendor number from the web session says the key needs the Finance or Admin role, instead of doubting the vendor number.

### Security

- Web-session calls outside iris are limited to App Analytics (`/analytics/api/v1/`), the Payments vendor list (`/WebObjects/iTunesConnect.woa/ra/paymentConsolidation/`) and `/olympus/v1/session`, GET and POST only, never following redirects, at most 4 at a time. Cookies stay out of errors and logs.

## [0.15.0] - 2026-10-27

### Added

- New opt-in `web` toolset for what Apple's API can't do, through your Apple ID web session (`app-store-connect-mcp web login`; tools never see the Apple ID, password or cookies):
  - `apps_create` creates an app record: name, bundle id (optionally registered first), SKU, platforms, primary locale, first version. An existing app with the same name and bundle id counts as done, and a name, SKU or bundle id conflict is named in the error. Needs your approval: the record can't be deleted.
  - `privacy_get` shows the App Privacy answers per data category (purposes, linked or not, tracking, or data not collected), whether they're published, and with `catalog:true` the valid tokens.
  - `privacy_plan` and `privacy_apply` replace the whole declaration and publish it. Tokens are checked against Apple's catalog, every removal is listed, steps run in an order that never leaves a gap longer than Apple forces, and a failure reports each step as done, outcome unknown or not attempted. Publishing needs your approval.
- `release_status` reads the App Privacy publish state when a web session exists: published is ok, unpublished blocks with `privacy_plan` as the fix. Without a session the check stays manual and says how to sign in.
- `web login` signs you in through a browser window on Apple's own page (password, 2FA, passkeys), because Apple refuses password sign-in from scripts with error -20209. The window uses a dedicated profile in your Brave, Chrome, Edge or Arc, or a downloaded Chrome for Testing; `ASC_MCP_BROWSER` picks a browser or `bundled`. Your own browser's cookies and Keychain are never read.
- Expired web sessions renew silently: the same browser profile signs in again headless, and you only see a window when Apple wants you.
- `web_login` tool: an agent can open the sign-in window when a tool says `web_session_required`. It reuses a valid session, opens one window at a time and pauses for a minute after a cancelled or failed sign-in. The agent never sees credentials.
- `web login --password` keeps the terminal Apple ID + password + 2FA flow for machines without a screen.

### Security

- macOS binaries, in the archives and in the `.mcpb`, are signed with a Developer ID and notarized by Apple. Gatekeeper opens them without a warning, and `spctl -a -vv -t install` reports `Notarized Developer ID`.
- Each release also carries its build provenance as `app-store-connect-mcp_<version>.intoto.jsonl`, so `gh attestation verify --bundle` works offline.

## [0.14.0] - 2026-10-27

### Fixed

**Writes**

- `screenshots_plan` treats only COMPLETE screenshots as already uploaded. FAILED ones are uploaded again, and old screenshots are kept while a replacement is still processing, so following the suggested replan can no longer delete the good one.
- `screenshots_sync` reports a replace that is still processing as still processing, not as a partial failure. Deletes held for that reason are counted in `held`.
- `library_plan` reports `display_mismatch` when a folder names a display or group (such as `APP_IPHONE_DUO`) that the image doesn't fit, instead of placing it in another public set that happens to match the size.
- A `null` inside a free-form object argument, such as `appinfo_update` `age_rating:{kidsAgeBand:null}`, reaches Apple again and clears the value. 0.13.0 dropped it silently.
- `offers_create` no longer reports a promotional or win-back offer as already existing when its territory prices differ from the request.
- `signing_revoke` with `confirm:true` on a missing id returns not found again, instead of reporting a successful revoke.
- `metadata_plan` no longer proposes a change when a field is empty locally and unset in App Store Connect (fastlane's empty `marketing_url.txt`), so applying twice ends in `no_changes`.
- On an app with only a live version, `metadata_plan` compares app-info fields with the live app info. An unmodified `metadata_pull` export is no longer `invalid`, and promo-text-only edits can be planned.
- `builds_upload` resumes an upload whose file reservation was interrupted, instead of blocking that build number for good with "wait for the existing upload". An upload held by another file gets a new `stale_build_upload` error that says to re-run with the original file or bump the build number.

**Numbers and state**

- `pricing_get` with `products` shows each territory's next scheduled price and its start date. A price rise starting tomorrow was only a count before.
- A sales day counts as zero sales only when Apple says there were no sales. Days Apple publishes late stay pending instead of showing as a −100% drop.
- SUBSCRIPTION reports warn when the active-subscription counts come from an earlier day.
- `testflight_feedback_list` with a build, platform or device filter no longer moves the app-wide last-seen mark. Review and feedback last-seen marks never move backwards.
- `release_status`: iPhone Duo screenshots can be added through the Asset Library (`library_plan`, folder `<locale>/APP_IPHONE_DUO/`, 1398x2034 PNG). A missing Duo slot warns before 2027-04-01 and blocks after.
- `screenshots_list` says when it shows the live version because no editable one exists, and points to `release_prepare`.

**Agent and client experience**

- Next steps and fix buttons that point at a tool in a disabled toolset are marked `toolset_disabled` with the call that turns it on. On stateless HTTP they are marked `stateless_fixed` with a restart hint and never point at `toolsets` calls the server refuses.
- Array arguments accept a single value, so `reviews_list rating:1` works the first time.
- The "which app?" error of `reviews_list`, `digest_summary` and `insights_summary` offers `app:"all"`.
- Prompt completion suggests App Store locale codes for `from_locale` and `to_locales`.
- Claude Desktop views: the release view shows the app's other platforms (including a rejected macOS version), next steps and App Review attachment names; its Fix buttons keep the version and platform you were checking and unlock switched-off tools first. The metadata view no longer shows a green "ok" for a plan that can't be applied.

## [0.13.0] - 2026-10-26

### Fixed

**Writes that could leave a listing worse than before**

- `library_apply` in replace mode no longer deletes a group's old screenshots when a new placement fails, is skipped or is still processing. Those deletes are reported as not attempted, with a hint to run `library_plan` again.
- Replacing a full Asset Library group frees one slot just before each new placement instead of emptying the group first, and stops deleting after a failed placement.
- `screenshots_sync` in replace mode deletes old screenshots only after their replacements finish processing. If processing fails or is still running, the old ones stay and you're asked to plan again.
- When a screenshot set is full, `screenshots_sync` first deletes only as many screenshots as it needs to stay within 10, and the preview shows that order.
- `screenshots_sync` keeps its result when checking processing hits an error or is cancelled: you see what was applied, uploads marked state unknown, and `screenshots_list` as the next step. A brief 404 right after an upload counts as still processing.
- `pricing_apply` keeps the start and end dates of manual prices in territories it doesn't change, so a temporary sale price no longer becomes permanent.
- `pricing_apply` carries over future scheduled manual prices in territories it doesn't change. Any it has to drop are named in the preview with territory, price and date.
- `offers_create` says a promotional or win-back offer already exists only when its terms match. A name or identifier clash with different terms is an error naming the existing offer.
- `offers_create` no longer files a rejected intro offer as skipped unless an identical offer exists. If replacing an intro offer fails after the old one was ended, the error says which offer ended, where, on what date, and how to recover.

**Confirms and partial failures**

- `builds_expire` and `release_cancel` confirms apply only to the builds or submission state the preview showed. If `latest` moved or the submission changed, the confirm is refused and a new preview is needed.
- A `release_submit` plan goes stale if another first-time in-app purchase or subscription becomes ready after the preview, so nothing the plan didn't list goes to App Review.
- When `release_submit` fails part-way, the result reports the submission id, whether it was created, resolved items and each item's outcome, instead of a bare error.
- `xcc_build_start` reports a started build when Apple's reply is empty. After a timeout or server error it points at the run list instead of inviting a retry that would start a second build.
- `signing_devices_register` counts a device as registered when Apple's reply is empty but the device exists. Re-running a confirmed `signing_revoke` whose delete already went through reports `already`.
- The `signing_revoke` preview counts every profile that uses a certificate, including profiles with more than 50 certificates.

**Multi-platform apps**

- `release_review_status` without a platform lists open or rejected submissions on the app's other platforms (a rejected macOS version was invisible from the iOS view).
- `builds_wait` without a platform finds the build on the app's other platforms, like `builds_list` does.
- `release_health_report` and the `guard` command check the platform the app is live on, for example macOS for Mac-only apps, instead of always iOS.
- `release_status` and `screenshots_list` no longer suggest a `library_plan` call that always failed; they explain it needs `dir`, `remove` or `archive`.

**Reports and state**

- Days with no sales are reported as zero-sales days (`no_sales_days`) instead of "not published yet", and `insights_summary` no longer shortens its comparison because of them.
- Sales and finance errors (403 or Apple server error) mention that the vendor number may be wrong.
- `subscriptions_list` shows each group localization's state, so a live name and a pending edit in the same locale can be told apart.
- Overlapping `store_ranks_report` track/untrack calls and overlapping `library_apply` runs no longer lose each other's saved data.

**Setup and protocol**

- `auth add` and `auth import` accept key paths pasted in quotes (Windows "Copy as path") or with backslash-escaped spaces (dragged into macOS Terminal).
- Optional arguments sent as `null` are treated as omitted instead of failing with an internal type error.
- The `daily_digest` and `post_release_check` prompts tell the agent to turn on the `insights` toolset first when it is off.

## [0.12.0] - 2026-10-26

### Added

- **Asset Library writes**, in the new opt-in `library` toolset.
  - `library_plan` diffs a fastlane-style folder against the library placements of a version, custom product page, experiment treatment or in-app event.
  - Folders can also hold `header.*`, `search.*`, `header-search.*`, `event-card.*` and `event-details.*` creatives.
  - `library_apply` runs the plan: upload, place, reorder, then delete or archive.
  - Files are checked locally against Apple's own spec data (size, alpha, type, per-group limits) and filtered to the target's platform.
  - An asset already in the library is reused instead of uploaded again.
  - Removing, archiving or replacing needs your approval.
- `release_status` reads the attached build's device list: a build with a watch app needs Watch screenshots, and watch-only or iMessage-only apps aren't asked for iPhone screenshots.
- `builds_upload` reads the platform from the `.ipa`'s Info.plist and refuses an explicit `platform` that disagrees.
- `asc_api_get` returns a summary and a resource link for a single resource too big to inline, such as `appAssetLibraryRefData`.

### Fixed

- Library images that are rejected, archived, still uploading or missing no longer count as filling a screenshot slot. `release_status` says why each one doesn't count, and warns on unknown placement states.
- A screenshot that appears both in a set and as a library placement is counted once.
- `screenshots_plan` accepted no alpha anywhere; Mac screenshots may have it.
- `screenshots_plan` now accepts 8 legacy 3.5", 4" and 9.7" iPad sizes that Apple still takes.
- Money keeps every digit Apple sends: JPY `255.5` stays `255.5`, and totals are no longer rounded.
- `ParseAmount` no longer reads `0x10` as 16.
- `asc_api_get` rejects `query.limit` above 200 and a negative `limit` before calling Apple.
- `asc_api_get` keeps all text from reviews, testers and feedback marked untrusted, including all-caps words and dates.

## [0.11.0] - 2026-10-26

### Added

- **App Asset Library.** `screenshots_list` takes `source: sets|library|both` (default `both`) and shows library placements per locale, plus header and search-result creatives. `release_status` counts library placements toward required screenshot slots, using Apple's own placement-group table from `appAssetLibraryRefData`. iMessage groups never count, and unknown groups are listed.
- `asset_packs_list` (toolset `uploads`): Background Assets packs and versions, with App Store, external and internal beta states and Apple's processing warnings (e.g. ITMS-91139).
- `appinfo_update review_attachments: {add, remove}` uploads or removes App Review attachments; removal goes through confirm. After a partial failure the next step retries only what didn't land. `release_status` lists the attachments.
- `reviews_list summary: true` returns Apple's review summaries per platform, or `none_yet`.
- `signing_profiles_list expiring_within_days`.
- Every result's `_meta` includes `requests.apple`: how many Apple requests that call made, retries and pages included.
- `digest_summary app:"all"` puts apps with something in flight first, lists `skipped_apps`, and takes `offset` for the next batch. Its next steps use real app ids. `insights_summary` without an app lists in-flight versions across all apps from one request.
- `sales_report` and the sales summaries show `refunds` next to net units. `pricing_get` shows year-2 subscription proceeds.

### Fixed

- `release_status` reported "no screenshots" for slots filled only through the Asset Library.
- HUF, TWD and IDR amounts lost their decimals (ISO 4217 gives them 2).
- Currencies in proceeds lists are sorted by code, not by raw amount, so JPY no longer outranks USD.
- `pricing_get` prices use the currency's decimals (`1.40`, not `1.4`).
- Amounts with thousands separators (`1,234.50`) no longer mark a report corrupt.
- `asc_api_get` sent `limit` to endpoints that reject it (`appAssetLibraryRefData` returned 400) and ignored `query.limit`.
- `asc_api_get` no longer wraps Apple timestamps and enum values as untrusted text.
- `app:"all"` in a single-app tool returns a clear `invalid_arguments` listing the tools that accept it, instead of `not_found`.
- The app name is untrusted text in every tool's output, not only some.

## [0.10.0] - 2026-10-26

### Added

- `config snippet|install <client> --allow-writes` turns writes on for that one client only. The locked-tool hint now points at it.
- `docs/FAQ.md`: upgrading, rotating a key, uninstalling, and the errors people actually hit (401, missing `.p8`, env overriding a profile, Docker can't read the key).
- The README says what MCP is and has a three-step quickstart.

### Changed

- Leaving out the app when you have several now says "which app? you have N apps" with the real count, and mentions `default_app` and the project pin.
- `insights_summary` without a vendor number tells you the exact `auth set … vendor_number=` command instead of suggesting `sales_report`, which would fail the same way. `auth add`/`auth import` stop calling the vendor number optional.
- `doctor` warns when `ASC_*` variables in your shell override a saved profile.
- `auth import` names the variable for each missing piece, and says so plainly when stdin isn't a terminal instead of `read answer: EOF`. A missing `.p8` comes with a fix.
- `config snippet` and `install.sh` show the credentials step first, and skip it once a profile exists.
- `toolsets` with no arguments lists the toolsets.
- `guard --help` shows its thresholds.
- Internals: one public-address policy for every outbound check (`internal/netpolicy`), one set of JSON:API helpers, one version resolver for metadata and screenshots, no test hooks in package variables, dead code removed, and CI fails on new dead code. The URL check in `release_status` now also refuses addresses that aren't global unicast.

### Fixed

- SAFETY.md's list of human-approved actions was missing `appinfo_update` (publishing accessibility labels) and `testflight_groups_upsert` (turning on a public link).
- The `.mcpb` listed 6 of the 8 prompts.
- `make docs` failed after any hand edit under `docs/`.

## [0.9.0] - 2026-10-26

### Added

- `--http-max-conns` / `ASC_MCP_HTTP_MAX_CONNS` caps open HTTP connections (default 256).
- `install.sh` is attached to every release and covered by the signed checksums, so the one-liner is `curl -fsSL https://github.com/idoo/app-store-connect-mcp/releases/latest/download/install.sh | sh`. `ASC_MCP_REQUIRE_SIGNATURE=1` makes it refuse anything it can't verify.
- Releases ship a cosign-signed `checksums-extra.txt` for the `.mcpb` and `tools.json`, and the release job fails unless the published release is immutable.

### Changed

- HTTP mode reads each request within 60 s, closes idle connections after 2 minutes, caps headers at 64 KiB and closes the connection on every refusal. A slow or never-ending body can't hold a connection open any more.
- At the session cap, a session idle for 10 minutes makes room, and the `503` says honestly when the next slot frees up.
- JSON-RPC batches are accepted only from clients on `2025-03-26` or older, at most 100 messages.
- `Bearer` is matched case-insensitively.
- Numbers and booleans sent as strings (`"3"`, `"true"`) are accepted; anything else still fails validation. `reviews_list rating` uses a 1–5 range instead of an integer enum, which Gemini rejects.
- Starting without credentials prints one warning instead of two.
- When `install.sh` can't check the signature (no cosign, no `gh`), it installs but doesn't run the binary, and prints how to verify it. `gh attestation verify` is pinned to the release workflow and tag. A missing version gets a readable error. `ASC_MCP_REQUIRE_COSIGN` is replaced by `ASC_MCP_REQUIRE_SIGNATURE`.
- `golang.org/x/sys` 0.44.0 (GO-2026-5024).

### Fixed

- SIGTERM during a long call in HTTP mode now cancels the call, answers the client and exits 0, instead of an empty reply and exit 1.
- The Homebrew formula passes `brew audit --strict`.

## [0.8.0] - 2026-10-26

### Added

- `testflight_groups_upsert` creates or updates a beta group by name: public link on/off, its tester limit, feedback. Turning a public link on needs your approval; other changes to an existing group show a preview first.
- `release_review_status wait:true` waits for App Review to finish (up to 15 minutes, with progress) and hands back a resume hint when it's still in review.
- `release_status` warns about iPhone Duo screenshots, which Apple requires from April 2027. It only blocks after that date once the API can actually upload them.
- `release_status` checks custom product page keywords that are missing from the version's keywords (App Store Connect drops them on release).
- `release_status check_urls:true` loads the support, marketing and privacy URLs. Only public https addresses, at most 3 redirects, checked again on every hop.
- Every result says what day it is (`as_of`, in UTC and Apple's Pacific reporting day). `reviews_list since` accepts `today`, `yesterday`, `this_week`, `last_7_days`, `this_month` and `last_30_days`.
- `metadata_plan` previews against the live version when no version is being prepared, so "show me what would change" works with writes off. `metadata_apply` won't take that preview.

### Changed

- `insights_summary` without an app sums ratings across all apps instead of giving up on the ambiguity.
- `builds_list` falls back to the only platform that has builds when you didn't pick one.
- Error results carry `next` once.
- Tool descriptions are about 300 bytes shorter overall, which pays for the new tool.

### Fixed

- The text sanitiser now marks every invisible character class: variation selectors (except an emoji's own), all format characters, Hangul fillers, the blank braille cell and other default-ignorables. They could carry hidden text into approval forms.
- A huge `Retry-After` overflowed into an instant retry, and `0` or a past date retried with no wait. Both the API client and uploads now share one parser: over 30 s fails fast, zero or past uses normal backoff.
- A long lookalike word at the line limit no longer leaves a half-written warning marker.
- `reviews_list since:"yesterday"` could come back empty when today had more reviews than the limit.

## [0.7.0] - 2026-10-26

### Added

- MCP `2026-07-28`. Approvals use the new multi-round-trip input request: the call comes back asking for your yes, and the retry carries a signed, single-use approval bound to the tool, its arguments, the profile and the plan. Older clients still get the same form through `elicitation/create`.
- HTTP mode serves `2026-07-28` clients statelessly from the startup config. Older clients keep their per-session servers on the same `/mcp` endpoint.
- The server now has a title, a description, a website and an icon (inline, never fetched), and list results carry short private cache hints.

### Changed

- An unknown resource returns the standard `-32602` resource-not-found error.
- On `2026-07-28` HTTP, `toolsets` can list but not enable toolsets or writes; use `--toolsets` and `--allow-writes`.
- Reads that happen before an approval run twice: once to ask, once after you say yes. Apple state is checked again on the second run, and a changed state still refuses.

### Security

- Approval forms are safe to render as markdown or HTML. Warnings (`Irreversible`, notes, "looks like a private key or token") come first; every value sits in a code span or fence; `<`, `>` and `&` show as `⟨U+003C⟩` tokens. A fork's branch named `fix<!--` can no longer hide the rest of the form, and a reply with a markdown link shows the link, not just its text.
- Lookalike words in names and form lines are marked: `⟨mixed-script: Аpp⟩`, `⟨lookalike: 𝐀pp⟩`, `⟨combining: a̸b⟩`.
- `reviews_reply` also spots disguised contacts (`ivan at example dot com`, `evil[.]example`, fullwidth `＠`, Cyrillic lookalikes) and warns about ids or API key ids in a reply.
- The approval form warns when a value looks like a private key or JWT.
- A key file that others can write is refused; one they can read gets a warning in `doctor`.
- File tools refuse symlinks. A fastlane `release_notes.txt` linked to your `.p8` used to be read as release notes.
- Redaction also catches base64-encoded PEM and bare key bodies.
- HTTP mode refuses a configured token shorter than 32 characters or with fewer than 8 different characters, and answers CORS preflights only for origins you allowed.
- Beta group names no longer appear in a not-found hint.
- Release pipeline: no npm code runs in the job that signs and attests. `go.mod` pins `toolchain go1.26.10`, so `go install` doesn't build with a vulnerable Go. A stale `hamn` binary was removed from the repo, with a CI check against tracked binaries and a `.dockerignore`. `install.sh` requires curl, verifies cosign against the exact release identity, and says so when it could only check the sha256.

### Not done

- The tasks extension: go-sdk v1.8.0 has no support yet. Long waits still return a resume hint.

## [0.6.0] - 2026-10-26

### Security

- Approval forms can't be faked by stranger text any more. The form now lists what will happen and the targets first, then the stranger's text fenced on a single line, then the server's own `Change`, `Irreversible` and `Approve?` lines. A review can no longer draw its own "Irreversible: none" line.
- Bidi, zero-width, tag and control characters in third-party text are shown as visible `⟨U+202E⟩` tokens, in tool results and approval forms.
- `webhooks_upsert`: moving a webhook names the old and the new host, and a non-ASCII (lookalike) host is shown escaped with a warning.
- Email addresses in `users_invite`, TestFlight tools and the App Review contact must be plain ASCII addresses. Newlines, non-breaking spaces and bidi characters are refused before anything is sent.
- `xcc_build_start` needs `confirm:true`. Pull-request builds also need your approval, and PRs from forks are refused unless you pass `allow_fork_pr`.
- `screenshots_sync` always needs a `plan_id` from `screenshots_plan`.
- `reviews_reply` warns, and the approval form says so, when a reply contains a link or email address.
- Names, commit messages, branch names, PR titles, Xcode Cloud test failures and user names are now wrapped as `{"untrusted": ...}`, including on the raw `asc_api_get` / `asc_api_request` path. The digest checks TestFlight device fields like `testflight_feedback_list` does.
- PEM redaction can no longer join two strings and erase the reviews between them.
- The server instructions and every prompt that reads reviews, feedback or listing text say it is data, not instructions. `reply_to_reviews` waits for your yes before confirming.

### Changed

- `release_health_report`: only crash diagnostics of the new build count as production data. With no signatures on the current build the verdict is `unknown`. Ratings need at least 20 reviews in each window, and the window starts at the release (phased start, scheduled release, review submission or version creation, the last two marked approximate). TestFlight crashes count only from the window start. Analytics crashes and performance are informational. `reasons` are objects with `level`, `source`, `message` and `pausing`.
- `guard --pause-on-breach` pauses only on a breach from crash diagnostics; rating drops and TestFlight crashes never pause. With no live version it exits 5.
- App names in `apps_list` / `apps_get` and Xcode Cloud names come back as `{"untrusted": ...}`.
- `release_submit` approval shows the build number, which listing fields changed per locale against the live version, and the What's New text.
- In MCP Apps views, third-party text sits in its own labelled box, a button turns into Confirm only when the tool returned a preview, and buttons for tools without a preview say "(runs now)".

### Fixed

- `digest_summary` no longer loses reviews or feedback that Apple publishes late (72-hour overlap), reports builds that fail processing after they were first seen, and keeps both watermarks when two calls read different sources.
- Prompt completion offers app names again, not only bundle ids.

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

[Unreleased]: https://github.com/idoo/app-store-connect-mcp/compare/v0.17.0...HEAD
[0.17.0]: https://github.com/idoo/app-store-connect-mcp/compare/v0.16.0...v0.17.0
[0.16.0]: https://github.com/idoo/app-store-connect-mcp/compare/v0.15.0...v0.16.0
[0.15.0]: https://github.com/idoo/app-store-connect-mcp/compare/v0.14.0...v0.15.0
[0.14.0]: https://github.com/idoo/app-store-connect-mcp/compare/v0.13.0...v0.14.0
[0.13.0]: https://github.com/idoo/app-store-connect-mcp/compare/v0.12.0...v0.13.0
[0.12.0]: https://github.com/idoo/app-store-connect-mcp/compare/v0.11.0...v0.12.0
[0.11.0]: https://github.com/idoo/app-store-connect-mcp/compare/v0.10.0...v0.11.0
[0.10.0]: https://github.com/idoo/app-store-connect-mcp/compare/v0.9.0...v0.10.0
[0.9.0]: https://github.com/idoo/app-store-connect-mcp/compare/v0.8.0...v0.9.0
[0.8.0]: https://github.com/idoo/app-store-connect-mcp/compare/v0.7.0...v0.8.0
[0.7.0]: https://github.com/idoo/app-store-connect-mcp/compare/v0.6.0...v0.7.0
[0.6.0]: https://github.com/idoo/app-store-connect-mcp/compare/v0.5.0...v0.6.0
[0.5.0]: https://github.com/idoo/app-store-connect-mcp/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/idoo/app-store-connect-mcp/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/idoo/app-store-connect-mcp/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/idoo/app-store-connect-mcp/compare/v0.1.3...v0.2.0
[0.1.3]: https://github.com/idoo/app-store-connect-mcp/compare/v0.1.2...v0.1.3
[0.1.2]: https://github.com/idoo/app-store-connect-mcp/releases/tag/v0.1.2
