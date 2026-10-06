#!/bin/sh
# asc-mcp installer: downloads a release binary from GitHub, verifies its
# sha256 against checksums.txt, the cosign signature of checksums.txt (if
# `cosign` is installed) and the build provenance (if `gh` is installed),
# and installs it to ~/.local/bin.
#
#   curl -fsSL https://raw.githubusercontent.com/idoo/app-store-connect-mcp/main/install.sh | sh
#
# Environment:
#   ASC_MCP_VERSION         release to install, e.g. 0.1.0 (default: latest)
#   ASC_MCP_INSTALL_DIR     target directory (default: ~/.local/bin)
#   ASC_MCP_REQUIRE_COSIGN  1 = refuse to install unless cosign verifies checksums.txt
set -eu

repo=idoo/app-store-connect-mcp
install_dir=${ASC_MCP_INSTALL_DIR:-$HOME/.local/bin}

say() { printf 'asc-mcp-install: %s\n' "$*" >&2; }
die() {
	say "error: $*"
	exit 1
}
need() { command -v "$1" >/dev/null 2>&1 || die "$1 is required"; }

need uname
need tar
if command -v curl >/dev/null 2>&1; then
	fetch() { curl -fsSL --proto '=https' --tlsv1.2 -o "$2" "$1"; }
	# Final URL after redirects.
	resolve() { curl -fsSLI --proto '=https' --tlsv1.2 -o /dev/null -w '%{url_effective}' "$1"; }
elif command -v wget >/dev/null 2>&1; then
	fetch() { wget -q --https-only -O "$2" "$1"; }
	resolve() { wget -q --https-only --max-redirect=5 -S --spider "$1" 2>&1 | sed -n 's/^ *[Ll]ocation: *//p' | tail -n 1 | tr -d '\r'; }
else
	die "curl or wget is required"
fi

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

archive="asc-mcp_${version}_${os}_${arch}.tar.gz"
base="https://github.com/$repo/releases/download/v$version"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

say "downloading asc-mcp $version ($os/$arch)"
fetch "$base/$archive" "$tmp/$archive"
fetch "$base/checksums.txt" "$tmp/checksums.txt"

if command -v cosign >/dev/null 2>&1; then
	fetch "$base/checksums.txt.sigstore.json" "$tmp/checksums.txt.sigstore.json" ||
		die "checksums.txt.sigstore.json missing from release v$version"
	cosign verify-blob --bundle "$tmp/checksums.txt.sigstore.json" \
		--certificate-identity-regexp "^https://github.com/$repo/.github/workflows/release.yml@refs/tags/v" \
		--certificate-oidc-issuer https://token.actions.githubusercontent.com \
		"$tmp/checksums.txt" >/dev/null 2>&1 ||
		die "cosign could not verify checksums.txt; refusing to install"
	say "checksums.txt signature verified (cosign)"
elif [ "${ASC_MCP_REQUIRE_COSIGN:-0}" = 1 ]; then
	die "ASC_MCP_REQUIRE_COSIGN=1 but cosign is not installed"
else
	say "warning: cosign not installed; skipping the checksums.txt signature check"
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

if ! command -v gh >/dev/null 2>&1; then
	say "warning: gh not installed; skipping build provenance check, relying on the sha256 check"
elif ! gh auth status >/dev/null 2>&1; then
	say "warning: gh not authenticated; skipping build provenance check, relying on the sha256 check"
elif gh attestation verify "$tmp/$archive" --repo "$repo" >/dev/null; then
	say "build provenance verified (gh attestation)"
else
	die "gh attestation verify failed for $archive; refusing to install"
fi

tar -xzf "$tmp/$archive" -C "$tmp" asc-mcp
mkdir -p "$install_dir"
dest="$install_dir/asc-mcp"
# Install via a temp name + rename so a running asc-mcp is not truncated.
cp "$tmp/asc-mcp" "$dest.new"
chmod 0755 "$dest.new"
if [ "$os" = darwin ]; then
	xattr -d com.apple.quarantine "$dest.new" 2>/dev/null || true
fi
mv -f "$dest.new" "$dest"
say "installed $dest ($("$dest" version 2>/dev/null || echo "$version"))"

case ":$PATH:" in
*":$install_dir:"*) ;;
*)
	say "$install_dir is not on your PATH; add this to your shell profile:"
	# shellcheck disable=SC2016 # $PATH is meant literally for the profile line
	printf '\n  export PATH="%s:$PATH"\n\n' "$install_dir" >&2
	;;
esac

say "next: create an API key (App Store Connect > Users and Access > Integrations), then add asc-mcp to Claude Code:"
"$dest" config snippet claude-code || true
