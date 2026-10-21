#!/bin/sh
# Print a Homebrew formula that installs the prebuilt release archives
# (formulae are not quarantined, unlike casks). release.yml pushes the
# output to idoo/homebrew-tap/Formula/app-store-connect-mcp.rb after the release is published.
#
# Usage: scripts/brew-formula.sh <version> <checksums.txt>
set -eu

[ $# -eq 2 ] || { echo "usage: $0 <version> <checksums.txt>" >&2; exit 2; }
version=${1#v}
sums=$2

sha() {
	f="app-store-connect-mcp_${version}_$1.tar.gz"
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
class AppStoreConnectMcp < Formula
  desc "MCP server to ship iOS and macOS apps through App Store Connect"
  homepage "https://github.com/idoo/app-store-connect-mcp"
  license "MIT"

  livecheck do
    url :stable
    strategy :github_latest
  end

  on_macos do
    on_arm do
      url "$base/app-store-connect-mcp_${version}_darwin_arm64.tar.gz"
      sha256 "$darwin_arm64"
    end
    on_intel do
      url "$base/app-store-connect-mcp_${version}_darwin_amd64.tar.gz"
      sha256 "$darwin_amd64"
    end
  end

  on_linux do
    on_arm do
      url "$base/app-store-connect-mcp_${version}_linux_arm64.tar.gz"
      sha256 "$linux_arm64"
    end
    on_intel do
      url "$base/app-store-connect-mcp_${version}_linux_amd64.tar.gz"
      sha256 "$linux_amd64"
    end
  end

  def install
    bin.install "app-store-connect-mcp"
  end

  def caveats
    <<~TEXT
      app-store-connect-mcp needs an App Store Connect API key:
        App Store Connect > Users and Access > Integrations > App Store Connect API
      Then register it and connect your client:
        app-store-connect-mcp auth import
        app-store-connect-mcp config install <client>   # claude-code, claude-desktop, codex, cursor, vscode, windsurf
    TEXT
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/app-store-connect-mcp version")
  end
end
EOF
