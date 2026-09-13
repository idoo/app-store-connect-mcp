<p align="center"><img src="docs/assets/hero.jpg" alt="A goose drives an old boxy car past a WAITING FOR REVIEW sign. In the back seat a guy in a grey hoodie with a bitten apple for a head cheers, and a cartoon Viking laughs while filming it on his phone. Fuzzy dice hang from the mirror." width="100%"></p>

# Hamn

MCP server for App Store Connect. Read-only by default, and it asks before anything goes public.

[![CI](https://github.com/idoo/hamn/actions/workflows/ci.yml/badge.svg)](https://github.com/idoo/hamn/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/idoo/hamn)](https://github.com/idoo/hamn/releases)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![MCP](https://img.shields.io/badge/MCP-server-black)](https://modelcontextprotocol.io)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/idoo/hamn/badge)](https://scorecard.dev/viewer/?uri=github.com/idoo/hamn)

## Install

Paste this into Claude Code, Cursor or Codex:

```text
Set up the Hamn MCP server for me: https://github.com/idoo/hamn/blob/main/docs/INSTALL.md
```

Or with Homebrew: `brew install idoo/tap/hamn`, then follow [docs/INSTALL.md](docs/INSTALL.md).

## Compared

| | Hamn | [forgeops](https://github.com/forgeopslabs/appstore-mcp) | [asc-mcp](https://github.com/zelentsov-dev/asc-mcp) | [Joshua](https://github.com/JoshuaRileyDev/app-store-connect-mcp-server) | [`asc`](https://github.com/rorkai/App-Store-Connect-CLI) |
|---|:-:|:-:|:-:|:-:|:-:|
| MCP | + | + | + | + | − |
| Read-only default | + | − | − | − | − |
| Asks before publishing | + | − | − | − | − |
| Injection guard | + | − | − | − | − |
| Release preflight | + | − | − | − | + |
| Sales reports | + | − | + | − | + |
| One binary, any OS | + | + | − | − | + |

## Security

Hamn starts read-only. Anything public, irreversible or touching money shows a preview and waits for your yes in the client. Reviews and tester feedback are marked as untrusted, and your key never reaches the model. Details are in [docs/SAFETY.md](docs/SAFETY.md).

## What it does

Releases, TestFlight, listings, screenshots, reviews, sales reports, IAPs and pricing, signing and Xcode Cloud, plus the rest of the App Store Connect API. See the [tool list](docs/TOOLS.md) and [config](docs/CONFIG.md).

## License

[MIT](LICENSE) © Ivan Verevkin
