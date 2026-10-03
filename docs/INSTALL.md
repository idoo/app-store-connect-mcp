# Installing Hamn

These steps are written for a coding agent (Claude Code, Cursor, Codex and so on), but a person can follow them too. Hamn is an MCP server for App Store Connect, and it ships as one binary called `hamn`.

## 1. Install the binary

Use the first option that works:

```sh
brew install idoo/tap/hamn                                                     # macOS / Linux
curl -fsSL https://raw.githubusercontent.com/idoo/hamn/main/install.sh | sh   # checks sha256 (and provenance with gh), installs to ~/.local/bin
go install github.com/idoo/hamn/cmd/hamn@latest
```

Then run `hamn version` to check it worked.

Claude Desktop users can skip the binary: download `hamn-<version>.mcpb` from [Releases](https://github.com/idoo/hamn/releases) and double-click it.

## 2. API key (the user does this step)

Credentials never go through the agent. Ask the user to:

1. Create a key in App Store Connect under *Users and Access → Integrations → App Store Connect API*, and download the `.p8`. A team key has an issuer ID, shown above the key list. An individual key has none.
2. Run one of these in their own terminal. In Claude Code they can type `!` followed by the command:

   ```sh
   hamn auth import   # picks up fastlane, ASC_* env vars or ~/.appstoreconnect/private_keys/AuthKey_*.p8
   hamn auth add      # or answer a few questions
   ```

   Both ask whether it's a team or individual key, and make one test call to Apple before saving.
3. Optional, for sales and finance reports: `hamn auth set vendor_number=<number>`.

When the user is done, run `hamn doctor`. Every check should be ok.

## 3. Connect the client

```sh
hamn config install claude-code    # or: claude-desktop, cursor, codex, vscode, windsurf
```

This writes the client's MCP config, or for Claude Code runs `claude mcp add`. To print the config instead of writing it, use `hamn config snippet <client>`. Restart the client (or reload MCP servers) afterwards.

## 4. Check

Call the `doctor` tool from the client. Then try something like "what's blocking my next release?".

Hamn starts read-only. Writes stay off until the user turns them on, and anything public asks the user first. See [SAFETY.md](SAFETY.md).
