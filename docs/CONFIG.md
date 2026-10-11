# Configuration

hamn reads credentials from the environment or from named profiles, then applies per-project pins and command-line flags. Credentials are only ever managed by a human with `hamn auth`; no MCP tool can read or change them.

## Commands

```text
hamn [serve]                  run the MCP server on stdio (default)
hamn auth add                 interactive: add a profile
hamn auth import              import fastlane / env / AuthKey_*.p8 credentials
hamn auth list                list profiles
hamn auth test [PROFILE]      sign a JWT and call GET /v1/apps?limit=1
hamn auth remove NAME         remove a profile
hamn auth set NAME key=value  set vendor_number, default_app, allow_writes, issuer_id, key_kind
hamn doctor [--profile NAME]  check the key, JWT, clock skew, Apple reachability, rate limit
hamn config snippet CLIENT [--profile NAME]   print a client config
hamn config install CLIENT [--profile NAME]   merge hamn into the client's config file
hamn skills list
hamn skills install [--client claude-code|codex|cursor|omp] [--dir DIR] [--force]
hamn version
```

`CLIENT` is one of `claude-code`, `claude-desktop`, `cursor`, `codex`, `vscode`, `windsurf`. `config install` writes:

| Client | File |
|---|---|
| cursor | `~/.cursor/mcp.json` |
| windsurf | `~/.codeium/windsurf/mcp_config.json` |
| codex | `~/.codex/config.toml` |
| claude-desktop | `<app support>/Claude/claude_desktop_config.json` |
| vscode | `<app support>/Code/User/mcp.json` |

For `claude-code` it prints the `claude mcp add` command to run. `<app support>` is `~/Library/Application Support` on macOS, `%AppData%` on Windows and `~/.config` elsewhere.

## `hamn serve` flags

| Flag | Meaning |
|---|---|
| `--profile NAME` | Use this profile (overrides `HAMN_PROFILE` and `.hamn.toml`) |
| `--allow-writes[=false]` | Enable (or force off) write tools for this session |
| `--toolsets EXPR` | Toolset expression, see below |
| `--discovery[=false]` | List only core tools; enable others at runtime |
| `--no-confirm` | Skip previews; honored only with `HAMN_CI=1`; human-approval tools still ask |
| `--http ADDR` | Serve Streamable HTTP on `ADDR` (e.g. `127.0.0.1:8765`) instead of stdio |
| `--allow-remote` | Allow a non-loopback `--http` address; requires TLS |
| `--tls-cert FILE`, `--tls-key FILE` | TLS certificate and key PEM for `--http` |

## Environment variables

### Credentials (single account)

| Variable | Meaning |
|---|---|
| `ASC_KEY_ID` | API key id |
| `ASC_ISSUER_ID` | Issuer id (team keys). Leave empty for an individual key |
| `ASC_KEY_KIND` | `team` or `individual`, when it can't be inferred |
| `ASC_PRIVATE_KEY_PATH` | Path to the `.p8` file |
| `ASC_PRIVATE_KEY` | The key itself, PEM or base64 PEM (prefer the path) |
| `ASC_VENDOR_NUMBER` | Vendor number, needed for sales and finance reports |

The fastlane names `APP_STORE_CONNECT_API_KEY_KEY_ID`, `APP_STORE_CONNECT_API_KEY_ISSUER_ID`, `APP_STORE_CONNECT_API_KEY_KEY_FILEPATH` and `APP_STORE_CONNECT_API_KEY_KEY` are accepted as fallbacks.

### Behavior

| Variable | Default | Meaning |
|---|---|---|
| `HAMN_PROFILE` | | Profile name |
| `HAMN_ALLOW_WRITES` | `false` | Enable write tools (`1`/`true`) |
| `HAMN_TOOLSETS` | defaults | Toolset expression |
| `HAMN_DISCOVERY` | `false` | Discovery mode |
| `HAMN_MAX_RESPONSE_BYTES` | `60000` | Result byte cap (4096 to 10 MiB) |
| `HAMN_MAX_RETRIES` | `3` | Retries for retryable requests (0 to 10) |
| `HAMN_BASE_URL` | Apple | API base override, loopback only (tests) |
| `HAMN_CI` | `false` | Marks a CI run; required for `--no-confirm` |
| `HAMN_HTTP_TOKEN` | generated | Bearer token for `--http` |

`install.sh` reads `HAMN_VERSION` (default: latest) and `HAMN_INSTALL_DIR` (default: `~/.local/bin`).

## Precedence

Profile selection: `--profile` > `HAMN_PROFILE` > `ASC_*` environment credentials > `.hamn.toml` pin > `default_profile` > the only profile, if there is exactly one.

