#!/bin/sh
# Print a Homebrew formula that installs the prebuilt release archives
# (formulae are not quarantined, unlike casks). release.yml pushes the
# output to idoo/homebrew-tap/Formula/asc-mcp.rb after the release is published.
#
# Usage: scripts/brew-formula.sh <version> <checksums.txt>
set -eu

[ $# -eq 2 ] || { echo "usage: $0 <version> <checksums.txt>" >&2; exit 2; }
version=${1#v}
sums=$2

sha() {
	f="asc-mcp_${version}_$1.tar.gz"
	s=$(awk -v f="$f" '$2 == f { print $1 }' "$sums")
	[ -n "$s" ] || { echo "brew-formula: $f not in $sums" >&2; exit 1; }
	printf '%s' "$s"
}

base="https://github.com/idoo/app-store-connect-mcp/releases/download/v$version"
darwin_arm64=$(sha darwin_arm64)
darwin_amd64=$(sha darwin_amd64)
linux_arm64=$(sha linux_arm64)
linux_amd64=$(sha linux_amd64)

cat <<EOF
class AscMcp < Formula
  desc "App Store Connect MCP server: ship iOS and macOS apps from your agent"
  homepage "https://github.com/idoo/app-store-connect-mcp"
  version "$version"
  license "MIT"

  livecheck do
    url :stable
    strategy :github_latest
  end

  on_macos do
    on_arm do
      url "$base/asc-mcp_${version}_darwin_arm64.tar.gz"
      sha256 "$darwin_arm64"
    end
    on_intel do
      url "$base/asc-mcp_${version}_darwin_amd64.tar.gz"
      sha256 "$darwin_amd64"
    end
  end

  on_linux do
    on_arm do
      url "$base/asc-mcp_${version}_linux_arm64.tar.gz"
      sha256 "$linux_arm64"
    end
    on_intel do
      url "$base/asc-mcp_${version}_linux_amd64.tar.gz"
      sha256 "$linux_amd64"
    end
  end

  def install
    bin.install "asc-mcp"
  end

  def caveats
    <<~TEXT
      asc-mcp needs an App Store Connect API key:
        App Store Connect > Users and Access > Integrations > App Store Connect API
      Then register it and connect your client:
        asc-mcp auth import
        asc-mcp config install <client>   # claude-code, claude-desktop, codex, cursor, vscode, windsurf
    TEXT
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/asc-mcp version")
  end
end
EOF
