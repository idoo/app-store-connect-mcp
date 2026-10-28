# Safety

app-store-connect-mcp holds a key that can publish apps, answer customers in public, change prices and fire your team. This page says who we assume is out to get you and what stops them.

## Who we worry about

- **Text from strangers.** Reviews, TestFlight feedback, tester and app names, commit messages and PR titles reach the model as written.
- **An eager model.** Wrong app, wrong version, or a "cleanup" that deletes six locales.
- **A sketchy repo.** A cloned `.app-store-connect-mcp.toml` pointing your agent at your production profile.
- **Bad URLs.** Upload, report and pagination links come from responses and could point anywhere.
- **Local neighbours.** Other processes, or a web page trying DNS rebinding against `127.0.0.1`.
- **The supply chain.** Dependencies, Actions, release artifacts.

## What stops them

**Read-only by default.** Write tools aren't even listed until you pass `--allow-writes`, set `ASC_MCP_ALLOW_WRITES=1` or `allow_writes` in a profile. Turning them on mid-session with `toolsets` `enable_writes` asks you first. The API client also refuses every non-GET that doesn't come from a confirmed write tool. Raw writes go through `asc_api_request`, which is hidden too. Raw reads use `asc_api_get`.

**Preview first.** A write call without confirmation only returns a preview. Some tools take `confirm:true` next, and only after that preview of the same arguments, so an agent can't skip it. Others hand out a `plan_id` tied to the args, profile, session and current state. It's single use, lasts 15 minutes and is refused if anything changed meanwhile. Every result reports `_meta.app-store-connect-mcp.approved_by`: `human`, `agent` or `none`.

**A human says yes.** Public, permanent and money-moving tools need you to accept an approval form in the client: `reviews_reply`, `release_review_reply` (a sent Resolution Center message to App Review can't be edited or deleted), `release_submit`, `release_cancel`, `release_publish`, `builds_expire`, `testflight_testers_remove`, `pricing_apply`, `offers_create`, `offer_codes_generate`, `users_invite`, `users_remove`, `webhooks_upsert`, `webhooks_delete`, `signing_revoke`, `apps_create` (an app record can't be deleted and its name stays reserved), `privacy_apply` (App Privacy answers are public once published), and any `asc_api_request` call. Sometimes also:

- `release_phased` with `complete` or `remove`;
- `metadata_apply` when it changes promotional text on the live version (public at once, no review);
- `library_apply` when it deletes placements or retires assets (`remove`, `archive`, or `mode:"replace"`). Placements go before their asset; an asset Apple has already reviewed is archived, not deleted;
- `iap_upsert` / `subscriptions_upsert` when `availability` removes territories (sales stop there);
- `signing_devices_register` when a device is new (each one uses a yearly device slot);
- `iap_upsert` / `subscriptions_upsert` when they create a product (its id is burned for good), turn on family sharing or change a subscription's level;
- `signing_capabilities_set` when it disables a capability;
- `testflight_distribute` / `testflight_testers_add` when they touch an external group, submit for beta review, answer export compliance or overwrite review details;
- `testflight_groups_upsert` when it turns a group's public link on, or creates a group with one (anyone with the link can install);
- `appinfo_update` when it publishes Accessibility Nutrition Labels on the public product page;
- `xcc_build_start` on a pull request, because the PR's `ci_scripts` run with the workflow's secrets. PRs from forks are refused unless you pass `allow_fork_pr`.

The form starts with what will happen and the profile (account), then the server's own warnings: `Irreversible`, notes, and "looks like a private key or token" if a value does. Then the exact targets: a webhook move names the old and the new host, a lookalike non-ASCII host is shown escaped, and email addresses must be plain ASCII. Stranger-written text comes next, fenced, labelled and squeezed onto one line, so it can't draw fake lines, then `Change`, exactly what Apple will receive, never redacted or truncated (over 6000 bytes the form refuses instead), and `Approve?` last. Every value sits in a code span or fence and `<`, `>`, `&` show as `⟨U+003C⟩` tokens, so a client that renders markdown or HTML can't turn a branch name into a hidden comment or a reply into a link that shows only its text. Words that mix scripts or use lookalike letters are marked, like `⟨mixed-script: Аpp⟩`. On MCP `2026-07-28` your yes comes back as a signed, single-use approval tied to the tool, its arguments, the profile and the plan, so it can't be replayed or moved to another call. Clients that can't show forms get `human_approval_required`, unless the profile sets `allow_no_elicit`, or a pipeline sets both `ASC_MCP_CI=1` and `ASC_MCP_ALLOW_NO_ELICIT=1`. `--no-confirm` skips previews only with `ASC_MCP_CI=1`, and still not the human ones.

**Interactive views add no new power.** MCP Apps views are only offered to clients that ask for them. They are static HTML in the client's sandbox with no network access, and show stranger text as text in its own labelled box. A button in a view calls the same tool the model would. If the tool returns a preview, the button turns into Confirm and the approval form still appears; buttons for tools without a preview say "(runs now)".

**The guard only pauses.** `app-store-connect-mcp guard --pause-on-breach` can pause an active phased release and nothing else, and only with writes on. Only crash diagnostics can trigger it: a rating drop or TestFlight crashes, which strangers can cause, never pause a rollout.

