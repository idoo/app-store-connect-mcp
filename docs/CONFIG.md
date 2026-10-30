# Configuration

Credentials come from env vars or named profiles. Only a human at a terminal manages them with `app-store-connect-mcp auth`. No MCP tool can read or change them, however nicely it asks.

## Commands

`app-store-connect-mcp --help` lists them all, and every subcommand takes `--help`. The ones worth knowing:

```text
app-store-connect-mcp [serve]                      MCP over stdio (default)
app-store-connect-mcp auth add|import              add a key; asks team or individual (--kind), tests it first (--no-verify skips)
app-store-connect-mcp auth list|test|remove
app-store-connect-mcp auth set [NAME] key=value    vendor_number, default_app, allow_writes, issuer_id, key_kind
app-store-connect-mcp doctor [--profile NAME]
app-store-connect-mcp config snippet|install CLIENT [--profile NAME] [--allow-writes]
app-store-connect-mcp skills list|install [--client claude-code|codex|cursor|omp] [--dir DIR] [--force]
app-store-connect-mcp guard --app APP [thresholds]  release health for cron; exit 0 ok, 3 watch, 4 breach, 5 unknown
app-store-connect-mcp aso track|report --app APP    keyword ranks for cron (public search, no key needed)
app-store-connect-mcp web login|import|status|team|logout   Apple ID web session for the web toolset
```

`guard` thresholds, defaults in brackets: `--signature-watch-pct` (25) and `--signature-breach-pct` (100) for crash-signature weight growth in percent, `--crash-watch` (3) and `--crash-breach` (10) TestFlight crash reports, `--rating-drop-watch` (0.3) and `--rating-drop-breach` (0.8) average-rating drop, `--regressions-breach` (2) perf regressions. `guard --help` prints the same list. Only a diagnostics breach can pause, with `--pause-on-breach` and writes on.

`auth set` without a name targets `env` when `ASC_KEY_ID` is set (stored under `[env]`), otherwise the default profile. Setting `issuer_id=` or `key_kind=individual` turns a profile into an individual key.

`config install` merges app-store-connect-mcp into the client's file and keeps a backup: `~/.cursor/mcp.json`, `~/.codeium/windsurf/mcp_config.json`, `~/.codex/config.toml`, or `Claude/claude_desktop_config.json` and `Code/User/mcp.json` under your app-support dir. For `claude-code` it runs `claude mcp add`, or prints the command if `claude` isn't on your PATH. It writes the `app-store-connect-mcp` on your PATH, so `brew upgrade` doesn't break anything.

## `app-store-connect-mcp serve` flags

