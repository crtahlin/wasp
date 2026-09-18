# The per-peer provider payment threshold, measured

Issue: [#327](https://github.com/crtahlin/wasp/issues/327). Spec:
[per-peer-threshold.md](per-peer-threshold.md). Implementation merged as
`d94c6747`.

Measured 2026-09-18 on the two-node bench, `bench-1` as the provider and
`bench-2` as the requester. Harness `cp290/t13.sh`, outside this repository.
Sole-source content from local ingest
([#326](https://github.com/crtahlin/wasp/issues/326)), 20,000,000 bytes at
redundancy level NONE, lookahead buffer 0.

**The feature does what it was built to do, and it does not fix what it was
hoped to fix.** The grant fires and raises a fresh peer's threshold from
13,500,000 to 108,000,000 on its first provider request. The download still
truncates.

## What the spec assumed, and what is actually true

The spec was written as though a peer's announced threshold sits at the
configured default until something raises it. It does not. `Connect` resets it
to the configured value (`accounting.go:1453`), and from there
`notifyPaymentThresholdUpgrade` raises it by one refresh rate every time
cumulative settlement passes a checkpoint. On this bench pair it had reached
94,500,000 organically, and `providerGrantDelta` declines to grant anything to a
peer growth has already carried above the configured value.

So the feature is not worth eight times more credit to an established peer. **It
is worth giving a fresh peer immediately what growth takes billions of units of
settlement to earn.** That is what this measures, and it is why both arms begin
with a provider restart: a restart is what puts the pair back to 13,500,000, and
it makes the two arms start from the same state.

Each arm gated on the requester reading exactly 13,500,000 before any download.
Both passed.

## The arms

| Arm | Run | Threshold before | Balance before | Delivered of 20,000,000 | Rate |
|---|---|---|---|---|---|
| Control | 1 | 13,500,000 | 0 | 360,448 | 81,642 B/s |
| Control | 2 | 13,500,000 | -13,270,000 | 1,212,416 | 57,551 B/s |
| Control | 3 | 13,500,000 | -10,470,000 | 262,144 | 57,294 B/s |
| Granted | 1 | 13,500,000 | 0 | **2,293,760** | 193,034 B/s |
| Granted | 2 | 108,000,000 | -104,000,000 | 1,146,880 | 186,908 B/s |
| Granted | 3 | 108,000,000 | -105,400,000 | 1,212,416 | 151,699 B/s |

No run completed. Every SHA-256 mismatched, every curl exit was 18.

## What is established

**The grant fires, once, for exactly its own size.** The provider's own metrics
after the run:

```
bee_accounting_provider_grants          1
bee_accounting_provider_budget_used     94,500,000
bee_accounting_provider_grants_refused  0
```

94,500,000 is precisely `108,000,000 - 13,500,000`, one grant and no more. The
requester's view moved with it: granted run 1 began at 13,500,000 and the
reading taken immediately after it was 108,000,000. The threshold was raised by
the first provider request of the first download, which is the design.

**The credit is then genuinely used.** The granted arm's balance ran to
-104,000,000, -105,400,000 and finally -107,780,000, against a ceiling of
108,000,000. The control never passed -13,270,000 against 13,500,000. Both arms
ran their balances up to their respective limits.

**The control's threshold did not grow during its arm**, staying at 13,500,000
across all three runs. A truncated download settles too little to reach a
450,000,000 checkpoint. This is better than the pre-registered plan expected,
because it means organic growth cannot have contaminated the comparison.

## What is not established

**The pre-registered prediction failed, and that is the useful part.** The plan
said 20 MB should complete at 108,000,000 and truncate at 13,500,000, on the
reasoning that the earlier size sweep completed 20 MB at a 94,500,000 threshold.
It truncated at both. So headroom is not what decides whether a sole-source
download of this size completes, and the reasoning that predicted it was wrong.

That agrees with [truncation-cause.md](truncation-cause.md), measured the same
day: a download stops when one chunk is asked of about thirty-four peers that do
not hold it, having stopped asking the one that does, and exhausts its retry
budget. More credit does not prevent a chunk from losing its holder.

**The only clean comparison is a single pair of runs.** Granted run 1 and
control run 1 both began at a balance of zero and a threshold of 13,500,000, so
only they are matched. 2,293,760 against 360,448 is 6.4 times more delivered,
and it is **n=1**, against rule 7's three runs per condition. Granted runs 2 and
3 began already at their ceiling and are not comparable with anything.

Making this a real figure needs a restart before every run so each starts fresh.
That is the obvious next measurement and it is cheap.

**Nothing here says what the grant is worth to a peer that would have grown
anyway.** It shortens the wait; over a long enough relationship the organic path
reaches the same place. Whether shortening it matters depends on how long
providers and requesters actually stay connected, which nothing in this project
has measured.

**One provider, one requester, one file size, one session.**

## What follows

- **The feature is correct and its motivation is narrower than the spec claimed.**
  It should be documented as helping a newly connected peer, not as raising
  credit generally, and [`DIFFERENCES.md`](../../DIFFERENCES.md) should say so
  when it is next revised.
- **It is not a fix for truncation**, and the spec should not have implied a
  download would complete. The next work on completion belongs to
  [#343](https://github.com/crtahlin/wasp/issues/343), where the cause now has
  evidence behind it.
- **The clean n=3 comparison is worth doing**, with a restart before every run.

---

Generated with help of AI.
