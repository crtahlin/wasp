# A release should say what the build does

Issue: [#481](https://github.com/crtahlin/wasp/issues/481)

## Problem

Three faults, in order of how badly they mislead a reader.

**The release page does not show the curated changelog.** `.goreleaser.yml` has
no `changelog:` section, so goreleaser uses its default, every commit between
tags. That walks all parents, so each change appears twice, once as its merge
and once as the branch commit behind it:

```
* 68f8a2ea chore(release): v0.1.4 (#478)
* c93b25cb chore(release): v0.1.4
```

`CHANGELOG.md`, built from the first-parent spine and committed by
`prepare-release.yml` minutes earlier, never reaches the page. Rule 3 exists to
make that changelog possible, and the result is then discarded where a reader
looks.

**The curated changelog does not say what the build does either.** For v0.1.4 it
is 202 one-line entries under seven headings. Every line is true and the list is
complete. It is a commit log: it says what changed, not what a node now does
differently from Bee. The entries an operator cares about sit among test and
documentation entries with equal weight.

**The README never summarises the differences.** It links to `DIFFERENCES.md`
and covers the fork's relationship to upstream, packaging and rollback. What a
wasp node actually does differently appears nowhere in it.

## The change

**One canonical summary, held in `README.md`, consumed by the release.**

`README.md` gains a `## What wasp changes` section between two HTML comment
markers:

```html
<!-- highlights:start -->
...
<!-- highlights:end -->
```

The release workflow extracts the text between those markers and puts it at the
top of the release notes, above the changelog for that version.

Markdown has no include directive, so the alternatives were a separate file that
the README links to rather than contains, which fails the requirement that the
README carry it, or writing the text twice, which drifts. Extraction keeps one
copy and puts it in both places. The markers are HTML comments, so they render
as nothing on GitHub.

**Release notes are built from `CHANGELOG.md`, not regenerated.** The workflow
takes the section for the version being released, from its `## [` heading to the
next one. `CHANGELOG.md` is already correct at that commit, because
`prepare-release.yml` wrote it and the release is cut from that merge. Using it
avoids installing git-cliff in a second workflow and avoids the two ever
disagreeing.

`changelog: disable: true` goes in `.goreleaser.yml`. Passing `--release-notes`
already suppresses generation; the setting states the intent and stops the
default dump returning if the flag is dropped.

**What the summary contains.** Themes, not entries, each naming what a node does
rather than what was edited, with a pointer to `DIFFERENCES.md` for the full
list. It is a summary and is allowed to be incomplete; `DIFFERENCES.md` is the
complete record and rule 13 keeps it current.

## Protocol impact

None. Documentation and release packaging only. No Go code changes, nothing
under `pkg/`, and `.github/protocol-freeze.lock` is untouched.

## What this does not change

`DIFFERENCES.md` keeps its content and structure. This gives it an entry point
rather than replacing it. `CHANGELOG.md` and `cliff.toml` are unchanged: the
per-merge list stays, it just stops being the only thing a reader sees.

## Tests

This is documentation and workflow configuration, so the checks are not Go
tests.

1. A script asserts the README markers exist, appear once each, in order, and
   with a non-empty body between them. It runs in CI on every pull request, so
   deleting or renaming a marker fails there rather than silently emptying the
   next release's notes.
2. The same script asserts the extracted section is non-empty for the current
   `CHANGELOG.md`, using the same extraction the workflow uses, so a changed
   heading format is caught before a release rather than during one.

**The mutation that must fail each**: removing `<!-- highlights:end -->` must
fail check 1; changing the `## [` heading pattern in `CHANGELOG.md` must fail
check 2. A check that passes with the markers gone would be worse than none,
because it would certify empty release notes.

## Verification

Run the extraction locally against the committed `CHANGELOG.md` and the edited
`README.md` and read the result. The real confirmation is the next release: its
page opens with the summary and carries the curated per-version changelog with
no duplicated entries. v0.1.4 is already published and is not rewritten.

## Files

- `README.md`, the new section and its markers
- `.goreleaser.yml`, `changelog: disable: true`
- `.github/workflows/release.yaml`, a step building the notes and the
  `--release-notes` argument
- `scripts/check-release-notes.sh`, the two checks
- `.github/workflows/go.yml` or the existing packaging workflow, to run it

Generated with help of AI.
