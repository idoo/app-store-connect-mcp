#!/bin/sh
# app-store-connect-mcp installer: downloads a release binary from GitHub, verifies its
# sha256 against checksums.txt, the cosign signature of checksums.txt (if
# `cosign` is installed) and the build provenance (if an authenticated `gh` is
# installed), and installs it to ~/.local/bin. If neither cosign nor gh could
# prove authenticity, the binary is installed but not run, and the exact
# verify commands are printed; ASC_MCP_REQUIRE_SIGNATURE=1 refuses instead.
#
#   curl -fsSL https://github.com/idoo/app-store-connect-mcp/releases/latest/download/install.sh | sh
#
# Environment:
#   ASC_MCP_VERSION            release to install, e.g. 0.1.0 (default: latest)
#   ASC_MCP_INSTALL_DIR        target directory (default: ~/.local/bin)
#   ASC_MCP_REQUIRE_SIGNATURE  1 = refuse to install unless cosign or gh verifies the release
#   ASC_MCP_DOWNLOAD_BASE      tests only: serve <base>/v<version>/<asset> instead of
#                              GitHub; https, or http on 127.0.0.1/localhost
set -eu

repo=idoo/app-store-connect-mcp
install_dir=${ASC_MCP_INSTALL_DIR:-$HOME/.local/bin}

say() { printf 'app-store-connect-mcp-install: %s\n' "$*" >&2; }
die() {
	say "error: $*"
	exit 1
}
need() { command -v "$1" >/dev/null 2>&1 || die "$1 is required"; }

need uname
need tar
need curl

# https only, also after redirects (--proto applies to every hop); plain http
# only for a loopback ASC_MCP_DOWNLOAD_BASE.
proto='=https'
download_base=${ASC_MCP_DOWNLOAD_BASE:-}
case $download_base in
"") ;;
https://*) ;;
http://127.0.0.1:* | http://127.0.0.1/* | http://localhost:* | http://localhost/*) proto='=http' ;;
*) die "ASC_MCP_DOWNLOAD_BASE must be https:// or http://127.0.0.1 / http://localhost" ;;
esac

# fetch URL FILE: 0 on 2xx, 4 on 404, 1 otherwise (with the status).
fetch() {
	code=$(curl -sSL --proto "$proto" --tlsv1.2 -o "$2" -w '%{http_code}' "$1") || code=${code:-000}
	case $code in
	2??) return 0 ;;
	404) return 4 ;;
	*)
		say "download failed: $1 (HTTP $code)"
		return 1
		;;
	esac
}
# Final URL after redirects.
resolve() { curl -fsSLI --proto "$proto" --tlsv1.2 -o /dev/null -w '%{url_effective}' "$1"; }

case $(uname -s) in
Darwin) os=darwin ;;
Linux) os=linux ;;
*) die "unsupported OS $(uname -s); download a release manually from https://github.com/$repo/releases" ;;
esac
case $(uname -m) in
x86_64 | amd64) arch=amd64 ;;
arm64 | aarch64) arch=arm64 ;;
*) die "unsupported architecture $(uname -m)" ;;
esac
# A shell under Rosetta reports x86_64 on Apple silicon; prefer the native build.
if [ "$os" = darwin ] && [ "$arch" = amd64 ] && [ "$(sysctl -n hw.optional.arm64 2>/dev/null || echo 0)" = 1 ]; then
	arch=arm64
fi

version=${ASC_MCP_VERSION:-}
if [ -z "$version" ] && [ -n "$download_base" ]; then
	die "ASC_MCP_DOWNLOAD_BASE needs ASC_MCP_VERSION"
fi
if [ -z "$version" ]; then
	# The releases/latest page redirects to .../releases/tag/vX.Y.Z; no API
	# call, so no 60-requests-per-hour limit.
	latest=$(resolve "https://github.com/$repo/releases/latest") || latest=
	case $latest in
	*/releases/tag/*) version=${latest##*/releases/tag/} ;;
	*) die "could not determine the latest release (set ASC_MCP_VERSION)" ;;
	esac
fi
version=${version#v}
case $version in
"" | *[!0-9A-Za-z.+-]*) die "invalid version '$version'" ;;
esac

archive="app-store-connect-mcp_${version}_${os}_${arch}.tar.gz"
base="${download_base:-https://github.com/$repo/releases/download}/v$version"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

say "downloading app-store-connect-mcp $version ($os/$arch)"
fetch "$base/$archive" "$tmp/$archive" || {
	[ $? = 4 ] && die "release v$version not found (see https://github.com/$repo/releases)"
	die "could not download $archive"
}
fetch "$base/checksums.txt" "$tmp/checksums.txt" || die "could not download checksums.txt for v$version"

