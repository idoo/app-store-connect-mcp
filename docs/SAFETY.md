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

**A human says yes.** Public, permanent and money-moving tools need you to accept an approval form in the client: `reviews_reply`, `release_submit`, `release_cancel`, `release_publish`, `builds_expire`, `testflight_testers_remove`, `pricing_apply`, `offers_create`, `offer_codes_generate`, `users_invite`, `users_remove`, `webhooks_upsert`, `webhooks_delete`, `signing_revoke`, and any `asc_api_request` call. Sometimes also:

- `release_phased` with `complete` or `remove`;
- `metadata_apply` when it changes promotional text on the live version (public at once, no review);
- `iap_upsert` / `subscriptions_upsert` when `availability` removes territories (sales stop there);
- `signing_devices_register` when a device is new (each one uses a yearly device slot);
- `iap_upsert` / `subscriptions_upsert` when they create a product (its id is burned for good), turn on family sharing or change a subscription's level;
- `signing_capabilities_set` when it disables a capability;
- `testflight_distribute` / `testflight_testers_add` when they touch an external group, submit for beta review, answer export compliance or overwrite review details;
- `xcc_build_start` on a pull request, because the PR's `ci_scripts` run with the workflow's secrets. PRs from forks are refused unless you pass `allow_fork_pr`.

The form starts with the profile (account), what will happen and the exact targets: a webhook move names the old and the new host, a lookalike non-ASCII host is shown escaped, and email addresses must be plain ASCII. Stranger-written text comes next, fenced, labelled and squeezed onto one line, so it can't draw fake lines. The server's own `Change`, `Irreversible` and `Approve?` lines always come after it. `Change` shows exactly what Apple will receive, never redacted or truncated; over 6000 bytes the form refuses instead. Clients that can't show forms get `human_approval_required`, unless the profile sets `allow_no_elicit`, or a pipeline sets both `ASC_MCP_CI=1` and `ASC_MCP_ALLOW_NO_ELICIT=1`. `--no-confirm` skips previews only with `ASC_MCP_CI=1`, and still not the human ones.

**Interactive views add no new power.** MCP Apps views are only offered to clients that ask for them. They are static HTML in the client's sandbox with no network access, and show stranger text as text in its own labelled box. A button in a view calls the same tool the model would. If the tool returns a preview, the button turns into Confirm and the approval form still appears; buttons for tools without a preview say "(runs now)".

**The guard only pauses.** `app-store-connect-mcp guard --pause-on-breach` can pause an active phased release and nothing else, and only with writes on. Only crash diagnostics can trigger it: a rating drop or TestFlight crashes, which strangers can cause, never pause a rollout.

**Stranger text is marked.** Reviews, tester data, feedback, crash logs, names, commit and branch text, free-text attributes, store listings and raw API bodies come back as `{"untrusted": ...}`, with bidi, zero-width and tag characters shown as visible `⟨U+202E⟩` tokens. The server instructions tell the model that's data, not orders. Treat this as a second layer, not the gate: in a 54-call test on Claude models the marking made no measurable difference, because the models already refused. Weaker models may not.

**Pins only narrow.** `.app-store-connect-mcp.toml` can turn writes off, never on. Outside `trusted_pins` it can only pick a read-only profile.

**Nothing deleted by omission.** Metadata, screenshot and tester tools only remove what's listed in `remove`. `screenshots_sync` always needs a `plan_id` from `screenshots_plan`, so a folder in a cloned repo can't reach your version unseen. A 409 "already exists" counts as done. Mutations are never retried except after a 429. A timeout on one returns `outcome_unknown` instead of trying again and burning a product id.

**Keys stay out of chat.** No tool takes or returns key material; only `app-store-connect-mcp auth` edits profiles. The config holds paths or keychain references and is refused if others can write it. JWTs, PEM blocks, `Authorization` headers and signed URL queries are redacted from results, errors and logs. JWTs only go to the API base URL. One-time offer codes go to `offer-codes/<batch_id>.csv` under the project root (git-ignored, mode 0600), not into the transcript.

**URLs and files are fenced.** Presigned URLs must be https on port 443 with no userinfo and public IPs, checked again at dial time, sent without `Authorization`, `Cookie`, `Host` or `Proxy-*`. Pagination cursors can't change host, path or query keys. File tools work through an `os.Root` on the project root, so `..` and symlinks don't get out. Results are capped at 60 KB, so a hostile payload can't flood the context.

**HTTP mode** needs a bearer token, checks `Origin` and binds to loopback unless you pass `--allow-remote` with TLS. Each session has its own write guard.

**Supply chain.** Pinned modules, SHA-pinned Actions, reproducible builds, SBOM, provenance attestations, govulncheck, CodeQL, gitleaks. `install.sh` checks the sha256, and with `gh` installed it runs `gh attestation verify` too.

## The review that says "ignore previous instructions"

1. `reviews_list` returns it as `{"untrusted": "..."}`.
2. With writes off, `reviews_reply` doesn't exist.
3. With writes on, the first call is a preview.
4. Confirming pops the approval form: what will be posted and where, then their review fenced on one line, then the server's own lines. A reply that contains a link or email gets an extra note asking you to check it's yours. Decline and nothing happens.

## What this doesn't fix

- The model can still be fooled. Your yes in the client is the real gate, and auto-approving everything or setting `allow_no_elicit` removes it.
- Plan/confirm catches accidents and stale state, not a model determined to call twice. That's why public actions also need you.
- The keychain protects the key at rest, not from other processes running as you.
- Read-only still reads. Sales numbers and tester emails go to whatever model your client uses.
- HTTP mode is for your own agents. There's no OAuth and no multi-tenant hosting.
- Apple's permissions are Apple's. Give the key the smallest role that does the job.

Found a gap? See [SECURITY.md](../SECURITY.md).
