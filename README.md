<p align="center"><img src="docs/assets/hero.jpg" alt="A goose drives an old boxy car past a WAITING FOR REVIEW sign. In the back seat a guy in a grey hoodie with a bitten apple for a head cheers, and a cartoon Viking laughs while filming it on his phone. Fuzzy dice hang from the mirror." width="100%"></p>

# app-store-connect-mcp

MCP server for App Store Connect. Read-only by default, and it asks before anything goes public. The binary is `asc-mcp`.

[![CI](https://github.com/idoo/app-store-connect-mcp/actions/workflows/ci.yml/badge.svg)](https://github.com/idoo/app-store-connect-mcp/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/idoo/app-store-connect-mcp)](https://github.com/idoo/app-store-connect-mcp/releases)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![MCP](https://img.shields.io/badge/MCP-server-black)](https://modelcontextprotocol.io)

## Install

Paste this into whatever AI is writing your app right now (we won't tell):

```text
Set up the app-store-connect-mcp server for me: https://github.com/idoo/app-store-connect-mcp/blob/main/docs/INSTALL.md
```

Or with Homebrew: `brew install idoo/tap/asc-mcp`, then follow [docs/INSTALL.md](docs/INSTALL.md).

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

It can't break anything until you let it. Anything public, money-moving or hard to undo for your users and team waits for your yes. Build uploads and Xcode Cloud runs don't ask. A 1-star review that says "ignore previous instructions" is still just a 1-star review. More in [docs/SAFETY.md](docs/SAFETY.md).

## What it does

Everything you'd click in App Store Connect: releases, TestFlight, listings, screenshots, reviews, sales, IAPs, signing, Xcode Cloud. For everything else there's raw API access. [Tool list](docs/TOOLS.md), [config](docs/CONFIG.md).

## License

[MIT](LICENSE) © Ivan Verevkin
