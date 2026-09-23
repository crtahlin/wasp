#!/usr/bin/env bash
#
# Every merge on main since the last release must appear in the changelog.
#
# The changelog is built from merge-commit subjects, so a merge whose subject
# is not a conventional-commit line is skipped — silently. That is not
# hypothetical: #104 was merged through the web interface with GitHub's default
# subject and vanished from the changelog with no warning, while every other
# pull request appeared. A release artefact that quietly omits a change is
# worse than one that fails to build.
#
# This checks the OUTCOME rather than the format: it asks git-cliff what it
# produced and compares against the merges that actually happened. A future
# subject shape nobody anticipated still gets caught.
set -euo pipefail

command -v git-cliff >/dev/null || { echo "ERROR: git-cliff not found" >&2; exit 1; }

LAST_TAG="$(git describe --tags --abbrev=0 --match 'v[0-9]*' 2>/dev/null || true)"
RANGE="${LAST_TAG:+$LAST_TAG..}HEAD"
echo "==> checking changelog coverage over ${LAST_TAG:-the whole history}..HEAD"

RENDERED="$(git-cliff --unreleased 2>/dev/null || true)"
[ -n "$RENDERED" ] || { echo "ERROR: git-cliff produced nothing" >&2; exit 1; }

missing=0
while read -r sha subject; do
  [ -n "$sha" ] || continue

  # cliff.toml deliberately skips release chores: a release commit must not
  # appear in the changelog it is generating. The script has to know about
  # that, or it reports the release's own merge as dropped and fails the
  # release that created it.
  #
  # Only this one skip rule is exempted, and the others are deliberately not:
  #
  #   "^Merge "            is NOT exempt. The preprocessor lifts the
  #                        conventional line out of the body first, so those
  #                        DO render, and #104 vanishing that way is the
  #                        reason this script exists.
  #   "^Pull from upstream" has never appeared on the spine in this range; if
  #                        it does, a human should look rather than have it
  #                        silently waved through.
  case "$subject" in
    'chore(release)'*) continue ;;
  esac
  # Upstream syncs and this fork's own merges both carry (#N); anything without
  # a number cannot be matched and is reported for a human to look at.
  pr="$(printf '%s' "$subject" | grep -oE '\(#[0-9]+\)$' | tr -d '(#)' || true)"
  if [ -z "$pr" ]; then
    pr="$(printf '%s' "$subject" | grep -oE '^Merge pull request #[0-9]+' | grep -oE '[0-9]+' || true)"
  fi
  if [ -z "$pr" ]; then
    # No usable number in the subject. This used to be reported as
    # UNMATCHABLE and counted as a failure, on the reasoning that a merge
    # without a number cannot be linked and so cannot be in the changelog.
    #
    # That is no longer true. #350 reached main with no number and #233 with
    # "(#198, #231)", which are the issues it corrects rather than its own
    # pull request, and cliff.toml now recovers both by subject. So the
    # question is not whether a number is present but whether the entry was
    # rendered, which is what this script claims to check.
    #
    # Verify by the description text git-cliff renders: everything after the
    # conventional prefix, with any trailing "(#...)" dropped, since a
    # recovery rule may rewrite that part.
    desc="${subject#*: }"
    desc="${desc%% (#*}"
    if [ -n "$desc" ] && printf '%s' "$RENDERED" | grep -qF -- "$desc"; then
      continue
    fi
    echo "  DROPPED      $sha  $subject"
    echo "               no pull request number in the subject, and its text is not in the changelog"
    missing=$((missing+1))
    continue
  fi
  if ! printf '%s' "$RENDERED" | grep -q "pull/${pr})"; then
    echo "  DROPPED      $sha  $subject"
    echo "               #${pr} is not in the generated changelog"
    missing=$((missing+1))
  fi
done < <(git log --first-parent --merges --format='%H %s' ${RANGE})

if [ "$missing" -ne 0 ]; then
  cat >&2 <<'MSG'

ERROR: the changelog omits merges that are on main.

The subject line of a merge commit IS its changelog entry. Fix by either:

  - adding a commit_preprocessors rule in cliff.toml that recovers the subject
    (see the rule for GitHub's default "Merge pull request #N from ..." form), or
  - editing CHANGELOG.md by hand in the release pull request, which is reviewed.

Prevention: merge with
    gh pr merge <n> --merge --subject "<conventional line> (#<n>)" --body ""
The repository is also configured so the web interface defaults the merge
subject to the pull request title, which "Lint PR Title" already enforces.
MSG
  exit 1
fi

count="$(git log --first-parent --merges --format='%H' ${RANGE} | grep -c '' || true)"
echo "    all ${count} merges are represented in the changelog"
