#!/usr/bin/env bash
# Configure the upstream remote for a fresh clone of wasp.
#
# Two things matter here and neither is obvious:
#
#   1. The upstream remote is FETCH-ONLY. Its push URL is set to a value that
#      cannot resolve, so a mistyped `git push upstream` fails loudly instead of
#      attempting to write to ethersphere/bee. Nothing from this fork is ever
#      pushed upstream.
#
#   2. Upstream tags are fetched into a SEPARATE namespace, refs/tags/upstream/*.
#      Upstream's tags (v2.8.1, v2.8.2, ...) become reachable from main once a
#      sync merge lands. If they lived in the default tag namespace, the
#      `git describe --match 'v[0-9]*'` in the Makefile could pick an upstream
#      tag over this fork's own release tag and stamp a build with the wrong
#      version. Namespacing them makes that impossible rather than unlikely.
#
# Safe to re-run.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

UPSTREAM_URL="https://github.com/ethersphere/bee.git"

if git remote get-url upstream >/dev/null 2>&1; then
  git remote set-url upstream "$UPSTREAM_URL"
else
  git remote add upstream "$UPSTREAM_URL"
fi

git remote set-url --push upstream DO_NOT_PUSH

# Replace any existing fetch refspecs with exactly the two we want.
git config --unset-all remote.upstream.fetch || true
git config --add remote.upstream.fetch '+refs/heads/*:refs/remotes/upstream/*'
git config --add remote.upstream.fetch '+refs/tags/*:refs/tags/upstream/*'
git config remote.upstream.tagOpt --no-tags

# Any upstream tags already sitting in the default namespace must go, or
# git describe can still find them.
# (portable to bash 3.2, which is what macOS ships)
stray=""
for t in $(git tag -l 'v[0-9]*'); do
  if git rev-parse -q --verify "refs/tags/upstream/$t" >/dev/null 2>&1; then
    stray="$stray $t"
  fi
done
if [ -n "$stray" ]; then
  printf 'removing upstream tags from the default namespace:%s\n' "$stray"
  # shellcheck disable=SC2086
  git tag -d $stray >/dev/null
fi

git fetch upstream

cat <<EOF

upstream configured:
  fetch : $(git remote get-url upstream)
  push  : $(git remote get-url --push upstream)   (intentionally unusable)
  tags  : refs/tags/upstream/*  ($(git tag -l 'upstream/*' | wc -l | tr -d ' ') known)
  base  : $(cat .upstream-base 2>/dev/null || echo 'unknown')
EOF
