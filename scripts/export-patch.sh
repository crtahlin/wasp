#!/usr/bin/env bash
#
# Export one experiment as a patch series that applies to CURRENT upstream master.
#
#   scripts/export-patch.sh <slug>
#
# This is what makes the work here reusable by someone else — an operator who
# wants only one change, or Ethersphere if a change is worth offering upstream.
#
# Why it can reconstruct the series at all: every experiment lands on main as a
# --no-ff merge commit, so for the merge M reached through its exp-<slug> tag,
#   M^1                        is main before the experiment
#   M^2                        is the experiment tip
#   merge-base(M^1,M^2)..M^2   is EXACTLY the experiment's own commits
# and that holds no matter how many upstream syncs have landed since. A
# squash-merge would destroy it permanently, which is why squashing is disabled
# at the repository level.
#
# Patches are generated on demand rather than committed, because a committed
# patch goes stale the moment upstream moves.
set -euo pipefail

if [ $# -ne 1 ]; then
  echo "usage: $(basename "$0") <slug>" >&2
  echo >&2
  echo "known experiments:" >&2
  git tag -l 'exp-*' | sed 's|^exp-|  |' >&2
  exit 2
fi

SLUG="$1"
cd "$(git rev-parse --show-toplevel)"

# Experiment tags are exp-<slug>, deliberately without a slash: goreleaser
# derives its version from git tags and a slash lands in package filenames as a
# path separator, breaking the build. See .goreleaser.yml.
TAG="exp-$SLUG"
if ! git rev-parse -q --verify "refs/tags/$TAG" >/dev/null; then
  echo "error: no tag $TAG" >&2
  echo "known experiments:" >&2
  git tag -l 'exp-*' | sed 's|^exp-|  |' >&2
  exit 1
fi

M="$(git rev-parse "$TAG^{commit}")"

# A merge commit is required: the reconstruction below depends on two parents.
if [ "$(git rev-list --no-walk --count --merges "$M")" -ne 1 ]; then
  echo "error: $TAG does not point at a merge commit." >&2
  echo "       The experiment was probably squash-merged, which loses the series." >&2
  exit 1
fi

BASE="$(git merge-base "$M^1" "$M^2")"
OUT="dist/patches/$SLUG"
WT="$(mktemp -d)"

echo "experiment : $SLUG"
echo "merge      : $(git log -1 --format='%h %s' "$M")"
echo "commits    : $(git rev-list --count "$BASE..$M^2")"

git fetch --quiet upstream master
UP="$(git rev-parse upstream/master)"
echo "upstream   : $(git log -1 --format='%h %s' "$UP" | cut -c1-60)"
echo

rm -rf "$OUT"; mkdir -p "$OUT"
cleanup() { git worktree remove --force "$WT" >/dev/null 2>&1 || true; rm -rf "$WT"; }
trap cleanup EXIT

git worktree add -f --detach "$WT" "$UP" >/dev/null

# Replay the experiment's own commits onto current upstream. Conflicts surface
# here, honestly, rather than being papered over — a series that no longer
# applies is something the person porting it needs to know.
if ! git -C "$WT" cherry-pick "$BASE..$M^2" >/dev/null 2>&1; then
  echo "CONFLICT replaying onto current upstream master." >&2
  echo "Upstream has changed underneath this experiment; it needs rebasing by hand." >&2
  echo "Conflicting paths:" >&2
  git -C "$WT" diff --name-only --diff-filter=U | sed 's/^/  /' >&2
  git -C "$WT" cherry-pick --abort >/dev/null 2>&1 || true
  exit 1
fi

git -C "$WT" format-patch --quiet --output-directory "$PWD/$OUT" "$UP"

echo "wrote $(ls -1 "$OUT" | wc -l | tr -d ' ') patch(es) to $OUT/"
ls -1 "$OUT" | sed 's/^/  /'
