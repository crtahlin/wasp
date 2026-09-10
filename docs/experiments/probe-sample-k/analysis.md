# How many probes k does the probe-based reserve-size estimator need?

Issue: [#245](https://github.com/crtahlin/wasp/issues/245). Umbrella:
[#234](https://github.com/crtahlin/wasp/issues/234). Follows the cost result
([#241](https://github.com/crtahlin/wasp/issues/241)) and the study that proposed the
probe approach ([#235](https://github.com/crtahlin/wasp/issues/235)).
Simulation: [`main.go`](main.go).

**Verdict: the probe estimator needs the same k as the current scheme, on the order of
16, and the choice is set by accuracy the same way the current scheme sets it. Both are
Erlang distributions with the same shape parameter k, so they have the same ability to
tell an honest reserve from a slacker; only a scale constant differs, and that only
moves the acceptance threshold, not the number of probes. At k=16 both give recall error
about 0.098 and precision error about 0.072, matching the Swarm spec. Raising k tightens
both errors fast: k=32 reaches about 0.03/0.02, k=64 about 0.003/0.003. So a practical
range is k in [16, 64]: k=16 for parity with today, k=32 to k=64 for a comfortable
margin, and per [#241](https://github.com/crtahlin/wasp/issues/241) even k=64 is hundreds
of times cheaper than the full sample. This settles the accuracy question
[#241](https://github.com/crtahlin/wasp/issues/241) left open. It does not address the
estimator's security, which is separate, and the whole thing is Swarm protocol design,
not a fork change.**

## What "the right k" means here

A reserve-size proof, called proof of resources, has to let the network tell a node that
honestly holds its share of the neighborhood from one that holds too little. The Swarm
spec ("Future-proof Storage", Appendix C) frames this as a statistical test with two
error rates:

- **recall error alpha**: an honest reserve of the expected size is wrongly judged too
  small.
- **precision error beta**: a slacker holding less than it should is wrongly accepted.

k is chosen to make alpha plus beta small enough at the size ratio the game cares about.
The spec discriminates a reserve of the target size from one twice as large, and reports
that k=16 gives alpha about 0.098 and beta about 0.072 (spec Table 3). The question for
the probe estimator is whether it needs a comparable k, or many more.

## Why a small k suffices: both estimators are Erlang with the same shape

Model the transformed chunk addresses as N points spread uniformly around the address
circle, which is what the anchor-keyed hash produces. Then:

- **Current estimator.** It takes the k smallest transformed addresses and uses the k-th
  one, x_k. That value is the sum of the first k gaps between consecutive points, and
  each gap is exponentially distributed with rate N+1. A sum of k independent
  exponentials with rate lambda is by definition an Erlang distribution with shape k and
  rate lambda, so x_k is Erlang(k, N+1). This is the spec's own derivation (Appendix C).

- **Probe estimator.** It picks k probe positions and, for each, measures the distance to
  the nearest held chunk going forward. In a uniform field of N points the forward
  distance from an arbitrary position is exponentially distributed with rate N. Summing
  the k independent probe distances gives Erlang(k, N). This is the statistic the
  ProbeSample benchmark in [#241](https://github.com/crtahlin/wasp/issues/241) actually
  computes: it seeks the retrieval index to each probe and takes the successor.

Both estimators are therefore Erlang with the **same shape parameter k**. The shape is
what sets the relative spread of the distribution, on the order of 1 over the square root
of k, and so it is what sets how well the test separates two reserve sizes. The two
estimators differ only in the rate constant, N+1 against N, which is a fixed scale factor
of about one part in a million at these sizes. A scale factor moves where the acceptance
threshold sits; it does not change how many probes are needed. So the probe k for a given
accuracy equals the current k for that accuracy.

The gain of the probe scheme is entirely in cost, not in the statistic: it draws its k
gaps at k probe points in work proportional to k, while the order-statistic scheme must
scan all N chunks to find the k smallest.

## Simulation

[`main.go`](main.go) is a standalone Go program with no dependency on the node code. It
computes alpha and beta in closed form for both estimators from the Erlang cumulative
distribution function,

    F(x; k, lambda) = 1 - e^(-lambda x) * sum_{i=0}^{k-1} (lambda x)^i / i!,

which is exact for integer k. For each k it scans the acceptance threshold u and reports
the alpha and beta that minimize their sum, at the spec's discrimination of an honest
reserve of one million chunks against a slacker target of two million. It also runs a
direct Monte Carlo of the probe estimator, generating uniform points and measuring the
forward-nearest gaps, to confirm the closed form is the right model.

Run it with:

    go run ./docs/experiments/probe-sample-k

### Validation anchor

At k=16 the current estimator gives alpha 0.0975 and beta 0.0718. The spec's Table 3
reports about 0.098 and 0.072. The match confirms the model reproduces Appendix C, so its
probe-side numbers can be trusted.

### The k sweep

**Table: recall error alpha and precision error beta versus number of probes k, for
distinguishing an honest reserve of one million chunks from a slacker at two million,
threshold chosen to minimize alpha plus beta; closed-form Erlang, both estimators**

| k | current alpha | current beta | current alpha+beta | probe alpha | probe beta | probe alpha+beta |
|---|---|---|---|---|---|---|
| 8 | 0.1961 | 0.1374 | 0.3336 | 0.1961 | 0.1374 | 0.3336 |
| 16 | 0.0975 | 0.0718 | 0.1693 | 0.0975 | 0.0718 | 0.1693 |
| 32 | 0.0292 | 0.0221 | 0.0513 | 0.0292 | 0.0221 | 0.0513 |
| 64 | 0.0033 | 0.0025 | 0.0058 | 0.0033 | 0.0025 | 0.0058 |
| 128 | 0.0001 | 0.0000 | 0.0001 | 0.0001 | 0.0000 | 0.0001 |

The two estimators produce identical error rates at every k, to four decimals, confirming
the argument above: same shape k, so same discrimination. The Monte Carlo of the probe
estimator agrees with its closed form within sampling noise (at k=16 the run gives
empirical alpha about 0.086 and beta about 0.077 against analytic 0.098 and 0.072), which
confirms the forward-nearest gap really is exponential and the sum really is Erlang(k).

## The bound on k

k is not a single number; it is a dial that trades accuracy against cost, and the table
gives the exchange rate:

- **k = 16** matches the current scheme exactly: recall error about 0.098, precision
  error about 0.072. This is the parity choice.
- **k = 32** cuts both errors to about 0.03 and 0.02, roughly a threefold improvement.
- **k = 64** reaches about 0.003 each, and k = 128 is below one in a thousand.

So the recommended range is **k in [16, 64]**: 16 for parity with today, 32 to 64 when a
larger safety margin on both error rates is wanted. There is no accuracy reason to go far
beyond this, because the errors are already small; and per
[#241](https://github.com/crtahlin/wasp/issues/241) the cost stays far below the full
sample across this whole range, so the choice is not constrained by cost either. A node
that today spends about 28.5 seconds on the full sample would spend single-digit
milliseconds warm at k=16 and still only tens of milliseconds at k=64.

These numbers are for the spec's own discrimination, a factor of two in reserve size. A
game that had to catch a subtler shortfall, say a node holding 90 percent of its share,
would need a larger k, because separating closer sizes needs a tighter distribution. That
calibration would be done the same way, by setting k until alpha plus beta is small enough
at the target ratio.

## Scope: accuracy only, and protocol not fork

This is the accuracy calibration alone. It says how many probes are needed for the
estimate to be as sharp as the current scheme's. It does **not** address the estimator's
security. The order-statistic sample is hard to forge because a node must hold the whole
set to know its true smallest k transformed addresses. The probe scheme as measured proves
"a held chunk within some distance of the probe", not "the nearest chunk", so whether a
node with a sparse but well-placed reserve could pass is a separate question that needs its
own analysis, flagged in [#235](https://github.com/crtahlin/wasp/issues/235). A sharp
estimator that can be gamed is not a usable proof, so security, not accuracy, is the
gating open question for the probe approach.

And, as [#235](https://github.com/crtahlin/wasp/issues/235) set out, the sampling rule is
shared with every node and the on-chain contract, so any change is Swarm protocol design,
not something this fork decides. The ProbeSample endpoint remains a benchmark only; it is
not wired into the redistribution game.

Generated with help of AI.
