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

## License

[MIT](LICENSE) © Ivan Verevkin