| Flag | Meaning |
|---|---|
| `--profile NAME` | Profile to use |
| `--allow-writes` | Turn on write tools |
| `--toolsets EXPR` | See [Toolsets](#toolsets) |
| `--discovery` | Start with core tools only |
| `--no-confirm` | Skip previews, only with `ASC_MCP_CI=1`. Human-approval tools still ask |
| `--http ADDR` | Streamable HTTP instead of stdio |
| `--allow-remote`, `--tls-cert`, `--tls-key` | Non-loopback HTTP, TLS required |
| `--allow-origin ORIGIN` | Extra allowed browser `Origin` (exact `scheme://host[:port]`, repeatable), only with `--allow-remote` |
| `--http-session-timeout`, `--http-max-sessions`, `--http-max-conns` | Defaults `30m`, `64` and `256` |
| `--root DIR` | Where file tools may read and write |

## Environment

| Variable | Meaning |
|---|---|
| `ASC_KEY_ID`, `ASC_ISSUER_ID` | Key id; issuer id for team keys, empty for individual keys |
| `ASC_PRIVATE_KEY_PATH` or `ASC_PRIVATE_KEY` | The `.p8` path, or the key as PEM / base64 PEM |
| `ASC_KEY_KIND` | `team` or `individual`, if it can't be guessed |
| `ASC_VENDOR_NUMBER` | For sales and finance reports; read from Payments via the web session when unset |
| `ASC_MCP_PROFILE`, `ASC_MCP_ALLOW_WRITES`, `ASC_MCP_TOOLSETS`, `ASC_MCP_DISCOVERY`, `ASC_MCP_ROOT` | Same as the flags |
| `ASC_MCP_MAX_RESPONSE_BYTES` | Result cap, default `60000` (4096 to 10 MiB) |
| `ASC_MCP_MAX_RETRIES` | Default `3` (0 to 10) |
| `ASC_MCP_CI` | Required for `--no-confirm` and `ASC_MCP_ALLOW_NO_ELICIT` |
| `ASC_MCP_ALLOW_NO_ELICIT` | Agent approves human gates; honoured only with `ASC_MCP_CI=1` (see [CI](#ci-github-actions)) |
| `ASC_MCP_HTTP_TOKEN`, `ASC_MCP_HTTP_SESSION_TIMEOUT`, `ASC_MCP_HTTP_MAX_SESSIONS`, `ASC_MCP_HTTP_MAX_CONNS` | HTTP mode |
| `ASC_MCP_HTTP_ALLOWED_ORIGINS` | Comma-separated `--allow-origin` values, only with `--allow-remote` |
| `ASC_MCP_WEB_STORE` | `file` keeps the web session in a 0600 file under the state dir instead of the OS keychain (headless or CI) |
| `ASC_MCP_BROWSER` | Browser for the `web login` window: a path to a Chromium-based browser, or `bundled` for the downloaded Chrome for Testing. Unset picks your default browser if it's Chromium-based, then any installed Brave, Chrome, Edge or Arc |
| `ASC_MCP_BASE_URL` | Loopback-only API override, for tests |

fastlane's `APP_STORE_CONNECT_API_KEY_*` names work as fallbacks. `install.sh` reads `ASC_MCP_VERSION`, `ASC_MCP_INSTALL_DIR` and `ASC_MCP_REQUIRE_SIGNATURE=1` (refuse to install unless cosign or `gh attestation verify` checks the release).

Profile picked: `--profile` > `ASC_MCP_PROFILE` > `ASC_*` env > `.app-store-connect-mcp.toml` > `default_profile` > the only profile. Writes: `--allow-writes` > `ASC_MCP_ALLOW_WRITES` > profile `allow_writes`, and a pin can still switch them off.

## Config file

`~/.config/app-store-connect-mcp/config.toml` (or `$XDG_CONFIG_HOME`, `%AppData%` on Windows). app-store-connect-mcp refuses it if group or others can write it, and complains if they can read it. Unknown keys are errors, so typos don't vanish quietly.

```toml
default_profile = "personal"
trusted_pins = ["/Users/me/code/hjul"]   # dirs whose .app-store-connect-mcp.toml may pick any profile
allow_profile_switch = false              # adds a per-call `profile` argument

[profiles.personal]
key_id = "ABC123DEFG"
issuer_id = "69a6de7f-0000-0000-0000-000000000000"
key_path = "~/.appstoreconnect/private_keys/AuthKey_ABC123DEFG.p8"
vendor_number = "81234567"
default_app = "se.example.hjul"          # app id or bundle id
allow_writes = false

[profiles.client]
key_id = "XYZ987"
key_kind = "individual"                  # no issuer_id
key_source = "keychain"                  # instead of key_path

[env]                                    # extras for ASC_* env users
vendor_number = "81234567"
```

`allow_no_elicit = true` on a profile lets human-approval tools run on the agent's say-so when the client can't show an approval form. Results then say `approved_by: "agent"`. That removes the human from the loop, so only use it with a client that asks you itself.

Individual keys have no issuer id and get their user's permissions. Some endpoints only take team keys; `docs/tools.json` marks those tools `needs_team_key`. The vendor number lives under *Payments and Financial Reports*; with a web session (`web login`) and no `vendor_number` set, it's read from there once and remembered in the state dir. Apple keeps exchange rates off the API, so `finance_report` gives per-currency totals and never guesses USD.

Analytics Reports (`analytics_setup`) need an Admin key. Without one, or before Apple has produced the first reports, `analytics_funnel_report` and `insights_summary` read the same numbers from the App Analytics dashboard through the web session; results say `data_source: "web_dashboard"`.

## Per-project pin

app-store-connect-mcp looks for `.app-store-connect-mcp.toml` from the working directory up to the git root:

```toml
profile = "personal"
app = "se.example.hjul"
allow_writes = false    # only false does anything
```

A pin can only narrow things. `allow_writes = true` is ignored with a warning. Outside `trusted_pins`, a pin may only pick a read-only profile, so a cloned repo can't aim your agent at production.

Earlier releases read `.asc-mcp.toml` and `.hamn.toml`. If one of those is in a walked directory without an `.app-store-connect-mcp.toml` next to it, and it holds pin keys, the server and `doctor` refuse to start and print the `mv` that renames it. A read-only pin is never silently dropped. A file with those names that holds another server's settings is ignored. Likewise, when `~/.config/app-store-connect-mcp/config.toml` is missing but `~/.config/asc-mcp/config.toml` or `~/.config/hamn/config.toml` has profiles, startup and `doctor` warn and print the `mv`.

## Toolsets

`core`, `release`, `testflight`, `metadata`, `reviews`, `reports` and `screenshots` are on by default. `uploads`, `monetization`, `signing`, `xcodecloud`, `webhooks`, `admin`, `store`, `insights`, `library` (Asset Library writes) and `web` are opt-in.

`web` holds what Apple's API can't do: `apps_create` (new app records) and `privacy_get`, `privacy_plan`, `privacy_apply` (App Privacy). They use your Apple ID web session instead of the API key, so a human signs in first. `app-store-connect-mcp web login` opens a browser window on Apple's sign-in page; agents can open the same window with the `web_login` tool. `web login --password` is the terminal flow (Apple ID, password, 2FA code) for machines without a screen, and `web import` takes cookies from a browser's "Copy as cURL" or `FASTLANE_SESSION`. One session per profile. It's kept in the OS keychain (service `app-store-connect-mcp-web`), or in `<state dir>/web/<profile>.json` (mode 0600) when the keychain can't hold it or `ASC_MCP_WEB_STORE=file`. The password is never stored. Apple ends web sessions after about a month, sometimes sooner. The browser window uses its own profile in `<state dir>/web/browser/<profile>`, and when the session expires that profile renews it headless, without a window, as long as Apple still trusts it; otherwise tools answer `web_session_required` until you sign in again. `web logout` deletes the session and the browser profile. `release_status` reads the App Privacy publish state through the session when there is one.

Expressions: `all`, `release,reviews` (just those), `+signing,-reports` (defaults with edits), `all,-admin`. `core` can't be removed. The `toolsets` tool changes them mid-session and the client gets `tools/list_changed`. Discovery mode starts with core only, for clients with a tool cap.

## HTTP mode

```sh
ASC_MCP_HTTP_TOKEN=$(openssl rand -hex 32) app-store-connect-mcp serve --http 127.0.0.1:8765
```

Every request needs `Authorization: Bearer <token>`. A token you set must be at least 32 characters with 8 different ones; without `ASC_MCP_HTTP_TOKEN` app-store-connect-mcp prints a generated one to stderr once (and refuses if stderr isn't a terminal). Non-loopback `Origin` headers are rejected, and CORS preflights get an answer only for origins you allowed. `/healthz` needs no token and says nothing. Clients on MCP `2025-11-25` or older get a session each, so toolsets and `enable_writes` don't leak between sessions. At the session cap a session idle for 10 minutes makes room; otherwise you get `503` with a `Retry-After` that says when one will. Clients on `2026-07-28` are served statelessly from the startup config: pick toolsets and writes with flags or env, because `toolsets` can't change them per request there. JSON-RPC batches work only for clients on `2025-03-26` or older, at most 100 per batch. Requests must arrive within 60 s, idle connections close after 2 minutes, and on SIGTERM in-flight calls are cancelled and answered before the server exits. The Docker image is `ghcr.io/idoo/app-store-connect-mcp`.

Tool schemas use plain JSON Schema with a few free-form objects (`listing`, `query`, `overrides`), so clients that force OpenAI's strict function-calling mode can't use those tools. Numbers and booleans sent as strings (`"3"`, `"true"`) are accepted.

## Files and state

File tools work inside one root: `--root`, `ASC_MCP_ROOT`, the client's first `file://` MCP root, then the git root. An automatic pick of `/` or your home dir is refused. `doctor` shows it as `file_root`.

app-store-connect-mcp speaks MCP up to `2026-07-28`. Approvals use the multi-round-trip input request there; older clients get the same form through `elicitation/create`.

Review and feedback cursors and upload resume files live in `~/.local/state/app-store-connect-mcp` (or `$XDG_STATE_HOME`, `%LocalAppData%` on Windows). Plans live in memory only.

If the state directory can't be written, the server still starts: `mark_seen`, upload resume and digest and rank history fail with an "unavailable" error, everything else works, and `doctor` prints a `state dir … not writable` line.

## CI (GitHub Actions)

Runners are ephemeral, so use the `env` profile:

```yaml
env:
  ASC_KEY_ID: ${{ secrets.ASC_KEY_ID }}
  ASC_ISSUER_ID: ${{ secrets.ASC_ISSUER_ID }}
  ASC_PRIVATE_KEY: ${{ secrets.ASC_PRIVATE_KEY }}
  ASC_VENDOR_NUMBER: ${{ secrets.ASC_VENDOR_NUMBER }}
  ASC_MCP_CI: "1"
  ASC_MCP_ALLOW_NO_ELICIT: "1"   # only if the agent may approve human gates itself
steps:
  - run: app-store-connect-mcp doctor --json
```

- An empty `ASC_PRIVATE_KEY` counts as unset, usually because the secret isn't available to the job (forks, environments). `doctor` fails its profile line with "set but empty (missing CI secret?)".
- `ASC_MCP_ALLOW_NO_ELICIT=1` lets human-approval tools run without an approval form, for any profile, but only together with `ASC_MCP_CI=1`. Otherwise it is ignored and `doctor` says so. Results still say `approved_by: "agent"`, and `doctor` prints `human gates: agent-approved (CI)`. A pin can't turn it on.
- `--no-confirm` likewise needs `ASC_MCP_CI=1`.
- `doctor --json` prints the same object as the `doctor` tool. The exit status is the same as in text mode: non-zero when a check fails.
