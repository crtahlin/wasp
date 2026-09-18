# Carrying the preferred set into erasure-coded downloads

Issue: [#299](https://github.com/crtahlin/wasp/issues/299). Phase 2 of
[#290](https://github.com/crtahlin/wasp/issues/290), named in
[spec.md](spec.md) and measured as a gap in [results.md](results.md).

Code references are to commit `ef00d3eb`, base `upstream/v2.8.2`.

## Terms

- **The preferred set** is the list of providers a download tries first. It is a
  mutable object shared by every download of one content key, carried to the
  retrieval layer as a **context value** (`pkg/retrieval/preferred.go:139-151`).
- **A decoder** is the erasure-coding reader for one intermediate chunk. It is
  created when that chunk's children include parity references
  (`pkg/file/joiner/joiner.go:86-94,126`).
- **The prefetch** is the goroutine a decoder starts the moment it is
  constructed (`pkg/file/redundancy/getter/getter.go:81`), before any caller has
  asked it for a chunk.
- **A data shard** is a child chunk holding content. **A parity shard** is a
  child chunk holding Reed-Solomon redundancy for its siblings.
- **`wasp-local-only`** is the stream header a preferred attempt carries, which
  tells the peer to answer from its own store or not at all.

## Problem

**The preferred set does not reach the chunk fetches an erasure-coded download
actually makes.** The loss is one line:

```go
// pkg/file/redundancy/getter/getter.go:240-249
c := make(chan error, len(m))

ctx, cancel := context.WithCancel(context.Background())
defer cancel()

for _, i := range m {
        go func(i int) {
                c <- g.fetch(ctx, i, false)
        }(i)
}
```

`context.Background()` carries no values. Every fetch the prefetch issues
descends from it, so `PreferredPeers(ctx)` at `pkg/retrieval/retrieval.go:181`
returns nil, no preferred candidate is built, and the chunk goes straight to
ordinary peer selection.

This is the only such loss in the download path. Searching the whole path for a
context that starts fresh returns three results: this one,
`getter.go:358` (a local write of a recovered chunk, no retrieval), and
`pkg/storer/netstore.go:26` (the direct-upload path, not a download).

**It is a race, not a total loss, and the spec must not overstate it.** Which
side fetches a shard is decided by a compare-and-swap at `getter.go:128`. If the
reader's own `Get` claims the shard first it runs under the request context and
does carry the set. The prefetch usually wins because it starts at construction,
but not always, and the measurement already shows the partial effect: a hint
still reached **176 to 201 chunks** of a default-level file
(`results.md:164-167`).

**Who is affected.** Anything uploaded at the shipped default, since
`DefaultUploadLevel = MEDIUM` (`pkg/file/redundancy/level.go:181`). Content at
level NONE never builds a decoder at all, because with no parity references
`len(addrs) == shardCnt` and `GetOrCreate` returns the plain getter
(`joiner.go:92-94`). That is why every condition in the #290 measurement except
two ran at level NONE.

## What is NOT established, and must not be re-asserted

**That erasure-coded sole-source content fails because of this.**
`results.md:184-191` records a prediction naming this exact mechanism and says
it was not met:

> Both copies failed, and their figures are indistinguishable, so these runs
> neither confirm nor rule out that mechanism for the erasure coded copy.

A related redundancy-level explanation for truncation was also withdrawn, in
`local-ingest-results.md:122-137`, because two variables moved at once.

So this spec claims a **mechanism read from code**, which is verifiable, and
makes no claim about availability, which is not.

## Hypothesis

Re-attaching the set to the prefetch's fetches raises the share of an
erasure-coded download served by a provider from the measured 176 to 201 chunks
toward the share a level-NONE download already reaches.

**Whether that is good is a separate question, and the honest answer from the
existing data is that it may not be.** Two measured facts point against it:

- **The hinted default-level download was already the slowest arm.** Condition
  3m in `results.md` is a 6.09 s median (5.84 to 6.75) against 5.60 s (5.34 to
  5.84) for the same content with **no** hint, and 5.12 s for hinted level-NONE
  content. Directing more of it at one peer is unlikely to reverse that on its
  own.
- **Nothing bounds the concurrency, and the accounting work says concurrency is
  what breaks credit.** The joiner's errgroup has no limit
  (`joiner.go:215`), a decoder dispatches one goroutine per outstanding shard
  with no limit (`getter.go:245-249`), and there is one live decoder per
  intermediate chunk the reader touches. At MEDIUM that is up to **119
  concurrent fetches per decoder**. Today they spread across whichever peers
  `closestPeer` returns. After this change they would point at **one** peer.
  `gate-terms-measured.md:76-84` already found peers sitting at a reserved
  balance near 18,000,000 from "concurrency overruns", and
  `truncation-cause.md` establishes that once refusals on one chunk pass
  `maxOverdraftReadmits`, which is 8 (`pkg/retrieval/retrieval.go:159`), the
  only holder is dropped and the read unit dies.

**So the pre-registered prediction is two-sided**: provider share rises, and
`preferred_overdrafts` rises with it, possibly far enough to make truncation
more likely rather than less. A result showing that is a real finding and closes
#299 as "the set can be carried and should not be, unbounded".

**One effect points the other way.** The preferred set's fingerprint is part of
the singleflight key (`retrieval.go:186-192`), so today a prefetch fetch (no
set) and a reader fetch (set) for the same address take **different** keys and
both run. That is the measured amplification: default-level downloads made
**4,750 to 5,073** chunk requests against about 4,129 for the same size at level
NONE, and the hinted ones made the most (`results.md:168-171`). Giving the
prefetch the same set collapses those to one flight, which should reduce request
count. The measurement must separate this from the provider-share effect,
because they move different counters in different directions.

## Design

**Re-attach the set in the fork's own getter wrapper. Do not touch
`pkg/file/`.**

`pkg/file/` is currently **byte-identical to `upstream/v2.8.2`**, verified with
`git diff --stat upstream/v2.8.2 origin/main -- pkg/file/`, which prints
nothing. Changing it would create the first fork divergence in that package,
which every future upstream sync then carries, and would mean changing either
the exported `getter.Config` struct or the `getter.New` signature.

None of that is necessary, because the decoder already calls fork-authored code.
The getter handed to the joiner is `s.providerGetter(ctx, s.storer.Download(cache))`
(`pkg/api/bzz.go:780,782`), it reaches the decoder as its `fetcher`
(`joiner.go:126`), and the prefetch calls it at `getter.go:146`. The wrapper is
`pkg/api/providers.go:109-122` and it already closes over the hint that holds
the set:

```go
return storage.GetterFunc(func(ctx context.Context, addr swarm.Address) (swarm.Chunk, error) {
        if hint.fetched.Add(1) == discoverAfterChunks {
                s.providers.Discover(retrieval.WithPreferredPeers(ctx, nil), hint.key, hint.set)
        }
        if !retrieval.HasPreferredPeers(ctx) {
                ctx = retrieval.WithPreferredPeers(ctx, hint.set)
        }
        return g.Get(ctx, addr)
})
```

The `ctx` parameter there is **whatever the caller passed**, which for a
prefetch fetch is the background context. So the set is restored at the last
point before retrieval, for exactly the fetches that lost it, and the change is
two lines in this fork's own file.

**Why `HasPreferredPeers` and not `PreferredPeers(ctx) == nil`.** The line above
it deliberately **clears** the set, so that the provider lookup's own reads do
not go to the download's providers. That clearing stores a nil set under the
key, and a nil-valued check cannot tell it apart from a context that never had
one, so the naive check would undo it. The new helper tests whether the key is
present at all:

```go
// HasPreferredPeers reports whether ctx carries a preferred set, including one
// deliberately set to nil to suppress preference.
func HasPreferredPeers(ctx context.Context) bool {
        _, ok := ctx.Value(preferredKey{}).(*PreferredSet)
        return ok
}
```

**The lifetime question this avoids.** The alternative designs all store a
context on the decoder, and a decoder outlives the request that created it: it
is cached per intermediate chunk (`joiner.go:96-123`) and reused by later
downloads that share that chunk. Storing a request context there would be a
lifetime bug, needing `context.WithoutCancel` to avoid cancelling one download's
prefetch when a different download ends. Re-attaching at the wrapper sidesteps
it, because the wrapper is created per request and the set it closes over is
already designed to outlive a request, kept for `providerSetTTL` of ten minutes
(`providers.go:129-164`).

**A decoder shared between downloads is still shared.** A decoder built by a
request with no hint is reused by a later request that has one, and that later
request gets nothing from the already-running prefetch. This design does not fix
that and should not: the first-request-wins behaviour matches the existing
shared-set rule. It does mean the effect size depends on cache state, which the
measurement has to hold fixed.

### Bounding, and why it is not in this change

The concurrency concern above argues for a limit on simultaneous preferred
attempts against one peer. That is a real change with its own trade-off, it
belongs to the same family as
[#327](https://github.com/crtahlin/wasp/issues/327) and
[#359](https://github.com/crtahlin/wasp/issues/359), and putting it here would
mean the measurement could not tell which half did what. **The unbounded form is
measured first, deliberately, and a bound gets its own issue if the numbers
call for one.** Recording the intent here so that a later reader does not read
the omission as an oversight.

## Configuration

**No new setting.** The behaviour is already gated by `providers-enable`
(`pkg/retrieval/retrieval.go:132-134`, flag at `cmd/bee/cmd/cmd.go:94`), which
is off by default, and by whether a request carries a hint. A node with
providers off is unaffected, and so is every download of level-NONE content.

Rule 8 is satisfied by not adding a dial. If the measurement shows the
concurrency needs bounding, that bound is a tuning constant and gets exposed as
a config option in its own issue, with the current value as its default.

## What this risks

- **Pointing up to 119 concurrent reservations at one peer**, where they are
  currently spread. This is the main risk and the main thing measured.
- **Making truncation more likely**, by driving refusals on one chunk past the
  readmit bound of 8.
- **A provider that does not hold the parity chunks.** Both routes that make a
  provider do hold them: pinning traverses and stores every reported address
  including parity (`pkg/traversal/traversal.go:46-52`, `joiner.go:419-439`),
  and local ingest generates them locally at the upload level
  (`pkg/api/localingest.go:72-75`). A provider missing one is handled by the
  existing miss path, which drops a peer after `demoteAfterMisses`
  (`preferred.go:39`), so the failure mode is a slower download and not a
  broken one.
- **No benefit for encrypted content.** Announce and lookup both refuse a
  64-byte reference (`pkg/api/providers.go:236-239`), so discovery can never
  supply a set for it and only an explicit hint can. The decoding path itself is
  unaffected.

## Protocol impact

**No frozen surface is touched.** No message type, no protobuf field, no
handshake value, no constant in `pkg/swarm` or `pkg/config`. The
`wasp-local-only` stream header already exists and its semantics are unchanged:
a parity chunk is an ordinary content-addressed chunk, and the handler serves
whatever is in the local store without regard to chunk type
(`pkg/retrieval/retrieval.go:594`).

**It is peer-visible in volume, not in kind.** A provider receives more
`wasp-local-only` requests per download than before, from one requester, in
bursts. That is the risk named above, and it is what the provider-side counters
in the measurement are for.

## Measurement

Requester and provider on the bench, three runs per condition, interleaved,
balances reset between runs, with the spread reported per condition.

Content: one file at **MEDIUM**, the shipped upload default, and the same bytes
at **level NONE** as the reference for what full preference already achieves.
`Swarm-Cache` held fixed across every arm, because with caching off the
recovered chunks are not retained and a later read refetches them through the
fallback decoder, which would move the request count on its own.

Arms: stock against patched, at both levels, for **12 runs**.

Recorded per run:

- **chunks served by the provider**, the primary observable, against the
  measured 176 to 201 baseline at MEDIUM and the level-NONE figure from the same
  session;
- **total chunk requests**, against the 4,750 to 5,073 baseline, to see the
  singleflight effect separately;
- `preferred_overdrafts`, `preferred_readmits`, and **the difference**, which
  `truncation-cause.md` establishes as the quantity that decides whether a read
  unit survives;
- `preferred_misses`, which rose by only 1 or 2 per download in every run so
  far, so a large rise means a provider is missing parity chunks;
- delivered bytes, `curl` exit code and body SHA-256;
- total time, reported but not credited to this change on its own;
- the provider's `/blocklist` naming the requester.

## Acceptance

Everything below is at MEDIUM, the arm under test. The level-NONE arm sets the
reference for what full preference already achieves and decides nothing on its
own.

**Two conditions come first and override the rest.** If the provider blocklists
the requester in any run, or any body hash fails, the change is **rejected**
whatever else it did. The first is the hazard the concurrency concern predicts;
the second means the download is wrong, and a faster wrong answer is not a
result.

Then, on the primary observable, **chunks served by the provider**, read against
the stock arm of the same session:

| Provider share | Overdrafts not readmitted | Delivered bytes | Outcome |
|---|---|---|---|
| rises, spreads apart | does not rise | not lower | **accept and ship** |
| rises, spreads apart | rises | not lower | **accept the mechanism, do not ship unbounded**: the bound becomes the next issue |
| rises, spreads apart | either | **lower** | **reject**: the change costs delivery |
| rises, spreads overlap | either | either | **undetermined**: rerun with more runs before reading it |
| does not rise | either | either | **reject**: refutes the mechanism read from the code, so something other than the background context drops the set |

Delivered bytes are a **floor, not an equality**. An earlier draft of this spec
required them to be "unchanged", which would have rejected the change for
improving them. The prediction is that they do not move, because the preferred
path and ordinary selection both end at the same chunks; a rise is a welcome
surprise and not a failure.

**What invalidates a run** rather than deciding it: a decoder cache warmed by a
previous arm, since a decoder built without a hint is reused with one; a
different `Swarm-Cache` setting between arms; a provider grant other than zero;
a peer count that changes between arms; or `preferred_misses` rising by much
more than the 1 or 2 per download seen so far, which says the provider is
missing parity chunks and the arm is measuring that instead.

## Rollout and rollback

Nothing new to turn on. The path is reached only with `providers-enable` on and
a hint present, so a node running the shipped defaults never enters it. Rolling
back is reverting two lines in this fork's own file; nothing persists and no
peer state depends on it.

## Upstream portability

**Deliberately not upstreamable, and that is the point of the design.** The
change lives entirely in fork-authored code, `pkg/api/providers.go` and
`pkg/retrieval/preferred.go`, and leaves `pkg/file/` byte-identical to upstream.

The underlying observation is about upstream code: the decoder's prefetch runs
from a context with no values, so **any** caller-supplied context value is lost
to it, not only this fork's. That is a design property rather than a defect,
since the prefetch deliberately outlives the request that started it, and rule
11 says to tag defects and not preferences. **No `affects-upstream` label**, and
this paragraph records what was considered so the question is not reopened
without new evidence.

## Files and test plan

- `pkg/retrieval/preferred.go`: `HasPreferredPeers`.
- `pkg/api/providers.go`: the re-attach in `providerGetter`.
- `pkg/retrieval/preferred_test.go`, `package retrieval_test`:
  - `TestHasPreferredPeersDistinguishesAbsentFromNil`, the whole reason the
    helper exists.
- `pkg/api/providers_test.go`, `package api_test`, following the existing idiom
  at `:75-79` which already asserts on the context a getter receives:
  - `TestProviderGetterRestoresSetOnBackgroundContext`, a fetch through the
    wrapper with `context.Background()` arrives at the underlying getter
    carrying the set;
  - `TestProviderGetterKeepsDeliberateNil`, the suppression at the discover
    call is not undone;
  - `TestProviderGetterLeavesExistingSetAlone`, a context that already carries a
    set is passed through unchanged.
- `docs/DIFFERENCES.md`: a row, since this changes what a node does.
- `docs/experiments/content-providers/`: the results document.

Generated with help of AI.
