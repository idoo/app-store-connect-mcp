# Upgrade, rotate, uninstall, troubleshoot

## Upgrade

- Homebrew: `brew upgrade app-store-connect-mcp`.
- install.sh: run the same curl line again. It replaces the binary in `~/.local/bin`.
- `go install`: run it again with `@latest`.
- Claude Desktop: double-click the new `.mcpb`.

Restart your client after upgrading. The client configs point at the binary on your PATH, so you don't need to touch them.

## Rotate a key

1. Create the new key in App Store Connect and download the `.p8`.
2. `app-store-connect-mcp auth add` (or `auth import`) under a new profile name, check it with `auth test <name>`, and point `default_profile` in `~/.config/app-store-connect-mcp/config.toml` at it.
3. `app-store-connect-mcp auth remove <old profile>`. If the old key lived in the keychain and no other profile uses it, this deletes it from the keychain too.
4. Revoke the old key in App Store Connect and run `app-store-connect-mcp doctor`.

## Uninstall

1. Remove the `app-store-connect-mcp` entry from your client: `claude mcp remove app-store-connect-mcp`, or delete it from the file `config install` wrote (listed in [CONFIG.md](CONFIG.md#commands); a backup sits next to it).
2. `app-store-connect-mcp auth remove <profile>` for each profile in `auth list`, so keychain copies go too.
3. Delete the binary (`brew uninstall app-store-connect-mcp` or `rm ~/.local/bin/app-store-connect-mcp`), `~/.config/app-store-connect-mcp` and `~/.local/state/app-store-connect-mcp`.

## Troubleshooting

Start with `app-store-connect-mcp doctor`. It says which check failed and usually how to fix it.

- **401 from Apple.** Wrong key id, issuer id or key kind, or a revoked key. Team keys need the issuer id; individual keys have none. Also check your clock: doctor prints the skew.
- **Missing `.p8`.** You moved or deleted the file. Fix `key_path` by running `auth import` again, or set `ASC_PRIVATE_KEY_PATH`.
- **The wrong account shows up.** `ASC_*` variables in your shell beat a saved profile. doctor names both; `unset ASC_KEY_ID ASC_ISSUER_ID ASC_PRIVATE_KEY_PATH` to use the profile.
- **The client doesn't see the tools.** Restart the client. GUI clients don't read your shell env, so use a profile (`auth import`) rather than exported variables. Write tools are hidden until writes are on: `app-store-connect-mcp config install <client> --allow-writes`.
- **Docker on Linux can't read the key.** The image runs as uid 65532 and `.p8` files are mode 0600, owned by you. Pass the key as a variable instead of a mount, `-e ASC_PRIVATE_KEY="$(cat ~/.appstoreconnect/private_keys/AuthKey_XXXX.p8)"`, or run the container as yourself with `--user "$(id -u)"`. Docker Desktop on macOS hides this problem.
