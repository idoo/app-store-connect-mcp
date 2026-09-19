#!/bin/sh
# Publish server.json for a released version to the MCP Registry.
# Runs in GitHub Actions (OIDC login); needs the release's .mcpb asset.
#
# Usage: scripts/publish-registry.sh <version> <mcpb-sha256>
set -eu

[ $# -eq 2 ] || { echo "usage: $0 <version> <mcpb-sha256>" >&2; exit 2; }
version=${1#v}
sha=$2
publisher_version=${PUBLISHER_VERSION:-1.8.4}

desc_len=$(jq -r '.description | length' server.json)
[ "$desc_len" -le 100 ] || { echo "publish-registry: server.json description is $desc_len chars (max 100)" >&2; exit 1; }

jq --arg v "$version" --arg sha "$sha" '
  .version = $v
  | .packages |= map(
      if .registryType == "mcpb" then
        .version = $v
        | .identifier = "https://github.com/idoo/hamn/releases/download/v\($v)/hamn-\($v).mcpb"
        | .fileSha256 = $sha
      elif .registryType == "oci" then
        .identifier = "ghcr.io/idoo/hamn:\($v)"
      else . end)' server.json >server.release.json
mv server.release.json server.json

tmp=$(mktemp -d)
curl -fsSL "https://github.com/modelcontextprotocol/registry/releases/download/v${publisher_version}/mcp-publisher_linux_amd64.tar.gz" |
	tar -xz -C "$tmp" mcp-publisher
"$tmp/mcp-publisher" login github-oidc
"$tmp/mcp-publisher" publish
