# Release notes reach the release page

Issue: [#531](https://github.com/crtahlin/wasp/issues/531). Follows
[spec.md](spec.md) (#481).
Type: fix.

## Problem

v0.1.5 was published with an empty release body. `release.yaml` built 79 lines
of notes and passed them to goreleaser with `--release-notes`, and none of them
reached the page. The notes were set by hand afterwards.

**Cause, confirmed in goreleaser v2.18.2's source.** The file named by
`--release-notes` is loaded by the changelog step
(`internal/pipe/changelog/changelog.go`, `Run`). That step's `Skip` returns
true when `changelog.disable` is set, so `Run` never executes and the notes stay
empty. #481 set `changelog.disable: true` on the understanding that
`--release-notes` works on its own and that `disable` only stops goreleaser
generating its own list. The source shows the opposite. The run log agrees:
there is no "generating changelog" line in the v0.1.5 run.

**The notes also leave out three things** that
`docs/agent-playbooks/release-process.md` says every release note carries: the
experimental-software warning, the upstream base, and the rollback. The v0.1.5
notes got them by hand.

**Nothing checks the published page.** The job was green with an empty body.

## Design

### 1. Remove `changelog.disable`

When `--release-notes` is given, the same `Run` returns straight after loading
the file, before any generation. So removing the setting cannot bring back
goreleaser's own commit list, which is what #481 wanted to prevent. The comment
in `.goreleaser.yml` is rewritten to say why the setting must stay unset.

### 2. One script builds the notes

`scripts/build-release-notes.sh <tag>` writes the notes to standard output, in
this order:

1. The experimental-software warning, as a quoted paragraph.
2. An "About this release" list with the upstream base read from
   `.upstream-base`, the previous release (the newest `v*.*.*` tag before
   `<tag>`), and the rollback: install the previous release's package with
   `apt-get install --allow-downgrades` and restart. The rollback line tells the
   reader to check the changelog below for a storage-format change, which would
   make going back unsafe.
3. The README highlights block, as today.
4. The link to `DIFFERENCES.md` at `<tag>`, as today.
5. The `CHANGELOG.md` section for the newest version, as today.

`release.yaml` calls the script instead of carrying the extraction inline.
`scripts/check-release-notes.sh`, which runs on every pull request, also runs it
and checks that each of the five parts is present, so a broken marker or heading
still fails a pull request rather than a release.

### 3. The per-pull-request guard rejects `changelog.disable`

`scripts/check-release-notes.sh` fails when `.goreleaser.yml` sets
`changelog.disable` to anything but false. This is the setting that emptied
v0.1.5, and it looks harmless.

### 4. The release job reads the page back

A step after goreleaser reads the published body with `gh release view` and
fails the job unless it contains the warning and the changelog heading for this
version. As with the deb upgrade check, the release is already out when this
runs; a red job is the signal to fix the page by hand.

## Not changed

- What the README highlights block and the changelog say.
- `prepare-release.yml`, the tag, the artifacts and their names.

## Protocol impact

None. Only the release workflow, one goreleaser setting and two scripts change.

## Tests

- `scripts/check-release-notes.sh` passes on the tree, and fails for each of:
  `changelog.disable: true` in `.goreleaser.yml`; a missing highlights
  marker; a first `## ` heading that is not a version (the existing checks).
- `scripts/build-release-notes.sh v0.1.5` run on the tree produces notes with
  all five parts, the upstream base `v2.8.2` and the previous release `v0.1.4`.
- Mutations: put `changelog.disable: true` back; drop the warning from the
  script; drop the read-back step's check of the heading.

## Measurement

The next release, v0.1.6. Its page carries the notes without anything set by
hand, and the read-back step passes. A negative result is an empty or partial
page, or a read-back step that passes against one.

## Rollback

Revert the merge. The next release would then need its notes set by hand, as
v0.1.5 did.

## Files

- `.goreleaser.yml`
- `.github/workflows/release.yaml`
- `scripts/build-release-notes.sh` (new)
- `scripts/check-release-notes.sh`
- `docs/agent-playbooks/release-process.md`: the notes now carry the warning,
  base and rollback automatically; read the page back after a release.

Generated with help of AI.
