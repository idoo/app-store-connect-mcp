# Installing Hamn

Written for a coding agent. Humans may read along.

## 1. Binary

Use the first one that works, then check with `hamn version`:

```sh
brew install idoo/tap/hamn
curl -fsSL https://raw.githubusercontent.com/idoo/hamn/main/install.sh | sh   # verifies sha256, installs to ~/.local/bin
go install github.com/idoo/hamn/cmd/hamn@latest
```

Claude Desktop can skip this: double-click `hamn-<version>.mcpb` from [Releases](https://github.com/idoo/hamn/releases).

## 2. API key (the user does this, not you)

Keys never pass through the agent. Ask the user to:

1. Create a key in App Store Connect under *Users and Access → Integrations → App Store Connect API* and download the `.p8`. Team keys show an issuer ID above the list. Individual keys don't have one.
2. Run `hamn auth import` (finds fastlane, `ASC_*` env vars or `~/.appstoreconnect/private_keys/AuthKey_*.p8`) or `hamn auth add` in their own terminal. In Claude Code that's `!hamn auth add`. Both test the key against Apple before saving.
3. Sales and finance reports need your vendor number: App Store Connect → Payments and Financial Reports, top left under the account name. Add it with `hamn auth set <profile> vendor_number=…`.

Then run `hamn doctor`. Everything should say ok.

## 3. Client

```sh
hamn config install claude-code    # or claude-desktop, cursor, codex, vscode, windsurf
```

`hamn config snippet <client>` prints the config instead of writing it. Restart the client afterwards.

## 4. Try it

Call the `doctor` tool, then ask "what's blocking my next release?". Hamn starts read-only, so the worst it can do is know things. See [SAFETY.md](SAFETY.md).
