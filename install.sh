#!/bin/sh
# hamn installer: downloads a release binary from GitHub, verifies its
# sha256 against checksums.txt (and the build provenance if `gh` is
# installed), and installs it to ~/.local/bin.
#
#   curl -fsSL https://raw.githubusercontent.com/idoo/hamn/main/install.sh | sh
#
# Environment:
#   HAMN_VERSION      release to install, e.g. 0.1.0 (default: latest)
#   HAMN_INSTALL_DIR  target directory (default: ~/.local/bin)
set -eu

repo=idoo/hamn
install_dir=${HAMN_INSTALL_DIR:-$HOME/.local/bin}

say() { printf 'hamn-install: %s\n' "$*" >&2; }
die() {
	say "error: $*"
	exit 1
}
need() { command -v "$1" >/dev/null 2>&1 || die "$1 is required"; }

need uname
need tar
if command -v curl >/dev/null 2>&1; then
	fetch() { curl -fsSL --proto '=https' --tlsv1.2 -o "$2" "$1"; }
	fetch_stdout() { curl -fsSL --proto '=https' --tlsv1.2 "$1"; }
elif command -v wget >/dev/null 2>&1; then
	fetch() { wget -q --https-only -O "$2" "$1"; }
	fetch_stdout() { wget -q --https-only -O - "$1"; }
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

version=${HAMN_VERSION:-}
if [ -z "$version" ]; then
	version=$(fetch_stdout "https://api.github.com/repos/$repo/releases/latest" |
		sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n 1)
	[ -n "$version" ] || die "could not determine the latest release (set HAMN_VERSION)"
fi
version=${version#v}

archive="hamn_${version}_${os}_${arch}.tar.gz"
base="https://github.com/$repo/releases/download/v$version"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

say "downloading hamn $version ($os/$arch)"
fetch "$base/$archive" "$tmp/$archive"
fetch "$base/checksums.txt" "$tmp/checksums.txt"

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

if command -v gh >/dev/null 2>&1; then
	if gh attestation verify "$tmp/$archive" --repo "$repo" >/dev/null 2>&1; then
		say "build provenance verified (gh attestation)"
	else
		say "warning: gh attestation verify failed or unavailable (not logged in?); continuing on the sha256 check"
	fi
fi

tar -xzf "$tmp/$archive" -C "$tmp" hamn
mkdir -p "$install_dir"
dest="$install_dir/hamn"
# Install via a temp name + rename so a running hamn is not truncated.
cp "$tmp/hamn" "$dest.new"
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

say "next: create an API key (App Store Connect > Users and Access > Integrations), then add hamn to Claude Code:"
"$dest" config snippet claude-code || true
