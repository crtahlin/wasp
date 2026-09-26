# Experiment lifecycle

Every feature, optimization, and fork-local fix follows this path. There are no
shortcuts for small changes, a small change that skips the spec is how the
repository stops being useful to anyone but its author.

## 1. Issue

File with the **Experiment** issue template, **unassigned**. Assign yourself
only when you actually start work, so the assignee list means "being worked on"
rather than "someone's idea once".

Required labels: one **type** (`experiment`, `optimization`, `fix`, `docs`,
`chore`) and one **priority** (`p0`, `p1`, `p2`). Add `area/*` and `source/*`
where they apply. Verify they actually stuck, `--label` on `gh issue create`
silently drops labels that do not exist yet:

```bash
gh issue view <n> --repo crtahlin/wasp --json labels
```

The body must let a reader decide without redoing the investigation: the
observed problem, the hypothesis, the expected effect **and how it will be
measured**, an initial protocol-impact assessment, and a rough effort estimate.
An idea with no stated way to measure it is filed with `needs-spec` and kept out
of the current milestone until that is answered.

## 2. Spec, merged before implementation

```
docs/experiments/<slug>/spec.md
```

Write it on its own branch and merge it before writing implementation code. A
spec that does not survive its own review is an experiment that should not be
built yet.

`spec.md` sections, all mandatory:

| Section | Contents |
|---|---|
| Problem | What is wrong today, with evidence: logs, measurements, upstream issue links |
| Hypothesis | What you believe is happening and why the proposed change addresses it |
| Design | What changes, which packages, which interfaces |
| Protocol impact | Explicitly: does this touch the frozen surface? If no, say why not. If yes, what breaks and for whom |
| Measurement | How the effect will be demonstrated; what a negative result would look like |
| Rollout and rollback | How an operator turns it on, and how they get back to stock behaviour |
| Upstream portability | What Ethersphere would need in order to adopt this. This is what makes the work reusable |
| Configuration | If the change tunes a constant: the flag name, its default (the current value), and what raising **and lowering** it costs, including what it costs *other nodes*, where that applies. See rule 8 in `AGENTS.md` |

`measurement.md` and `results.md` join it later, `measurement.md` when the
method is fixed, `results.md` once the change has run on a real node.

## 3. Branch

```bash
git switch main && git pull
git switch -c exp/<issue>-<slug>      # or fix/<issue>-<slug>
```

Branches are never deleted. The name is permanent and will be referenced from
the ledger, so make the slug descriptive.

## 4. Implement

Tests alongside the code, documentation in the same branch, in the same pull
request. Not "tests to follow".

Before every push:

```bash
make format && make build && make test && make lint && make protocol-freeze
```

`make protocol-freeze` is the same check CI runs. Running it locally means a
wire-surface change is something you decided to make, rather than something a
red check tells you about after the fact.

### Checks that failed in practice, and what to do instead

