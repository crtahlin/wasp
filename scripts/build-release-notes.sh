#!/usr/bin/env bash
# Build the release notes for a wasp release and write them to stdout.
#
# release.yaml passes the result to goreleaser with --release-notes, and
# scripts/check-release-notes.sh runs it on every pull request, so the page a
# release publishes is built by the same code a pull request checks. See #481
# and #531.
#
# Five parts, in this order:
#   1. the experimental-software warning
#   2. "About this release": upstream base, previous release, rollback
#   3. the README highlights block
#   4. the link to docs/DIFFERENCES.md at this tag
#   5. the CHANGELOG.md section for the newest version
#
# Usage: scripts/build-release-notes.sh <tag> [repository]
#   <tag>         the release tag, for example v0.1.6
#   [repository]  owner/name for links, default crtahlin/wasp
set -euo pipefail

cd "$(dirname "$0")/.."

tag=${1:?usage: build-release-notes.sh <tag> [repository]}
repo=${2:-crtahlin/wasp}
base=$(cat .upstream-base)

# The newest release tag before this one. A shallow checkout has no tags, so
# the rollback line then names no version rather than failing the build.
prev=$(git describe --tags --abbrev=0 --match 'v[0-9]*.[0-9]*.[0-9]*' "${tag}^" 2>/dev/null || true)
if [ -n "$prev" ]; then
  prev_line="- **Previous release:** ${prev}."
  rollback="install the ${prev} package with \`apt-get install --allow-downgrades ./wasp_${prev#v}_amd64.deb\` and restart."
else
  prev_line=""
  rollback="install the previous release's package with \`apt-get install --allow-downgrades\` and restart."
fi

cat <<EOF
> **Experimental software.** wasp is an experimental downstream distribution of Bee, not affiliated with or supported by the Swarm Foundation. Run it on nodes whose stake and data you are prepared to risk.

## About this release

- **Upstream base:** Bee ${base}.
EOF
[ -z "$prev_line" ] || echo "$prev_line"
cat <<EOF
- **Rollback:** ${rollback} Check the changelog below for a change to the storage format first; after one, going back is not safe.

EOF

sed -n '/^<!-- highlights:start -->$/,/^<!-- highlights:end -->$/p' README.md | sed '1d;$d'

echo
echo "For every difference from Bee, entry by entry, see [docs/DIFFERENCES.md](https://github.com/${repo}/blob/${tag}/docs/DIFFERENCES.md)."
echo
awk '/^## \[/ { if (seen) exit; seen = 1 } seen { print }' CHANGELOG.md
