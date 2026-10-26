#!/bin/sh
# Publish server.json for a released version to the MCP Registry.
# Runs in GitHub Actions (OIDC login); needs the release's .mcpb asset.
#
# Usage: scripts/publish-registry.sh <version> <mcpb-sha256>
set -eu

[ $# -eq 2 ] || { echo "usage: $0 <version> <mcpb-sha256>" >&2; exit 2; }
version=${1#v}
sha=$2
# Pinned publisher; bump both lines together (sha256 of
# mcp-publisher_linux_amd64.tar.gz from the registry release's checksums file).
publisher_version=1.8.4
publisher_sha256=5dc2fd415369c73551259f31cd33e2aacc9e0d1021e7f08fef1492d93b781628

desc_len=$(jq -r '.description | length' server.json)
[ "$desc_len" -le 100 ] || { echo "publish-registry: server.json description is $desc_len chars (max 100)" >&2; exit 1; }

jq --arg v "$version" --arg sha "$sha" '
  .version = $v
  | .packages |= map(
      if .registryType == "mcpb" then
        .version = $v
        | .identifier = "https://github.com/idoo/app-store-connect-mcp/releases/download/v\($v)/app-store-connect-mcp-\($v).mcpb"
        | .fileSha256 = $sha
      elif .registryType == "oci" then
        .identifier = "ghcr.io/idoo/app-store-connect-mcp:\($v)"
      else . end)' server.json >server.release.json
mv server.release.json server.json

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM
curl -fsSL -o "$tmp/publisher.tar.gz" \
	"https://github.com/modelcontextprotocol/registry/releases/download/v${publisher_version}/mcp-publisher_linux_amd64.tar.gz"
echo "$publisher_sha256  $tmp/publisher.tar.gz" | sha256sum -c - >&2
tar -xzf "$tmp/publisher.tar.gz" -C "$tmp" mcp-publisher
"$tmp/mcp-publisher" login github-oidc
"$tmp/mcp-publisher" publish
