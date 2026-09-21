# Spec: give TestCrashRecovery two buckets that are actually different

Issue: [#387](https://github.com/crtahlin/wasp/issues/387). Type: fix. Area: postage.
Affects upstream: yes (the test is unmodified from bee v2.8.2).

## Problem

`TestCrashRecovery` in `pkg/postage/service_test.go` picks two random chunk addresses
and then uses their bucket indices as though they were distinct:

```go
chunkAddr0 := swarm.RandAddress(t)
chunkAddr1 := swarm.RandAddress(t)
bIdx0 := postage.ToBucket(issuer.BucketDepth(), chunkAddr0)
bIdx1 := postage.ToBucket(issuer.BucketDepth(), chunkAddr1)
```

A **collision bucket** is one of the slots a stamp issuer counts stamps into, chosen
from the leading bits of the chunk address. The issuer is built by `newTestStampIssuer`
with a bucket depth of 8, so there are 2^8 = 256 buckets and two uniformly random
addresses share one with probability **1 in 256**.

`bIdx0` is written at collision count 2 and must recover to 3; `bIdx1` is written at
collision count 0 and must recover to 1. When the two collide, both writes land in the
same bucket, it recovers to 3, and the `bIdx1` assertion fails.

This is an **intermittent failure**: the test passes or fails on identical code
depending only on which addresses it happened to draw.

Observed on CI for [#386](https://github.com/crtahlin/wasp/pull/386), the Ubuntu race
job:

```
--- FAIL: TestCrashRecovery (0.01s)
    service_test.go:386: bucket 242: want 1, got 3
    service_test.go:412: post-recovery clean restart: bucket 242: want 1, got 3
```

Bucket 242 is reported for the `bIdx1` assertion holding the value `bIdx0` should have,
which is the collision rather than a recovery defect. The branch it failed on touched
none of this code.

## Cost of leaving it

No node behaviour is affected: it is a test. What it costs is a CI failure on an
unrelated pull request.

How often depends on how many times the test runs, and **it runs on all three
platforms**: the matrix in `.github/workflows/go.yml` is
`[ubuntu-latest, macos-latest, windows-latest]`, Ubuntu and macOS run `make test-ci-race`
and Windows runs `make test-ci`, and both of those are `go test ./...`, which includes
`pkg/postage`. The failure is not specific to the race detector. That #386 passed on
Windows is one draw coming out the other way, not evidence that Windows is exempt.

So a single CI run fails with probability 1 - (255/256)^3 = 0.0117, about **1 run in
86**. The workflow also fires on push to `main`, so a code change that is merged is
executed about six times and fails somewhere with probability 1 - (255/256)^6 = 0.0232,
about **1 merged pull request in 43**. Documentation-only changes skip the test step, so
they do not count.

Against that there is the time spent establishing that the failure is not yours, and the
habit it teaches of re-running CI without reading it, which is worse than the
intermittent failure itself.

## The change

Draw the pair together, so that their buckets differ by construction rather than by
luck. The helper returns both addresses:

```go
func twoAddressesInDifferentBuckets(t *testing.T, bucketDepth uint8) (swarm.Address, swarm.Address)
```

and `TestCrashRecovery` calls it in place of its two `swarm.RandAddress(t)` calls.

**Bounded, not an unbounded loop.** With 256 buckets a single draw already succeeds 255
times in 256, so needing more than a few is unlikely. But a test that can hang is worse
than a test that can fail, and a bounded loop states what it assumes. The bound is 64,
which is far past the point where exhausting it could be chance: all 64 colliding has
probability 256^-64, about 1 in 10^154. Reaching the bound means address generation is
broken, and saying so is more useful than looping until the test times out.

**Both addresses are returned, rather than one plus a bucket index to avoid.** The
obvious shape is a helper taking the first address's bucket and avoiding it. That shape
lets the caller pass the wrong index, or an index computed at a different bucket depth,
and neither slip is visible: each one simply restores the 1 in 256 failure the helper
exists to remove. Returning the pair removes the first of those two mistakes entirely,
because there is no index to get wrong.

**`TestCrashRecovery` states the precondition itself**, with one assertion that the two
bucket indices differ. This does not make a broken helper any more likely to be caught,
and it is not there for that. It is there so that the residual failure reports
`precondition: both addresses landed in bucket 186` instead of
`bucket 186: want 1, got 3`, which is the message that made this look like a recovery
defect in the first place.

**Distinctness is a precondition of the subject, not part of it.** The test is about
crash recovery of bucket counts. Establishing that the two buckets differ, rather than
assuming it, loses no coverage: every assertion that ran before still runs, on two
buckets that are now guaranteed to be the two the assertions describe.

## What this does not do

It does not make the test deterministic. The addresses are still random, so the test
still exercises arbitrary buckets rather than fixed ones, which is the coverage worth
keeping. Only the collision, which the test was never written to handle, is excluded.

It does not touch the recovery code, `postage.Service` or the issuer. The defect is in
the test's premise.

## Verification

- `go test ./pkg/postage/ -count=2000` passes, with and without `-race`. **2000, not
  500.** At 1 in 256 the unmodified test survives 500 runs 14 percent of the time, so a
  500-run check gives the wrong answer about one time in seven. At 2000 it survives 0.04
  percent of the time.
- Mutations, each run over the **whole package** with `-run .`, never a name filter,
  since a filter has twice produced a false "not caught" result in this repository:
  - restoring the two independent random addresses must fail;
  - asking the helper for a different bucket depth than the assertions use must fail,
    and must report the precondition;
  - dropping the bucket comparison inside the helper, making the helper return the same
    address twice, and cutting the bound to one try must each fail on the first run.
- Removing the precondition assertion is **expected not to fail anything**, because it
  is a diagnostic rather than a guard. What justifies keeping it is the message it
  produces under the wrong-depth mutation, and that is what the results must show.

## Scope

`pkg/postage/service_test.go` only: the two lines inside `TestCrashRecovery` that draw
the addresses, one precondition assertion, and a helper with its own test appended at
the end of the file. Appended rather than inserted, because the file is otherwise
byte-identical to upstream and a block at the end gives the next upstream sync a smaller
conflict surface.

No production code, no configuration, no wire change. Nothing an operator can observe
changes, so `docs/DIFFERENCES.md` gains no row; `docs/UPSTREAM.md` already carries a row
for #387 and needs its status, branch and merge commit filled in at merge time, per
rule 14.

**Upstream check.** `pkg/postage/service_test.go` is byte-identical to bee v2.8.2. The
command that shows it needs two revisions, since a one-revision `git diff` compares
against the working tree and so reports this branch's own change:

```bash
git diff upstream/v2.8.2 origin/main -- pkg/postage/service_test.go
```