**Stranger text is marked.** Reviews, tester data, feedback, crash logs, names, commit and branch text, free-text attributes, store listings and raw API bodies come back as `{"untrusted": ...}`, with bidi, zero-width and tag characters shown as visible `⟨U+202E⟩` tokens. The server instructions tell the model that's data, not orders. Treat this as a second layer, not the gate: in a 54-call test on Claude models the marking made no measurable difference, because the models already refused. Weaker models may not.

**Pins only narrow.** `.app-store-connect-mcp.toml` can turn writes off, never on. Outside `trusted_pins` it can only pick a read-only profile.

**Nothing deleted by omission.** Metadata, screenshot, library and tester tools only remove what's listed in `remove` (or, for `library_apply`, what `mode:"replace"` lists in its plan). `screenshots_sync` and `library_apply` always need a `plan_id` from their plan tool, so a folder in a cloned repo can't reach your version unseen. A 409 "already exists" counts as done. Mutations are never retried except after a 429. A timeout on one returns `outcome_unknown` instead of trying again and burning a product id.

**Keys stay out of chat.** No tool takes or returns key material; only `app-store-connect-mcp auth` edits profiles. The config holds paths or keychain references and is refused if others can write it; so is a `.p8` key file, and `doctor` warns if others can read it. JWTs, PEM blocks (also base64-encoded or bare key bodies), `Authorization` headers and signed URL queries are redacted from results, errors and logs. JWTs only go to the API base URL. One-time offer codes go to `offer-codes/<batch_id>.csv` under the project root (git-ignored, mode 0600), not into the transcript.

**Apple ID stays with you.** The `web` toolset uses your Apple ID web session, which can do anything you can do in App Store Connect. Only you can start one: `app-store-connect-mcp web login` (or the agent's `web_login` tool) opens a browser window where you sign in on Apple's own page, and `web login --password` or `web import` work in a terminal. No MCP tool takes or returns an Apple ID, password, 2FA code or cookie, and none can switch teams; `web_login` only opens the window and reports which account signed in. It opens one window at a time and waits a minute after a cancelled or failed sign-in, so an agent can't bury you in windows. The password and 2FA code are used once and never stored; the session cookies live in the OS keychain (or a 0600 file) and are redacted like keys. Web tools go through the same client as the API: writes off by default, preview first, a human approval for `apps_create`, `privacy_apply` and `release_review_reply`, no replayed mutations. `release_review_reply` sends only the text you pass (never copied from the reviewer's message), refuses unless App Review is waiting for a reply, and its approval form shows the exact text and the thread. `privacy_plan` takes the complete declaration, because Apple's form is all-or-nothing, so its preview and the approval form list every answer it removes. `web logout` deletes the session.

**The sign-in browser is its own.** The window runs in a dedicated profile under the state dir, never your everyday one. We don't read your browser's cookies, saved passwords or its Keychain entry, and the dedicated profile has its password manager turned off. Your password goes from you to Apple's page and nowhere else. The browser's debugging port, which is how the session cookies are picked up, is open only while that window is, and only on localhost. `web logout` deletes the profile too.

**URLs and files are fenced.** Presigned URLs must be https on port 443 with no userinfo and public IPs, checked again at dial time, sent without `Authorization`, `Cookie`, `Host` or `Proxy-*`. Pagination cursors can't change host, path or query keys. File tools work through an `os.Root` on the project root and refuse symlinks, so `..` doesn't get out and a fastlane file linked to your key isn't read. Results are capped at about 60 KB by default (`ASC_MCP_MAX_RESPONSE_BYTES`), so a hostile payload can't flood the context.

**HTTP mode** needs a bearer token of at least 32 characters, checks `Origin`, answers CORS preflights only for origins you allowed, and binds to loopback unless you pass `--allow-remote` with TLS. Older clients get a session each with its own write guard; `2026-07-28` clients are stateless and get exactly the startup config.

**Supply chain.** Pinned modules and Go toolchain, SHA-pinned Actions, no npm in the signing job, reproducible builds, SBOM, provenance attestations, govulncheck, CodeQL, gitleaks, and a CI check that no binary is committed. Releases are immutable once published (the workflow fails if one isn't), and `install.sh`, the `.mcpb` and `tools.json` are covered by cosign-signed checksums. `install.sh` checks the sha256, verifies cosign against the exact release identity when cosign is installed, and runs `gh attestation verify` pinned to the release workflow and tag when `gh` is. If neither can check the release, it installs but doesn't run the binary and tells you how to verify it; `ASC_MCP_REQUIRE_SIGNATURE=1` makes it refuse instead.

## The review that says "ignore previous instructions"

1. `reviews_list` returns it as `{"untrusted": "..."}`.
2. With writes off, `reviews_reply` doesn't exist.
3. With writes on, the first call is a preview.
4. Confirming pops the approval form: warnings first, then what will be posted and where, their review fenced on one line, and your reply in its own fence. A reply that contains a link, an email (even spelled `at`/`dot`) or an id gets an extra note asking you to check it. Decline and nothing happens.

## What this doesn't fix

- The model can still be fooled. Your yes in the client is the real gate, and auto-approving everything or setting `allow_no_elicit` removes it.
- Plan/confirm catches accidents and stale state, not a model determined to call twice. That's why public actions also need you.
- The keychain protects the key at rest, not from other processes running as you.
- Read-only still reads. Sales numbers and tester emails go to whatever model your client uses.
- HTTP mode is for your own agents. There's no OAuth and no multi-tenant hosting.
- Apple's permissions are Apple's. Give the key the smallest role that does the job.

Found a gap? See [SECURITY.md](../SECURITY.md).
