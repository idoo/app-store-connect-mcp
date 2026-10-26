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
app-store-connect-mcp config snippet|install CLIENT [--profile NAME]
app-store-connect-mcp skills list|install [--client claude-code|codex|cursor|omp] [--dir DIR] [--force]
```

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
| `--http-session-timeout`, `--http-max-sessions` | Defaults `30m` and `64` |
| `--root DIR` | Where file tools may read and write |

## Environment

| Variable | Meaning |
|---|---|
| `ASC_KEY_ID`, `ASC_ISSUER_ID` | Key id; issuer id for team keys, empty for individual keys |
| `ASC_PRIVATE_KEY_PATH` or `ASC_PRIVATE_KEY` | The `.p8` path, or the key as PEM / base64 PEM |
| `ASC_KEY_KIND` | `team` or `individual`, if it can't be guessed |
| `ASC_VENDOR_NUMBER` | For sales and finance reports |
| `ASC_MCP_PROFILE`, `ASC_MCP_ALLOW_WRITES`, `ASC_MCP_TOOLSETS`, `ASC_MCP_DISCOVERY`, `ASC_MCP_ROOT` | Same as the flags |
| `ASC_MCP_MAX_RESPONSE_BYTES` | Result cap, default `60000` (4096 to 10 MiB) |
| `ASC_MCP_MAX_RETRIES` | Default `3` (0 to 10) |
| `ASC_MCP_CI` | Required for `--no-confirm` and `ASC_MCP_ALLOW_NO_ELICIT` |
| `ASC_MCP_ALLOW_NO_ELICIT` | Agent approves human gates; honoured only with `ASC_MCP_CI=1` (see [CI](#ci-github-actions)) |
| `ASC_MCP_HTTP_TOKEN`, `ASC_MCP_HTTP_SESSION_TIMEOUT`, `ASC_MCP_HTTP_MAX_SESSIONS` | HTTP mode |
| `ASC_MCP_HTTP_ALLOWED_ORIGINS` | Comma-separated `--allow-origin` values, only with `--allow-remote` |
| `ASC_MCP_BASE_URL` | Loopback-only API override, for tests |

fastlane's `APP_STORE_CONNECT_API_KEY_*` names work as fallbacks. `install.sh` reads `ASC_MCP_VERSION` and `ASC_MCP_INSTALL_DIR`.

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

Individual keys have no issuer id and get their user's permissions. Some endpoints only take team keys; `docs/tools.json` marks those tools `needs_team_key`. The vendor number lives under *Payments and Financial Reports*. Apple keeps exchange rates off the API, so `finance_report` gives per-currency totals and never guesses USD.

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

`core`, `release`, `testflight`, `metadata`, `reviews`, `reports` and `screenshots` are on by default. `uploads`, `monetization`, `signing`, `xcodecloud`, `webhooks`, `admin` and `store` are opt-in.

Expressions: `all`, `release,reviews` (just those), `+signing,-reports` (defaults with edits), `all,-admin`. `core` can't be removed. The `toolsets` tool changes them mid-session and the client gets `tools/list_changed`. Discovery mode starts with core only, for clients with a tool cap.

## HTTP mode

```sh
ASC_MCP_HTTP_TOKEN=$(openssl rand -hex 32) app-store-connect-mcp serve --http 127.0.0.1:8765
```

Every request needs `Authorization: Bearer <token>`. A token you set must be at least 32 characters with 8 different ones; without `ASC_MCP_HTTP_TOKEN` app-store-connect-mcp prints a generated one to stderr once (and refuses if stderr isn't a terminal). Non-loopback `Origin` headers are rejected, and CORS preflights get an answer only for origins you allowed. `/healthz` needs no token and says nothing. Clients on MCP `2025-11-25` or older get a session each, so toolsets and `enable_writes` don't leak between sessions; past the session cap you get `503` with `Retry-After`. Clients on `2026-07-28` are served statelessly from the startup config: pick toolsets and writes with flags or env, because `toolsets` can't change them per request there. The Docker image is `ghcr.io/idoo/app-store-connect-mcp`.

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
