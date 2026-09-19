#!/bin/sh
# Pack goreleaser's dist/ binaries into one cross-platform Claude Desktop
# bundle: dist/hamn-<version>.mcpb.
#
# Usage: scripts/build-mcpb.sh [version]   (default: dist/metadata.json)
# Needs: jq, zip. Validates with `npx @anthropic-ai/mcpb` when npx exists.
set -eu

root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
dist="$root/dist"
[ -d "$dist" ] || { echo "build-mcpb: $dist missing; run goreleaser first" >&2; exit 1; }

version=${1:-}
if [ -z "$version" ]; then
	version=$(jq -r .version "$dist/metadata.json")
fi
version=${version#v}

# Find one binary in dist/ by goreleaser target directory prefix.
find_bin() {
	for d in "$dist"/$1; do
		for f in "$d/hamn" "$d/hamn.exe"; do
			if [ -f "$f" ]; then
				printf '%s\n' "$f"
				return 0
			fi
		done
	done
	echo "build-mcpb: no binary matching dist/$1" >&2
	return 1
}

stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT INT TERM
mkdir -p "$stage/server"

cp "$(find_bin 'hamn-universal_darwin_all')" "$stage/server/hamn-darwin"
cp "$(find_bin 'hamn_linux_amd64*')" "$stage/server/hamn-linux-amd64"
cp "$(find_bin 'hamn_windows_amd64*')" "$stage/server/hamn-windows-amd64.exe"
chmod 0755 "$stage/server/hamn-darwin" "$stage/server/hamn-linux-amd64"

cp "$root/packaging/mcpb/icon.png" "$root/LICENSE" "$stage/"
jq --arg v "$version" '.version = $v' "$root/packaging/mcpb/manifest.json" >"$stage/manifest.json"

if command -v npx >/dev/null 2>&1; then
	npx -y @anthropic-ai/mcpb validate "$stage/manifest.json" >&2
else
	echo "build-mcpb: npx not found, skipping manifest validation" >&2
fi

out="$dist/hamn-$version.mcpb"
rm -f "$out"
(cd "$stage" && zip -qrX "$out" manifest.json icon.png LICENSE server)
echo "$out"
