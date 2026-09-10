# Results: the probe-based sample is dramatically cheaper, and scales with k

Issue: [#241](https://github.com/crtahlin/wasp/issues/241). Spec: [`spec.md`](spec.md).
Umbrella: [#234](https://github.com/crtahlin/wasp/issues/234). Follows the
reserve-size-independent sampling study
([#235](https://github.com/crtahlin/wasp/issues/235)).

**Verdict: the probe-based sample is sublinear as predicted and far cheaper than the
full sample. On bench-1, over a 3.77M-chunk reserve, the full sample takes about 28.5s;
the probe sample takes about 170 to 200 microseconds per probe warm (730 to 920
microseconds cold), so at a small k it is hundreds to thousands of times faster, and it
still wins at every k tested. This settles the cost question the estimate in #235 could
not. It does not settle the right value of k, which is an accuracy-and-security question
that only makes sense at the protocol level.** The measurement was run against a binary
whose version was verified and logged before measuring (see the note at the end), after
an earlier run silently measured the wrong binary.

## Setup

bench-1, leveldb, mainnet unstaked, reserve 3,770,854 chunks within radius at committed
depth 9. Running version `probe241-e15a96de-dev`, confirmed from `/health` and logged in
the run before any measurement; `/probesample` confirmed returning 200. Baseline is the
full-sample `/rchash`; the probe is `/probesample/{depth}/{overlay}/{k}`. Warm, and cold
with the OS page cache dropped before each run. Three runs per condition.

Full sample (`rchash`) baseline: 33.1 / 28.7 / 28.1s, about **28.5s** settled.

## The measurement

**Table: probe sample cost and speedup versus the full sample, bench-1, reserve 3.77M
within radius, three runs; per-probe is wall time divided by k**

| k | warm | cold | speedup warm | speedup cold | per-probe warm | per-probe cold |
|---|---|---|---|---|---|---|
| 16 | 0.0027s | 0.015s | ~10,500x | ~1,900x | 169 us | 920 us |
| 64 | 0.008s | 0.049s | ~3,400x | ~580x | 130 us | 770 us |
| 256 | 0.054s | 0.334s | ~530x | ~85x | 210 us | 1.3 ms |
| 1024 | 0.196s | 1.01s | ~145x | ~28x | 190 us | 990 us |
| 4096 | 0.80s | 3.41s | ~36x | ~8.4x | 196 us | 833 us |
| 16384 | 3.27s | 11.95s | ~8.7x | ~2.4x | 200 us | 730 us |

Every probe found a chunk (hits = k, no misses, no load failures), so the seek and
nearest-in-address-order logic works over the real reserve.

## What it shows

- **The sublinear claim holds.** Cost scales cleanly as O(k): a flat 170 to 200
  microseconds per probe warm, 730 to 920 microseconds cold, across three orders of
  magnitude of k, against the full sample's fixed 28.5s over 3.77M chunks. At a small k
  (16 to 256, the range comparable to today's 16-order-statistic accuracy) the probe is
  hundreds to over ten thousand times faster warm.
- **No crossover in the tested range.** Even the largest k measured, 16384, beats the
  full sample: 8.7x warm, 2.4x cold. The probe wins throughout, so there is headroom to
  choose a generous k for accuracy and still be far ahead.
- **Disk dominates, not hashing.** The cost is the index seek (locate) plus the chunk
  read (load); the transformed-address hash (taddr) is a small fraction. At k=16384 warm
  the split is about 1.1s locate, 1.0s load, 1.15s hash; cold the load grows to about
  6.1s while hashing stays flat, which is the random-read penalty below.
- **The random-read penalty is real but does not erase the win.** Each probe is a random
  index seek and a random chunk read, so cold is about 4 to 5 times slower per probe than
  warm. On a genuinely slow-disk weak machine the cold per-probe cost would rise further,
  but it is still k reads against N.
- **Resource use is modest and scales with k.** Allocations run from about 0.5 MB at
  k=16 to about 377 MB at k=16384; garbage collection barely triggers (0 cycles below
  k=4096, 2 at k=16384).

Put plainly: a weak node that spends about 30s on the full sample could, with an
equivalent-accuracy probe sample, finish in single-digit milliseconds warm or tens of
milliseconds cold.

## Choosing k, and what is still open

This experiment measures cost for a given k. It does not decide the right k, and does not
touch the estimator's accuracy or its security. Those are the harder, protocol-level
questions:

- **Accuracy sets k, by the same method the current scheme uses.** The Swarm spec
  Appendix C picks the current k=16 by minimizing recall-plus-precision error against a
  target reserve-size estimate. The probe scheme would be calibrated the same way, but it
  is a different statistic: the current scheme uses the k smallest order statistics
  (Erlang distributed), the probe scheme uses k nearest-neighbour distances (exponentially
  distributed with rate proportional to reserve size). That derivation and calibration
  have not been done, so no rigorous k can be quoted. Both estimators have relative error
  on the order of 1/sqrt(k), which suggests the probe k for equivalent accuracy is in the
  same ballpark as today's 16, on the order of tens; and the cost above shows that even a
  very generous k stays far cheaper than the full sample.
- **Security is the larger open question.** The order-statistic sample is unforgeable
  because a node must hold the whole set to have the true smallest 16. The probe scheme as
  measured proves "a held chunk within distance d of the probe", not "the nearest", so the
  gap has to be analysed for ways a node with a sparse, well-placed reserve could pass.
  Probes are unpredictable until the round (the anchor is post-commit), which is the reason
  to expect it holds, but it must be proven.

Both are Swarm protocol work, not a fork change ([#235](https://github.com/crtahlin/wasp/issues/235)):
the sampling rule is shared with every node and the on-chain contract. This experiment is
a benchmark only; the `/probesample` endpoint is not wired into the redistribution game.

## Method note

An earlier run of this experiment measured the wrong binary: the deploy used `cp` to
overwrite `/usr/bin/bee`, which fails with "text file busy" on a running executable and
silently leaves the old binary in place, and the run did not check the version. The
corrected run stops the service before copying, builds the test binary with a version
marker, and verifies and logs the running version before measuring, aborting on a
mismatch. The numbers above are from a run that logged `probe241-e15a96de-dev` as the
running version. The same failure affected the earlier sampler-BMT measurement
([#236](https://github.com/crtahlin/wasp/issues/236)), which is being re-run.

Generated with help of AI.