# authentic=1 once cosign or gh ties the download to this repo's release workflow.
authentic=0
if command -v cosign >/dev/null 2>&1; then
	fetch "$base/checksums.txt.sigstore.json" "$tmp/checksums.txt.sigstore.json" ||
		die "checksums.txt.sigstore.json missing from release v$version"
	# Exact identity: the release workflow at this very tag, not any tag.
	cosign verify-blob --bundle "$tmp/checksums.txt.sigstore.json" \
		--certificate-identity "https://github.com/$repo/.github/workflows/release.yml@refs/tags/v$version" \
		--certificate-oidc-issuer https://token.actions.githubusercontent.com \
		"$tmp/checksums.txt" >/dev/null 2>&1 ||
		die "cosign could not verify checksums.txt; refusing to install"
	say "checksums.txt signature verified (cosign)"
	authentic=1
else
	say "cosign not installed; skipping the checksums.txt signature check"
fi

want=$(awk -v f="$archive" '$2 == f { print $1 }' "$tmp/checksums.txt")
[ -n "$want" ] || die "$archive not listed in checksums.txt"
if command -v sha256sum >/dev/null 2>&1; then
	got=$(sha256sum "$tmp/$archive" | awk '{ print $1 }')
elif command -v shasum >/dev/null 2>&1; then
	got=$(shasum -a 256 "$tmp/$archive" | awk '{ print $1 }')
else
	die "sha256sum or shasum is required"
fi
[ "$want" = "$got" ] || die "checksum mismatch for $archive (want $want, got $got)"
say "sha256 verified"

# Provenance pinned to this repo's release workflow at this very tag, like the
# cosign identity above; any other workflow's attestation is rejected.
if ! command -v gh >/dev/null 2>&1; then
	say "gh not installed; skipping the build provenance check"
elif ! gh auth status >/dev/null 2>&1; then
	say "gh not authenticated; skipping the build provenance check"
elif gh attestation verify "$tmp/$archive" --repo "$repo" \
	--signer-workflow "$repo/.github/workflows/release.yml" \
	--source-ref "refs/tags/v$version" >/dev/null; then
	say "build provenance verified (gh attestation)"
	authentic=1
else
	die "gh attestation verify failed for $archive; refusing to install"
fi
if [ "$authentic" = 0 ] && [ "${ASC_MCP_REQUIRE_SIGNATURE:-0}" = 1 ]; then
	die "ASC_MCP_REQUIRE_SIGNATURE=1 but neither cosign nor an authenticated gh could verify release v$version; refusing to install"
fi

tar -xzf "$tmp/$archive" -C "$tmp" app-store-connect-mcp
mkdir -p "$install_dir"
dest="$install_dir/app-store-connect-mcp"
# Install via a temp name + rename so a running app-store-connect-mcp is not truncated.
cp "$tmp/app-store-connect-mcp" "$dest.new"
chmod 0755 "$dest.new"
if [ "$os" = darwin ]; then
	xattr -d com.apple.quarantine "$dest.new" 2>/dev/null || true
fi
mv -f "$dest.new" "$dest"

case ":$PATH:" in
*":$install_dir:"*) ;;
*)
	say "$install_dir is not on your PATH; add this to your shell profile:"
	# shellcheck disable=SC2016 # $PATH is meant literally for the profile line
	printf '\n  export PATH="%s:$PATH"\n\n' "$install_dir" >&2
	;;
esac

if [ "$authentic" = 0 ]; then
	# Not run: an unauthenticated binary must not execute before the user checks it.
	cat >&2 <<EOF

WARNING: installed $dest but did NOT run it. Its sha256 matches checksums.txt
from the (immutable) release v$version, but neither cosign nor an authenticated
gh was available to prove that release was built by $repo's release workflow.
Verify it before first use with either of:

  gh attestation verify $archive --repo $repo \\
    --signer-workflow $repo/.github/workflows/release.yml --source-ref refs/tags/v$version

  cosign verify-blob --bundle checksums.txt.sigstore.json \\
    --certificate-identity https://github.com/$repo/.github/workflows/release.yml@refs/tags/v$version \\
    --certificate-oidc-issuer https://token.actions.githubusercontent.com checksums.txt

(download $archive, checksums.txt and checksums.txt.sigstore.json from
https://github.com/$repo/releases/tag/v$version), or re-run this installer with
cosign or an authenticated gh installed. Set ASC_MCP_REQUIRE_SIGNATURE=1 to
refuse unverified installs. Then: $dest auth import, then $dest config install claude-code

EOF
	exit 0
fi

say "installed $dest ($("$dest" version 2>/dev/null || echo "$version"))"
# Credentials first, and only while no profile is saved.
case "$("$dest" auth list 2>/dev/null || true)" in
"" | "no profiles"*)
	say "next: create an API key (App Store Connect > Users and Access > Integrations), then run: $dest auth import (or: $dest auth add)"
	;;
esac
say "then add app-store-connect-mcp to your client: $dest config install claude-code (or claude-desktop, codex, cursor, vscode, windsurf)"
