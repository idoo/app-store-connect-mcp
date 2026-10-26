# Installing app-store-connect-mcp

Written for a coding agent. Humans may read along.

## 1. Binary

Use the first one that works, then check with `app-store-connect-mcp version`:

```sh
brew install idoo/tap/app-store-connect-mcp
curl -fsSL https://github.com/idoo/app-store-connect-mcp/releases/latest/download/install.sh | sh   # verifies sha256 and signatures, installs to ~/.local/bin
go install github.com/idoo/app-store-connect-mcp/cmd/app-store-connect-mcp@latest
```

Claude Desktop can skip this: double-click `app-store-connect-mcp-<version>.mcpb` from [Releases](https://github.com/idoo/app-store-connect-mcp/releases).

Docker (`ghcr.io/idoo/app-store-connect-mcp`) speaks stdio by default. Mount the key read-only and pass credentials as env:

```sh
docker run -i --rm \
  -e ASC_KEY_ID -e ASC_ISSUER_ID -e ASC_PRIVATE_KEY_PATH=/keys/AuthKey_XXXX.p8 \
  -v ~/.appstoreconnect/private_keys:/keys:ro \
  ghcr.io/idoo/app-store-connect-mcp
```

On Linux the container runs as uid 65532 and can't read a 0600 key you own. Pass it as `-e ASC_PRIVATE_KEY="$(cat …p8)"` instead of the mount, or add `--user "$(id -u)"`.

For HTTP, the container listens on a non-loopback address, so it needs `--allow-remote`, a bearer token and TLS:

```sh
docker run -d --name app-store-connect-mcp -p 8765:8765 \
  -e ASC_KEY_ID -e ASC_ISSUER_ID -e ASC_PRIVATE_KEY_PATH=/keys/AuthKey_XXXX.p8 -e ASC_MCP_HTTP_TOKEN \
  -v ~/.appstoreconnect/private_keys:/keys:ro -v "$PWD/tls:/tls:ro" \
  ghcr.io/idoo/app-store-connect-mcp serve --http 0.0.0.0:8765 --allow-remote --tls-cert /tls/cert.pem --tls-key /tls/key.pem
```

Browser-based clients send an `Origin` header; loopback origins are always allowed. Allow others with `--allow-origin https://app.example` (repeatable) or `ASC_MCP_HTTP_ALLOWED_ORIGINS=https://a.example,https://b.example`. Both need `--allow-remote` and take exact origins (`scheme://host[:port]`, no path).

## 2. API key (the user does this, not you)

Keys never pass through the agent. Ask the user to:

1. Create a key in App Store Connect under *Users and Access → Integrations → App Store Connect API* and download the `.p8`. Team keys show an issuer ID above the list. Individual keys don't have one.
2. Run `app-store-connect-mcp auth add` in a separate terminal (it asks questions), or `auth import` with `ASC_*` set (it also finds fastlane and `~/.appstoreconnect/private_keys/AuthKey_*.p8`). Both test the key against Apple before saving.
3. Sales and finance reports need your vendor number: App Store Connect → Payments and Financial Reports, top left under the account name. Add it with `app-store-connect-mcp auth set <profile> vendor_number=…`.

Then run `app-store-connect-mcp doctor`. Everything should say ok.

## 3. Client

```sh
app-store-connect-mcp config install claude-code    # or claude-desktop, cursor, codex, vscode, windsurf
```

`app-store-connect-mcp config snippet <client>` prints the config instead of writing it. Restart the client afterwards.

## 4. Try it

Call the `doctor` tool, then ask "what's blocking my next release?". app-store-connect-mcp starts read-only, so the worst it can do is know things. See [SAFETY.md](SAFETY.md). Upgrading, uninstalling and common errors: [FAQ.md](FAQ.md).