Writes: `--allow-writes` > `HAMN_ALLOW_WRITES` > the profile's `allow_writes`, and then a pin may turn them off.

## Config file

`$XDG_CONFIG_HOME/hamn/config.toml`, else `~/.config/hamn/config.toml`, or `%AppData%\hamn\config.toml` on Windows. hamn refuses to load it if it is writable by group or others, and warns if others can read it. Unknown keys are errors, so a typo never silently drops a setting.

```toml
default_profile = "personal"
trusted_pins = ["/Users/me/code/hjul"]   # absolute dirs whose .hamn.toml may pick any profile
allow_profile_switch = false              # expose a per-call `profile` argument on tools

[profiles.personal]
key_id = "ABC123DEFG"
issuer_id = "69a6de7f-0000-0000-0000-000000000000"
key_path = "/Users/me/.appstoreconnect/private_keys/AuthKey_ABC123DEFG.p8"
vendor_number = "81234567"
default_app = "se.example.hjul"
allow_writes = false

[profiles.client]
key_id = "XYZ987"
key_kind = "individual"      # no issuer_id for individual keys
key_source = "keychain"      # instead of key_path; `hamn auth add` can store it there
```

| Key | Meaning |
|---|---|
| `key_id` | Required |
| `issuer_id` | Team keys only |
| `key_kind` | `team` or `individual` |
| `key_path` / `key_source = "keychain"` | Where the `.p8` is; exactly one |
| `vendor_number` | For sales and finance reports |
| `default_app` | App id or bundle id used when a tool gets no `app` |
| `allow_writes` | Enable writes for this profile |
| `allow_no_elicit` | Let human-approval tools proceed with `confirm:true` when the client can't show an approval dialog. Only for clients you trust to prompt you themselves |

### Team keys and individual keys

Team keys have an issuer id and a role chosen by the Account Holder or an Admin. Individual keys belong to one user, have no issuer id and inherit that user's permissions; hamn signs them with `sub: "user"`. Some endpoints only work with team keys, and `docs/tools.json` marks those tools with `needs_team_key`.

### Vendor number

Sales and finance reports need it. Find it in App Store Connect under *Payments and Financial Reports*. Without it, the report tools explain what is missing.

## Per-project pin: `.hamn.toml`

hamn walks up from the working directory to the git root looking for `.hamn.toml`:

```toml
profile = "personal"
app = "se.example.hjul"    # app id or bundle id
allow_writes = false       # optional; only `false` has an effect
```

A pin never holds key material and can only narrow permissions:

- `allow_writes = true` is ignored with a warning. `allow_writes = false` also blocks enabling writes at runtime.
- A pin in a directory not listed in `trusted_pins` may only select a profile that has writes off, and may only set `app` when the effective profile is read-only. Otherwise it is ignored with a warning. This stops a cloned repo from pointing your agent at a write-enabled production profile.

## Toolsets

Toolsets: `core`, `release`, `testflight`, `metadata`, `reviews`, `reports` (default on), `screenshots`, `uploads`, `monetization`, `signing`, `xcodecloud`, `webhooks`, `admin`, `store`.

An expression is a comma- or space-separated list:

| Expression | Result |
|---|---|
| empty | the defaults |
| `all` | everything |
| `default` | the defaults |
| `release,reviews` | exactly those (plus core) |
| `+signing,-reports` | defaults plus signing, minus reports |
| `all,-admin` | everything except admin |

`core` is always on and cannot be removed. At runtime the `toolsets` tool lists, enables and disables toolsets for the session and the client gets `tools/list_changed`.

**Discovery mode** (`--discovery`, `HAMN_DISCOVERY=1`) lists only core tools at start, for clients with a tool cap.

## HTTP mode

```sh
HAMN_HTTP_TOKEN=$(openssl rand -hex 32) hamn serve --http 127.0.0.1:8765
```

- Every request needs `Authorization: Bearer <token>`. Without `HAMN_HTTP_TOKEN`, hamn generates a token and prints it once to stderr (it refuses when stderr is not a terminal).
- Non-loopback `Origin` headers are rejected, which blocks DNS rebinding from browser pages.
- Binding to a non-loopback address needs `--allow-remote` and `--tls-cert`/`--tls-key`.
- `/healthz` is unauthenticated and returns no data.
- Each HTTP session gets its own server, so enabled toolsets and write mode are per session.

A Docker image (`ghcr.io/idoo/hamn`) is published with each release for this mode.

## State

Review and feedback cursors and upload resume files live in `$XDG_STATE_HOME/hamn`, else `~/.local/state/hamn` (`%LocalAppData%\hamn` on Windows), created with mode 0700. Plans live in memory only.
