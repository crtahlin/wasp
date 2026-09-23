#!/usr/bin/env bash
# Guard the two extractions that build a release's notes.
#
# release.yaml assembles the notes from two places: the highlights block in
# README.md, and the section for the version being released in CHANGELOG.md.
# Both are pulled out by pattern, so a renamed marker or a changed heading does
# not fail anything, it silently yields nothing and the release page goes out
# with empty notes. That is precisely the fault #481 exists to stop, so it is
# checked on every pull request instead of at release time.
#
# Usage: scripts/check-release-notes.sh
set -euo pipefail

cd "$(dirname "$0")/.."

fail() { echo "FAIL: $*" >&2; exit 1; }

# --- 1. the README highlights block ------------------------------------------

starts=$(grep -c '^<!-- highlights:start -->$' README.md || true)
ends=$(grep -c '^<!-- highlights:end -->$' README.md || true)

[ "$starts" = "1" ] || fail "README.md has $starts highlights:start markers, want exactly 1"
[ "$ends" = "1" ]   || fail "README.md has $ends highlights:end markers, want exactly 1"

sline=$(grep -n '^<!-- highlights:start -->$' README.md | cut -d: -f1)
eline=$(grep -n '^<!-- highlights:end -->$' README.md | cut -d: -f1)
[ "$sline" -lt "$eline" ] || fail "highlights:start (line $sline) is not before highlights:end (line $eline)"

highlights=$(sed -n "$((sline + 1)),$((eline - 1))p" README.md)
[ -n "$(echo "$highlights" | tr -d '[:space:]')" ] \
  || fail "the README highlights block is empty, so a release would publish no summary"

echo "ok: README highlights block, $(echo "$highlights" | wc -l | tr -d ' ') lines"

# --- 2. the CHANGELOG section for the newest version -------------------------
#
# Same extraction release.yaml uses: from the first "## [" heading to the line
# before the next one. Anything above the first heading is the file's own
# preamble and is not part of any release.

if [ ! -f CHANGELOG.md ]; then
  echo "ok: no CHANGELOG.md yet, nothing to extract"
  exit 0
fi

# The FIRST second-level heading in the file must be a version heading.
#
# Checking only that some "## [" exists is not enough, and a mutation proved it:
# renaming the newest heading leaves the extraction matching the one below it,
# so the check passes while the release publishes the PREVIOUS version's
# changelog. Newest-first ordering is what the extraction relies on, so that is
# what is asserted.
first_h2=$(grep -m1 '^## ' CHANGELOG.md || true)
[ -n "$first_h2" ] || fail "CHANGELOG.md has no '## ' heading at all"
case "$first_h2" in
  '## ['*) ;;
  *) fail "the first '## ' heading in CHANGELOG.md is \"$first_h2\", not a '## [version]' heading, so the extraction would silently take an older release's section" ;;
esac

section=$(awk '/^## \[/ { if (seen) exit; seen = 1 } seen { print }' CHANGELOG.md)
[ -n "$(echo "$section" | tr -d '[:space:]')" ] \
  || fail "no '## [version]' section found in CHANGELOG.md, so a release would publish no changelog"

heading=$(echo "$section" | head -1)
entries=$(echo "$section" | grep -c '^- ' || true)
[ "$entries" -gt 0 ] || fail "the newest CHANGELOG.md section ($heading) has no entries"

echo "ok: CHANGELOG section $heading, $entries entries"
