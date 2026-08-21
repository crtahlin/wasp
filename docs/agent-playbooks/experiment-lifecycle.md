# Experiment lifecycle

Every feature, optimization, and fork-local fix follows this path. There are no
shortcuts for small changes — a small change that skips the spec is how the
repository stops being useful to anyone but its author.

## 1. Issue

File with the **Experiment** issue template, **unassigned**. Assign yourself
only when you actually start work, so the assignee list means "being worked on"
rather than "someone's idea once".

Required labels: one **type** (`experiment`, `optimization`, `fix`, `docs`,
`chore`) and one **priority** (`p0`, `p1`, `p2`). Add `area/*` and `source/*`
where they apply. Verify they actually stuck — `--label` on `gh issue create`
silently drops labels that do not exist yet:

```bash
gh issue view <n> --repo crtahlin/bee-experimental --json labels
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
| Problem | What is wrong today, with evidence — logs, measurements, upstream issue links |
| Hypothesis | What you believe is happening and why the proposed change addresses it |
| Design | What changes, which packages, which interfaces |
| Protocol impact | Explicitly: does this touch the frozen surface? If no, say why not. If yes, what breaks and for whom |
| Measurement | How the effect will be demonstrated; what a negative result would look like |
| Rollout and rollback | How an operator turns it on, and how they get back to stock behaviour |
| Upstream portability | What Ethersphere would need in order to adopt this. This is what makes the work reusable |

`measurement.md` and `results.md` join it later — `measurement.md` when the
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
make format && make build && make test && make lint
```

Upstream's conventions apply in full — `package foo_test` tests, `t.Parallel()`
only where safe, errors wrapped with `%w`, no casual `go.mod` changes. See the
upstream half of `AGENTS.md`, plus `CODING.md` and `CODINGSTYLE.md`.

New fork-authored files take the fork copyright header:

```go
// Copyright 2026 The bee-experimental Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.
```

Files inherited from upstream keep their existing headers. Do not relabel them.

## 5. Pull request

```bash
gh pr create --repo crtahlin/bee-experimental --base main \
  --title "feat(scope): what changed" --body-file <file>
```

The title is linted as a conventional commit. The template's checklist includes
the protocol-compatibility question — answer it honestly rather than ticking it.

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
git tag -a "exp/<slug>" -m "<one line: what this experiment does>"
git push origin "exp/<slug>"
```

The `exp/*` tag is the durability backstop — it survives even if the branch
pointer is ever lost, and `scripts/export-patch.sh` resolves experiments through
it. It can never be mistaken for a release tag: the Makefile matches `v[0-9]*`
and `.goreleaser.yml` ignores `exp/*`.

Finally, add the row to `docs/experiments/INDEX.md`.

## 7. Validate on a real node

Merged is not the same as proven. Deploy to the bench, confirm the node connects
to stock peers and stays healthy, and write `results.md`. See
`docs/agent-playbooks/test-bench.md`. A negative result is a genuine outcome —
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
