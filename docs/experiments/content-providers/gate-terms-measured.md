# The gate terms at the moment of refusal

Issue: [#343](https://github.com/crtahlin/wasp/issues/343). Instrument:
[#353](https://github.com/crtahlin/wasp/issues/353), merged as
[`5e527a22`](https://github.com/crtahlin/wasp/commit/5e527a22). Harness
`t16.sh`, outside this repository. Follows
[overdraft-terms.md](overdraft-terms.md), which established that polling could
not answer this.

This is the measurement four designs and two analyses were withdrawn waiting
for.

## Conditions

Taken 2026-09-18 between 12:53 and 13:03 UTC. Requester
`0.1.3-353-5e527a22`, the merge commit exactly. Provider `0.1.3-f005605d`,
grant asserted zero at run time. SWAP block, both nodes full. Sole-source
content, 4,194,304 bytes, hinted at the provider, `Swarm-Cache: false`.

Six runs, three at `Swarm-Lookahead-Buffer-Size: 0` and three at the shipped
buffer. Every lookahead-0 run returned 196,608 bytes with `curl` exit 18,
matching [overdraft-terms.md](overdraft-terms.md)'s interleaved arm on a
different build and a different session.

`node/accounting` was raised to `all` for each run and restored afterwards. The
V(2) entry was confirmed present in `GET /loggers` immediately after boot,
before any retrieval traffic, which is what building it in `NewAccounting` is
for.

## What the gate saw

**2,381 refusals**, read under the same lock as the comparison. Every one
reconciles exactly against
`increasedExpectedDebt = max(-balance, 0) + reservedBalance + price + surplusBalance`,
with no exceptions, which is the instrument checking itself.

| | at the floor | at the ceiling |
|---|---|---|
| `refresh_due` | 0 | 4,500,000 |
| `overdraft_limit` | 13,500,000 | 18,000,000 |
| refusals | **2,251 (94.5%)** | **130 (5.5%)** |
| `reserved_balance` median | 0 | 4,480,000 |
| `reserved_balance` max | 12,860,000 | 17,980,000 |
| `reserved_balance` above 4,500,000 | 15% | 45% |
| `settled_balance` median | -13,450,000 | -13,230,000 |
| margin over the limit | 10,000 to 300,000 | 30,000 to 300,000 |
| margin below one chunk price | **2,251 of 2,251** | **130 of 130** |

### The answer to the question as asked

**Which term moves: the limit.** In 94.5 per cent of refusals `refreshDue` is
zero, so the gate is at 13,500,000 rather than 18,000,000. Those refusals would
not have occurred at the ceiling.

The timing is exact. In the first run the refreshment completed at
12:53:15.230 and the refusals landed at 12:53:16.15, 0.925 seconds later.
`min(925/1000, 1)` is **0** by integer division, so the whole refresh allowance
was absent while less than a second of wall clock had passed.

**The remaining 5.5 per cent are a different regime.** They sit at the ceiling
with a median `reserved_balance` of 4,480,000 and a maximum of 17,980,000, so
concurrency alone carries them past a limit that already included the full
allowance.

### The one number that unifies both

**Every one of the 2,381 refusals missed by less than a single chunk price.**
The largest margin is 300,000 against a price of 320,000.

## What this does NOT license, stated before anyone reads a remedy into it

The tempting inference is that a small amount of extra headroom would remove
every refusal here. **The data does not support that**, and the reason is in
the data itself.

The settled debt is a **steady state**, not a variable: its median is
-13,450,000 against a 13,500,000 threshold, which is 99.6 per cent. Retrieval
consumes faster than refreshment restores, so the balance sits against whatever
the limit is. At a limit, every refusal is marginal **by construction**. Raise
the limit and the debt re-equilibrates against the new one.

So "all margins are under one chunk" is close to a tautology of a system
running at its limit, and is not evidence that a one-chunk grant fixes
anything. What is **not** tautological is the 94.5 to 5.5 split: most refusals
happen specifically in the sub-second window where the limit is 4,500,000
lower.

Whether removing the step function helps, or merely moves the steady state, is
a separate question this run cannot answer. It needs its own measurement.

## A sampling error found and corrected inside this run

The harness originally piped the journal through `head -400`. On that sample
the answer looked unambiguous and was wrong:

| | capped at 400 | uncapped |
|---|---|---|
| refusals | 1,282 | 2,381 |
| at the ceiling | **0** | **130** |
| `reserved_balance` max | 12,830,000 | 17,980,000 |

The ceiling refusals occur later in a run, so taking the first 400
chronologically excluded them completely. An interim reading of the capped data
concluded "one mechanism, universal", which the full window refutes.

The cap is removed from `t16.sh`, with the reason recorded there.

This also corrects a prediction registered before the second arm ran: that the
shipped lookahead buffer would show a distinct regime at the ceiling driven by
`reserved_balance`. On the capped sample that looked refuted. On the full
window it is right, at 5.5 per cent of refusals rather than as the dominant
mode.

## And a correction to overdraft-terms.md

That document measured `reservedBalance` peaking at 12,860,000 with the
prefetch on and inferred concurrency was the likely driver. The peak is
confirmed here, at 12,860,000 at the floor and 17,980,000 at the ceiling, but
**the peak and the refusal do not coincide**: the median `reserved_balance` at
a refusal is **zero** in the 94.5 per cent case. A peak sampled at 50 ms is not
the value the gate saw.

That is the specific thing polling could not have told us, and it is why the
instrument was worth building.

## History of this question, since the pattern is the finding

The mechanism now measured was proposed, then refuted, then re-qualified, then
measured:

1. proposed on #343 as a completed refreshment tightening the gate;
2. **retracted** on the grounds that the code forbids it. That retraction was
   itself wrong;
3. restated as possible under stated preconditions and explicitly not measured;
4. measured here, and it accounts for 94.5 per cent of refusals.

Four designs were withdrawn before this, each derived by reading the code
carefully. The first instinct was closer to right than the confident refutation
that followed it. What the discipline bought was not the answer but the refusal
to act on any of the three versions before this run existed.

Generated with help of AI.