Each item below cost at least one wasted CI round or a false result on
2026-09-25 and 2026-09-26 (#498, #499, #500, #511). Do them before the first
push, not after a red check.

- **Format only the files you changed**, then run `git status`. On some
  machines `make format` also rewrites dozens of unrelated files, and the
  formatter can regroup imports in untouched lines of files you did edit.
  Revert anything you did not mean to change before committing.
- **Read the lint output before pushing.** `golangci-lint run` on the changed
  packages; a single `gofmt` finding fails the Lint check.
- **Commit types are limited** by `commitlint.config.js`: `build`, `chore`,
  `ci`, `docs`, `feat`, `fix`, `perf`, `refactor`, `revert`, `test`. `style` is
  not one of them. Check the subject before pushing; rewording afterwards means
  a force push of the branch.
- **Test addresses must count as public** where the code checks
  `manet.IsPublicAddr`. The documentation ranges `203.0.113.0/24`,
  `198.51.100.0/24` and `2001:db8::/32` are **not** public to libp2p, so a test
  using them silently exercises the private-address branch. Use for example
  `1.2.3.4`, `5.6.7.8` and `2a00:1450::/32`, as the existing tests do.
- **Run the race detector on the packages you touched**, and compare a failure
  with `main` before blaming your change: some packages race inside
  dependencies on some networks (for example go-libp2p's NAT-PMP code where the
  router answers NAT-PMP).
- **A timing-sensitive test that fails once proves nothing either way.** Run it
  at least 15 times on your branch and on `main` with a compiled test binary
  (`go test -c`, then `-test.run`) and compare the failure counts.

### Mutation checks that actually prove something

- **A mutation removes the behaviour**, not just its return value. Replacing a
  `return x` while leaving the wait or the call in place is not a mutation of
  the wait.
- **A mutation must compile.** An unused variable or an impossible type
  assertion fails the build, which your harness may report as "killed" or as
  "survived". Read the output for each mutation; do not trust a summary.
- **A test caught only by a hang is a weak test.** Give it its own deadline so
  the mutation fails with a message, and release anything it started so the
  test server can shut down.
- **When a mutation survives, find out why before adding a test.** Twice the
  cause was the test double, not the code: a fake that finished instantly hid a
  missing wait, and a wrapper host hid the interface the new path needed.

Upstream's conventions apply in full, `package foo_test` tests, `t.Parallel()`
only where safe, errors wrapped with `%w`, no casual `go.mod` changes. See the
upstream half of `AGENTS.md`, plus `CODING.md` and `CODINGSTYLE.md`.

New fork-authored files take the fork copyright header:

```go
// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.
```

Files inherited from upstream keep their existing headers. Do not relabel them.

## 5. Pull request

```bash
gh pr create --repo crtahlin/wasp --base main \
  --title "feat(scope): what changed" --body-file <file>
```

The title is linted as a conventional commit. The template's checklist includes
the protocol-compatibility question, answer it honestly rather than ticking it.

**Do not write `closes #N`, `fixes #N` or `resolves #N` in the body unless this
pull request really finishes that issue.** GitHub treats those words as a
closing keyword wherever they appear and shuts the issue on merge, whatever the
sentence around them says. That is not theoretical: a spec pull request
described the result its experiment expected to report as "it clos[e]s #NNN as a
measured negative", and that issue closed the moment the spec merged, with no
code written and no measurement run. The issue number is replaced above so that
this warning can be quoted without springing the trap it describes.

The trap is specific to this process, because here an issue stays open across
three merges: its spec, its code, and its results. Only the last of those should
carry the keyword. In prose about a future outcome write **settles**, **answers**
or **would close**. Check before merging:

```bash
gh pr view <n> --repo crtahlin/wasp --json body --jq .body |
  grep -inE 'clos(e|es|ed) #|fix(es|ed) #|resolv(e|es|ed) #'
git log --format=%B origin/main..HEAD |
  grep -inE 'clos(e|es|ed) #|fix(es|ed) #|resolv(e|es|ed) #'
```

A hit that is not deliberate is a reword, not a debate.

**Check before the first push, not before the merge.** Amending the commit and
force-pushing does not undo it: GitHub records the reference when the commit
arrives, and applies it when the pull request merges, even though the amended
commit is what ends up on `main`. That is not a guess. The commit adding this
very warning quoted the offending phrase, was amended and force-pushed within
minutes, merged cleanly with a sanitised message, and closed the issue anyway
from the commit that no longer exists in the history.

If it has already happened, reopening the issue is the whole fix; nothing needs
reverting.

## 6. Merge, then record

```bash
gh pr merge <n> --merge --subject "feat(scope): what changed (#<n>)"
```

The subject **is** the changelog entry. Squash and rebase merging are disabled
at the repository level, so the only thing that can go wrong here is a lazy
subject line.

Then, on `main`:

```bash
git switch main && git pull
git tag -a "exp-<slug>" -m "<one line: what this experiment does>"
git push origin "exp-<slug>"
```

The `exp-*` tag is the durability backstop: it survives even if the branch
pointer is ever lost, and `scripts/export-patch.sh` resolves experiments through
it. It can never be mistaken for a release tag: the Makefile matches `v[0-9]*`
and experiment tags contain no slash.

It **will** be mistaken for a version by goreleaser in snapshot mode, which
`ignore_tags` does not filter. That is handled in `.goreleaser.yml`, where the
snapshot version template derives from nothing at all rather than from
`{{.Tag}}`. Do not change it back: with an `exp-*` tag on almost every merge,
`{{.Tag}}` names a snapshot after whichever experiment `git describe` happens to
reach.

This step is easy to skip and was skipped for twenty-five merges, which left
`export-patch.sh` able to resolve two experiments out of twenty-seven. If you
are reading this while writing a merge, do it now.

Finally, add the row to `docs/experiments/INDEX.md`. If the merge changes what
a node does compared with Bee, the pull request should already have updated
`docs/DIFFERENCES.md` (rule 13 in `AGENTS.md`). If it did not, do it now.

## 7. Validate on a real node

Merged is not the same as proven. Deploy to the bench, confirm the node connects
to stock peers and stays healthy, and write `results.md`. See
`docs/agent-playbooks/test-bench.md`. A negative result is a genuine outcome,
record it and say so in the ledger rather than quietly abandoning the branch.

## Extracting an experiment for upstream

If someone wants to take an experiment to Ethersphere, or analyse it in
isolation:

```bash
scripts/export-patch.sh <slug>
```

This replays the experiment's own commits onto **current** upstream master and
writes a clean patch series to `dist/patches/<slug>/`. It works no matter how
many upstream syncs have happened since the merge, because the `--no-ff` merge
preserved the exact commit range.
