#!/bin/sh
# Wire-API compatibility gate: compare docs/tools.json with the copy attached
# to the latest GitHub release (asset tools.json) and fail on breaking changes:
#   - a removed (or renamed) tool
#   - a removed argument
#   - a newly required argument
#   - a class change from R (read) to W/D
# Accepts either a bare tools array or the tools/list shape {"tools": [...]}.
# Class comes from .class when present, else from MCP annotations.
#
# Usage: scripts/check-compat.sh [old.json] [new.json]
# Without old.json it downloads the latest release asset via gh; it exits 0
# (skip) when there is no release or no tools.json yet.
set -eu

root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
new=${2:-$root/docs/tools.json}
old=${1:-}

if [ ! -f "$new" ]; then
	echo "check-compat: $new not found; skipping" >&2
	exit 0
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

if [ -z "$old" ]; then
	repo=${GITHUB_REPOSITORY:-idoo/hamn}
	if ! tag=$(gh release view --repo "$repo" --json tagName --jq .tagName 2>/dev/null) || [ -z "$tag" ]; then
		echo "check-compat: no release of $repo yet; skipping" >&2
		exit 0
	fi
	if ! gh release download "$tag" --repo "$repo" --pattern tools.json --dir "$tmp" 2>/dev/null; then
		echo "check-compat: release $tag has no tools.json asset; skipping" >&2
		exit 0
	fi
	old=$tmp/tools.json
	echo "check-compat: comparing against $tag" >&2
fi

# Normalize to {name: {class, args: [..], required: [..]}}.
norm='(if type == "array" then . else .tools end)
	| map({key: .name, value: {
		class: (.class // (if .annotations.readOnlyHint then "R"
			elif .annotations.destructiveHint then "D" else "W" end)),
		args: ((.inputSchema.properties // {}) | keys),
		required: (.inputSchema.required // [])
	}}) | from_entries'

jq "$norm" "$old" >"$tmp/old.n"
jq "$norm" "$new" >"$tmp/new.n"

jq -r --slurpfile new "$tmp/new.n" '
	. as $old | $new[0] as $new
	| [ $old | to_entries[] | .key as $t | .value as $o
		| if ($new[$t] | not) then "removed tool: \($t)"
		  else $new[$t] as $n
			| ( ($o.args - $n.args)[] | "\($t): removed argument \(.)" ),
			  ( ($n.required - $o.required)[] | "\($t): newly required argument \(.)" ),
			  ( select($o.class == "R" and $n.class != "R") | "\($t): class R -> \($n.class)" )
		  end
	  ] | .[]' "$tmp/old.n" >"$tmp/breaks"

if [ -s "$tmp/breaks" ]; then
	echo "check-compat: breaking wire-API changes (need a major version or a deprecation cycle):" >&2
	sed 's/^/  - /' "$tmp/breaks" >&2
	exit 1
fi
echo "check-compat: no breaking changes" >&2
