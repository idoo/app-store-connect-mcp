<p align="center"><img src="docs/assets/hero.jpg" alt="A goose drives an old boxy car past a WAITING FOR REVIEW sign. In the back seat a guy in a grey hoodie with a bitten apple for a head cheers, and a cartoon Viking laughs while filming it on his phone. Fuzzy dice hang from the mirror." width="100%"></p>

# app-store-connect-mcp

MCP server for App Store Connect. MCP is the plug-in format AI clients like Claude, Cursor and Codex use, so with this one your agent can read and manage your App Store Connect account. Read-only by default, and it asks before anything goes public. The binary is `app-store-connect-mcp`.

[![CI](https://github.com/idoo/app-store-connect-mcp/actions/workflows/ci.yml/badge.svg)](https://github.com/idoo/app-store-connect-mcp/actions/workflows/ci.yml)
[![CodeQL](https://github.com/idoo/app-store-connect-mcp/actions/workflows/codeql.yml/badge.svg)](https://github.com/idoo/app-store-connect-mcp/actions/workflows/codeql.yml)
[![Release](https://img.shields.io/github/v/release/idoo/app-store-connect-mcp)](https://github.com/idoo/app-store-connect-mcp/releases)
[![Go Reference](https://pkg.go.dev/badge/github.com/idoo/app-store-connect-mcp.svg)](https://pkg.go.dev/github.com/idoo/app-store-connect-mcp)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![MCP](https://img.shields.io/badge/MCP-server-black)](https://modelcontextprotocol.io)
[![Glama score](https://glama.ai/mcp/servers/idoo/app-store-connect-mcp/badges/score.svg)](https://glama.ai/mcp/servers/idoo/app-store-connect-mcp)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/idoo/app-store-connect-mcp/badge)](https://scorecard.dev/viewer/?uri=github.com/idoo/app-store-connect-mcp)
[![OpenSSF Best Practices](https://www.bestpractices.dev/projects/15998/badge)](https://www.bestpractices.dev/projects/15998)

## Install

Paste this into whatever AI is writing your app right now (we won't tell):

```text
Set up the app-store-connect-mcp server for me: https://github.com/idoo/app-store-connect-mcp/blob/main/docs/INSTALL.md
```

Or by hand, in three steps:

```sh
curl -fsSL https://github.com/idoo/app-store-connect-mcp/releases/latest/download/install.sh | sh   # or: brew install idoo/tap/app-store-connect-mcp
app-store-connect-mcp auth add                    # asks for your API key, tests it with Apple
app-store-connect-mcp config install claude-code  # or claude-desktop, cursor, codex, vscode, windsurf
```

Details in [docs/INSTALL.md](docs/INSTALL.md). Upgrading, key rotation and fixes for common errors: [docs/FAQ.md](docs/FAQ.md).

## Why you can trust it

You're handing it an App Store Connect key, so here's what you can check yourself instead of taking my word for it:

- **All of it is here.** MIT, no closed parts. Build it yourself with `go install github.com/idoo/app-store-connect-mcp/cmd/app-store-connect-mcp@latest`.
- **Read-only until you say otherwise.** Write tools aren't even listed until you turn writes on, and anything public or hard to undo shows a preview and waits for your yes. [docs/SAFETY.md](docs/SAFETY.md)
- **No telemetry.** It talks to `api.appstoreconnect.apple.com`, the presigned upload and report URLs Apple hands back (sent without your token), and the public iTunes Search API for `store_lookup`. After `web login` it also reads `appstoreconnect.apple.com` (App Analytics, Payments, the `web` toolset) and Apple's own sign-in pages. The JWT is signed on your machine and only ever goes to the API. [SECURITY.md](SECURITY.md#keys)
- **Your key stays where you put it.** It's read from a file, an env var or the OS keychain. The config holds only a path or keychain reference, is written `0600`, and is refused if anyone else can write to it. Apple ID cookies live in the Keychain. No MCP tool can add or change credentials.
- **Releases are signed and traceable to their source.** Every release is built by [release.yml](.github/workflows/release.yml) from a tag, published as an [immutable release](https://docs.github.com/en/code-security/supply-chain-security/understanding-your-software-supply-chain/immutable-releases), and ships an SPDX SBOM per archive, a cosign keyless signature on `checksums.txt`, and [build provenance attestations](https://github.com/idoo/app-store-connect-mcp/attestations). The macOS binaries are also signed with my Developer ID and notarized by Apple, so Gatekeeper opens them without a warning.
- **CI scans every push** with CodeQL and `govulncheck`, and a tag can't ship with a known reachable vulnerability.

Check a download with the GitHub CLI:

```sh
gh attestation verify app-store-connect-mcp_<version>_<os>_<arch>.tar.gz --repo idoo/app-store-connect-mcp
```

or with [cosign](https://docs.sigstore.dev/cosign/system_config/installation/):

```sh
cosign verify-blob --bundle checksums.txt.sigstore.json \
  --certificate-identity-regexp '^https://github.com/idoo/app-store-connect-mcp/\.github/workflows/release\.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com checksums.txt
shasum -a 256 -c checksums.txt --ignore-missing
```

On a Mac, `spctl -a -vv -t install app-store-connect-mcp` should say `source=Notarized Developer ID`.

The `.mcpb` and `tools.json` are listed in `checksums-extra.txt` with their own `checksums-extra.txt.sigstore.json`. Both checks above work on them the same way.

## Compared

| | **this one** | [forgeops](https://github.com/forgeopslabs/appstore-mcp) | [zelentsov](https://github.com/zelentsov-dev/asc-mcp) | [Joshua (archived)](https://github.com/JoshuaRileyDev/app-store-connect-mcp-server) | [`asc`](https://github.com/rorkai/App-Store-Connect-CLI) |
|---|:-:|:-:|:-:|:-:|:-:|
| MCP | + | + | + | + | − |
| Read-only default | + | − | − | − | − |
| Asks before publishing | + | − | ~ | − | ~ |
| Injection guard | + | − | − | − | − |
| Release preflight | + | − | ~ | − | + |
| Sales reports | + | − | + | + | + |
| One binary, any OS | + | + | − | − | + |

`~` partial

## Security

It can't break anything until you let it. Anything public, money-moving or hard to undo for your users and team waits for your yes. Build uploads and Xcode Cloud branch runs don't ask; pull request runs do. A 1-star review that says "ignore previous instructions" is still just a 1-star review. More in [docs/SAFETY.md](docs/SAFETY.md).

## What it does

Everything you'd click in App Store Connect: releases, TestFlight, listings, screenshots, reviews, sales, IAPs, signing, Xcode Cloud. For everything else there's raw API access. [Tool list](docs/TOOLS.md), [config](docs/CONFIG.md).

After launch it keeps watching, if you turn on the `insights` toolset: post-release health with a guard that pauses a phased rollout from cron, a "what's new since yesterday" digest, the analytics funnel, and keyword rank tracking. In Claude Desktop, claude.ai, ChatGPT and VS Code the release checklist, metadata diffs, screenshots, reviews and funnel show up as small interactive views.

A toolset is a group of tools you switch on or off, so your agent only sees what you use. The release, TestFlight, metadata, reviews, reports and screenshots ones are on out of the box. Add others with `--toolsets +insights,+signing` or `ASC_MCP_TOOLSETS=+insights`, or ask the agent to turn one on mid-chat. The list is in [docs/CONFIG.md](docs/CONFIG.md#toolsets).

### Stats without an Admin key or vendor number

Apple's Analytics Reports need an Admin key and a day or two before the first numbers, and sales need the vendor number, which the API doesn't expose. After `app-store-connect-mcp web login` neither matters: the vendor number is read from Payments once and remembered, and `analytics_funnel_report` and `insights_summary` read App Analytics the way the website does. `insights_summary` without an app lists every app's impressions, page views, downloads, sessions and crashes against the previous period.

### Creating apps and App Privacy

Apple's API can't create an app record or fill in App Privacy. The opt-in `web` toolset can, through your Apple ID instead of the API key. You sign in once:

```sh
app-store-connect-mcp web login
```

A browser window opens on Apple's own sign-in page. Type your password, approve 2FA or use a passkey, and the window closes by itself. It's a separate browser profile just for this: your default browser if it's Brave, Chrome, Edge or Arc, another one of those if it isn't, or a downloaded Chrome for Testing when you have none. When the session runs out later, the same profile renews it headless, so mostly you won't see a window again. An agent can open the window too, with the `web_login` tool, when a tool says `web_session_required`.

No screen? `web login --password` keeps the old terminal flow (Apple ID, password, 2FA code), though Apple often refuses it with error -20209. On an SSH box, use `web import` with the cookies from a browser's "Copy as cURL" or a `FASTLANE_SESSION`.

Then turn the toolset on (`--toolsets +web`) and ask for `apps_create`, `privacy_get`, `privacy_plan` and `privacy_apply`. The agent never sees your Apple ID, password or cookies: the cookies stay in your Keychain and the password isn't stored anywhere. Creating an app and publishing App Privacy each wait for your yes. Details in [docs/CONFIG.md](docs/CONFIG.md#toolsets).

## License

[MIT](LICENSE) © Ivan Verevkin
