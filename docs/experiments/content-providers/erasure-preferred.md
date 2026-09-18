# Carrying the preferred set into erasure-coded downloads

Issue: [#299](https://github.com/crtahlin/wasp/issues/299). Phase 2 of
[#290](https://github.com/crtahlin/wasp/issues/290), named in
[spec.md](spec.md) and measured as a gap in [results.md](results.md).

Code references are to commit `ef00d3eb`, base `upstream/v2.8.2`.

## Terms

- **The preferred set** is the list of providers a download tries first, carried
  to the retrieval layer as a **context value**
  (`pkg/retrieval/preferred.go:139-151`). A set built from an explicit hint
  applies to that request alone; a set built by discovery is shared by every
  download of one content key (`pkg/api/providers.go:90-96`).
- **Singleflight** is the deduplication that collapses concurrent fetches of one
  chunk address into a single request. Its key includes a fingerprint of the
  preferred peers, so a fetch with a set and one without do not share a flight
  (`pkg/retrieval/retrieval.go:186-192`).
- **A read unit** is the span of bytes one `ReadAt` covers. It is all or nothing:
  one chunk failing fails the unit (`pkg/file/joiner/joiner.go:215-223`).
- **An overdraft** is a credit refusal by this node's own accounting before a
  request is sent. **A readmit** is the #324 path keeping a refused provider for
  a later attempt instead of dropping it.
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

This is the only such loss on a live retrieval path. Searching for a context
that starts fresh returns three others, none of them live for this:
`getter.go:358` (a local write of a recovered chunk, no retrieval),
`pkg/storer/netstore.go:26` (the direct-upload path, not a download), and
`pkg/manifest/simple.go:45`, which would lose it too but is unreachable from the
API, since only the mantaray manifest is built today.

**It is a race, not a total loss, and the spec must not overstate it.** Which
side fetches a shard is decided by a compare-and-swap, reached at
`getter.go:128` and performed at `:350`. If the
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

## Phase 1's own rule stops phase 2, and this is an argued exception

`spec.md:543-550` makes "no gain in time to first byte or throughput in
conditions 3 and 5 compared with condition 2, beyond the spread" a negative
result, and says "the first is enough to stop building phase 2 onwards".
`docs/experiments/INDEX.md:65` records that outcome and concludes, in terms,
that the spec's rule stops phase 2. `spec.md:412` lists carrying the set into
erasure-coded content as a phase 2 item. So this spec needs an argument, not
silence.

**The argument is that this item is diagnostic rather than a build-out.** The
rule exists to stop spending effort on making providers faster after they were
measured not to be. This change does not claim a speed gain; its whole
acceptance turns on a counter that says whether a mechanism read from the code
is real, and its expected outcome is written down in advance as a reason not to
ship the unbounded form. It is also a candidate explanation **for** the phase 1
negative: if the set is lost for most of a default-level download, the speed
result was measured on a mechanism that was only partly running.

That is a genuine reading and it is not open-ended, and the binding has to be
something the measurement can actually trigger. **It is this: accepting this
change does not reopen the rest of phase 2.** The other phase 2 items stay
stopped under the phase 1 rule, and each would need its own argument. If the
result here is read as evidence that providers are faster after all, that is a
speed claim, it falls under the phase 1 rule, and it needs the phase 1
conditions rerun rather than this arm quoted at them.

A draft bound itself with "if the measurement produces a speed claim the rule
applies", which could never fire, because nothing in the acceptance table can
produce a speed claim: download time appears there only as a harm check.

## What is NOT established, and must not be re-asserted

**That erasure-coded sole-source content fails because of this.**
`results.md:186-191` records a prediction naming this exact mechanism and says
it was not met:

> Both copies failed, and their figures are indistinguishable, so these runs
> neither confirm nor rule out that mechanism for the erasure coded copy.

A related redundancy-level explanation for truncation was also withdrawn, in
`local-ingest-results.md:126-139`, because two variables moved at once.

So this spec claims a **mechanism read from code**, which is verifiable, and
makes no claim about availability, which is not.

## Hypothesis

### The obvious observable is the wrong one

The obvious prediction is that the provider's share of an erasure-coded download
rises. **It is wrong, and a first draft of this spec made it.** Two things
refute it, both from the document it cited:

- **The MEDIUM share is already the higher of the two.** Condition 3, level NONE
  with a hint, is **163 chunks (160 to 177)**. Condition 3m, the default level
  with a hint, is **177 (176 to 201)** (`results.md:87,92`). The draft proposed
  raising MEDIUM "toward" a number below it.
- **The share is capped by credit, not by how many fetches carry the set.**
  `results.md:109-121` derives the cap directly: a credit window of 18,000,000
  units at about 310,000 a chunk is about 58 chunks, plus refresh, and
  `:165-166` says the erasure-coded arm is "capped like the others". Carrying
  the set into more fetches cannot lift a ceiling that accounting sets.

So an acceptance rule reading "share does not rise, therefore the mechanism is
refuted" would have rejected a correct fix for a reason its own source excludes.

### No existing counter can answer this, so the change adds one

Two drafts proposed observables built from the counters that exist. Both were
wrong, and the second was wrong in the same shape as the first.

**`preferred_attempts + preferred_overdrafts` is inflated by retries.** The #324
readmit branch keeps the peer and **does not consume the candidate**
(`retrieval.go:280-295`, against the success branch at `:304`), so a later retry
runs `prepareCredit` on the same peer and counts the same refusal again, up to
`maxOverdraftReadmits + 1`, which is 9 times for one peer and 18 for one chunk
with both candidates. Credit therefore sets the total, and inflates it exactly
where refusals rise, which is the outcome this spec predicts.

**Subtracting the readmits removes the signal instead.** On the readmit path
there is no `continue`: ordinary selection runs immediately, and the code says so
(`retrieval.go:314-315`). This measurement deliberately uses network-held
content, so an ordinary peer serves the chunk and the flight ends with the
candidate never concluded. The usual per-chunk sequence is one overdraft, one
readmit, net zero. Reaching a net of one needs nine refusals inside one flight,
which needs ordinary selection to keep failing, which is sole-source behaviour
this content excludes.

What is left is `preferred_attempts`, and that increments **only after
`prepareCredit` succeeds** (`pkg/retrieval/preferred.go:228-239`), which
`measurement.md:338-340` states outright: a chunk whose provider was overdrawn
has no attempt. It is credit-capped, which is what the first draft was rejected
for.

**And the data says so.** Preferred attempts at level NONE with a hint are
**165 (162 to 179)**; at the default level with a hint, **178 (178 to 203)**
(`results.md:87,92`). Level NONE is the case where every fetch carries the set,
and it produces fewer attempts than MEDIUM, where most fetches lose it. Any
hypothesis of the form "raise the MEDIUM figure toward the level-NONE one" is
asking for a rise toward a number below the present value. Three drafts of this
spec made that mistake with three different quantities, which is the strongest
evidence that the existing counters do not measure what is being asked.

**So the change adds a counter**, `PreferredCandidatesSelected`, incremented
where `preferredCandidates` returns a non-empty list (`retrieval.go:212`). That
is "the set reached this fetch", stated directly, with no dependence on credit,
on readmits, or on whether an ordinary peer got there first: the call sits before
the retry loop and before any peer is contacted, and reads only the preferred
peers, the chunk address, the error skip list and connectivity.

Two things an implementer and a reader both need. The call is inside the
singleflight closure, so the counter rises once per **flight**, not once per
`RetrieveChunk`, and a deduplicated caller does not move it. And the candidate
list is filtered to connected full nodes (`preferred.go:184-190`), so a provider
that is not connected produces no candidate and no increment, which the
measurement holds fixed by asserting the peer count.

Adding observability in order to make a measurement possible is the shape of
[#353](https://github.com/crtahlin/wasp/issues/353), which existed only to make
an overdraft refusal visible so that [#343](https://github.com/crtahlin/wasp/issues/343)
could be measured at all. The cost here is one counter and one line.

**The hypothesis is therefore: with the set re-attached,
`preferred_candidates_selected` at MEDIUM rises to approach the chunk count of
the download, where today it counts only the fetches the prefetch did not
claim.** The level-NONE arm gives the figure a download reaches when nothing
loses the set. It orients the prediction and decides nothing: every comparison
that settles an outcome is patched against stock at MEDIUM, within one session.

### Whether it helps is a separate question, and it may not

- **The hinted default-level download was already the slowest arm**, 6.09 s
  median against 5.60 s for the same content with no hint (`results.md:92,91`).
  Those two spreads touch at a single millisecond, 5.840 against 5.841, so with
  three runs this orders the arms and does not separate them. It is a reason for
  caution, not a finding.
- **Nothing bounds the concurrency, and the accounting work says concurrency is
  what breaks credit.** The joiner's errgroup has no limit (`joiner.go:215`), a
  decoder dispatches one goroutine per outstanding shard with no limit
  (`getter.go:245-249`), and there is one live decoder per intermediate chunk
  the reader touches, so the ceiling is that many times **119** at MEDIUM.
  `gate-terms-measured.md:76-84` found peers at a reserved balance near
  18,000,000 from "concurrency overruns", and `truncation-cause.md` establishes
  that refusals on one chunk passing `maxOverdraftReadmits`, which is 8
  (`pkg/retrieval/retrieval.go:159`), drop the only holder and kill the read
  unit. The set holds at most `maxProviderHints`, which is 8
  (`pkg/api/providers.go:31`), with `maxPreferredAttempts` of 2 per chunk
  (`preferred.go:34`), so the concentration is onto a few peers rather than
  literally one.

**So the pre-registered prediction is two-sided**: the candidate count rises,
and `preferred_overdrafts` rises with it, possibly far enough to make truncation
more likely. A result showing that settles #299 as "the set can be carried and
should not be, unbounded", and produces the bound as the next issue.

### One effect points the other way

The set's fingerprint is part of the singleflight key
(`retrieval.go:186-192`), so today a prefetch fetch without a set and a reader
fetch with one take **different** keys and both run. Giving the prefetch the
same set collapses them.

**The size of that effect is much smaller than a first draft claimed.** It put
it at the whole gap between 4,750 to 5,073 requests and about 4,129 at level
NONE. Most of that gap is not attributable: the **unhinted** default-level arm
alone made 4,750 to 4,830 requests, and with no hint the set has no peers, so
`fingerprint` returns the empty string and both keys already match. The
attributable part is the hinted arm's excess over the unhinted one, a few
hundred requests at most. `results.md:170` claims only that "the hinted ones
made the most", which is the correct and weaker statement.

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
two lines, in two files of this fork's own.

**`HasPreferredPeers` is defensive, and the reason a first draft gave for it was
wrong.** That draft argued the naive `PreferredPeers(ctx) == nil` check would
undo the deliberate suppression on the line above, which stores a nil set so the
provider lookup's own reads do not go to the download's providers. It would not:
that suppressed context is built inline, handed straight to `Discover`, and
never assigned back, and `Discover` reads through a different getter entirely,
the plain storer one wired at `pkg/node/providers.go:58`. It never re-enters
this wrapper, so the guard is never evaluated on it.

In every reachable case the two checks therefore agree, and a test named for the
suppression would pass with either. The helper is kept because it states the
intent exactly, distinguishing "no set was ever attached" from "preference was
deliberately switched off", and because the suppressed context reaching a
wrapper is a change one refactor away. It is **not** load-bearing today, and the
spec says so rather than claiming a defect it does not prevent. The Go semantics
it rests on are real, confirmed by running them rather than reading them: a
typed nil stored under a key satisfies the type assertion, so the value is nil
while the key is present.

```go
// HasPreferredPeers reports whether ctx carries a preferred set, including one
// deliberately set to nil to suppress preference.
func HasPreferredPeers(ctx context.Context) bool {
        _, ok := ctx.Value(preferredKey{}).(*PreferredSet)
        return ok
}
```

**The decoder cache is per download, and #299's own text says otherwise.** The
issue asks which request's set should win, "since the decoder is shared by all
requests for the same intermediate chunk". It is not. `NewDecoderCache` has one
non-test caller, `pkg/file/joiner/joiner.go:184`, inside the joiner
constructor, and a joiner is built per download (`pkg/api/bzz.go:780,782`). The
cache is a field of that joiner, so decoders are never reused across requests
and there is no cross-request precedence to define. Rule 11 asks for corrections
to be carried with the finding, so it is recorded here rather than left in the
issue.

A first draft of this spec repeated the issue's premise and built three things
on it: a lifetime argument, a section on shared decoders, and a rule for
discarding runs whose decoder cache had been warmed by a previous arm. All three
are removed. The wrapper design is still the right one, for the smaller and true
reason that it leaves `pkg/file/` untouched.

**The set's lifetime, stated correctly.** For a request carrying an explicit
hint the set is built fresh and applies to that request only, which the code
comment at `pkg/api/providers.go:90-96` says. The ten minute `providerSetTTL`
(`providers.go:38`) applies to the **discovered** set shared across downloads of
one content key, which is a different case from the one measured here. A first
draft used the TTL to argue the set safely outlives a request; since the whole
measurement is hint-driven, that argument did not apply to it.

### A second defect on the same path, filed separately

The same prefetch context breaks provider discovery outright. The lookup is
triggered from whichever chunk fetch is counted 64th
(`pkg/api/providers.go:114-119`), and `Discover` derives its background
goroutine from that fetch's context (`pkg/providers/providers.go:308-311`). On
erasure-coded content that fetch is almost always a prefetch fetch, whose
per-shard context is cancelled as soon as that one shard returns
(`getter.go:130-131`), while a lookup takes 1.54 to 1.67 s. So discovery is
cancelled roughly two orders of magnitude too early, and only at level NONE,
where the reader's context is in play, does it work.

Filed as [#369](https://github.com/crtahlin/wasp/issues/369) rather than fixed
here, because the two want settling together and neither should be fixed twice.
It also means **a discovery arm at the default level would measure a broken
lookup rather than the preferred set**, which is why the measurement below uses
an explicit hint throughout.

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
- **A provider that does not hold the parity chunks**, which is a fallback-path
  concern only: under the shipped `DATA` strategy parity indices are never
  fetched, and they are added only under `RACE`, reached after `DATA` fails
  (`pkg/file/redundancy/getter/getter.go:221,231-233`). Both routes that make a
  provider do hold them: pinning traverses and stores every reported address
  including parity (`pkg/traversal/traversal.go:46-52`, `joiner.go:419-439`),
  and local ingest generates them locally at the upload level
  (`pkg/api/localingest.go:72-75`). A first draft offered the miss path as the
  mitigation, which it is not: `demoteAfterMisses` is 16 and the misses must be
  consecutive, since a hit clears the counter (`preferred.go:39,110`), and
  `truncation-cause.md:92-97` says flatly that it never fires. The real
  mitigation is that a preferred miss falls through to ordinary selection, so on
  network-held content the chunk is served anyway and the cost is a wasted round
  trip. On sole-source content it would not be, which is one more reason this
  measurement does not use sole-source content.
- **The change does nothing at all for encrypted content, and not for the reason
  a first draft gave.** That draft said discovery cannot supply a set for a
  64-byte reference so only an explicit hint can. An explicit hint does not help
  either: `withProviders` nulls the content key for a 64-byte reference
  (`pkg/api/providers.go:86-88`), and `providerGetter` returns the **bare**
  getter when the key is nil (`:111-113`), so the wrapper is never installed and
  the re-attach cannot run. The same early return applies to `GET /chunks` and
  `GET /feeds`, which pass no key. Extending the wrapper to a hint with no key
  would reach encrypted content, and is deliberately out of scope here.

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

**Content, stated because the first draft did not.** One file of 16 MiB at
**MEDIUM**, the shipped upload default, pinned and announced on the provider,
and **held by the network as well**, matching the conditions the 176 to 201 and
4,750 to 5,073 figures came from. It is deliberately **not** sole-source: the
truncation mechanism in `truncation-cause.md` turns on its step 4, "the content
is sole-source, so no ordinary peer can serve it", and on network-held content a
refusal that is not readmitted falls through to ordinary peers and succeeds.
Measuring on sole-source content would mix a truncation risk into a mechanism
question.

The same bytes at **level NONE** are a **null control, not a treatment arm**: at
that level no decoder is built at all, because with no parity references
`len(addrs) == shardCnt` and `GetOrCreate` returns the plain getter
(`joiner.go:92-94`). Stock and patched are therefore identical by construction
there, and a difference between them would mean the harness is wrong. It also
supplies the level-NONE candidate count the hypothesis compares against.

`Swarm-Cache` held fixed across every arm, because with caching off the
recovered chunks are not retained and a later read refetches them through the
fallback decoder, which would move the request count on its own.

Arms: stock against patched, at both levels, for **12 runs**.

Recorded per run:

- **`preferred_candidates_selected`, the counter this change adds, and the
  primary observable.** It rises once per **flight** whose candidate list is not
  empty, which is "the set reached this fetch" with no dependence on credit, on
  readmits, or on an ordinary peer arriving first;
- `preferred_attempts` and `preferred_overdrafts` separately, recorded because
  they are needed to read the primary one and **not** usable as it, for the
  reasons under the Hypothesis;
- `preferred_overdrafts` and `preferred_readmits` separately, and **the
  difference between overdrafts and readmits**, which `truncation-cause.md`
  establishes as the quantity that decides whether a read unit survives;
- **chunks served by the provider**, reported for continuity with the 176 to 201
  baseline but **not** decisive, because `results.md:109-121` shows it is bound
  by the credit window;
- **total chunk requests**, for the singleflight effect, read as the patched
  arm against the stock arm of the same session rather than against the
  historical figure;
- `preferred_misses`. The "1 or 2 per download" figure belongs to the size
  sweep in `truncation-cause.md:95-97`; across the #290 conditions it reached
  5 (2 to 6) and 20 (19 to 20) (`results.md:88,90`), so the invalidation
  threshold below is set against the stock arm of the same session rather than
  against a remembered number;
- delivered bytes, `curl` exit code and body SHA-256;
- **total download time**, which is decisive here and not merely reported: the
  concentration risk is the reason this change might be unshippable;
- the provider's `/blocklist` naming the requester;
- and the three conditions the invalidation list needs but cannot check after
  the fact: the `Swarm-Cache` setting used, the provider grant asserted zero,
  and the peer count on both nodes.

**Every comparison is patched against stock within one session.** The #290
figures are quoted for orientation only: they were taken on a 16 MiB file with
30 ms added latency against a particular peer count, and reusing them as a
control would compare across node states, which this repository has already had
to withdraw a result for once.

**What a negative result looks like.** `preferred_candidates_selected` does not
separate between stock and patched at MEDIUM, with the three-run ranges
overlapping. That says the set is not reaching the prefetch even with the
re-attach, so the mechanism read from the code is not what happens, and the spec
is wrong rather than the change. It is the reject row of the table below. The
counter cannot fall, since the change only adds set-carrying fetches, so failing
to separate is the whole of the negative case and there is nothing else for it
to look like.

## Acceptance

Everything below is at MEDIUM. The level-NONE arm is a null control and decides
nothing; if stock and patched differ there, the run is invalid rather than
informative.

**Two conditions come first and override the rest.** If the provider blocklists
the requester in any run, or the **patched** arm produces a body hash mismatch,
the change is **rejected** whatever else it did. A stock-arm hash failure
invalidates the run instead, since it says the content or the bench is wrong
rather than the change.

Then, on the primary observable, `preferred_candidates_selected`, patched
against stock in the same session, with **apart** meaning the three-run ranges do
not overlap, applied to **every** column below and not only the first:

| Candidates selected | Overdrafts not readmitted | Download time | Outcome |
|---|---|---|---|
| rises, apart | not risen apart | not worse apart | **accept and ship** |
| rises, apart | not risen apart | worse, apart | **accept the mechanism, do not ship**: the cost is the concentration, and the bound is the next issue |
| rises, apart | rises, apart | either | **accept the mechanism, do not ship unbounded**: same follow-up, with the readmit exhaustion as its evidence |
| does not rise apart | either | either | **reject**: the set is not reaching the prefetch even with the re-attach, so the mechanism read from the code is not what happens |

**The first column cannot fall, which is why it has no fall row and why an
overlap is a refutation rather than a rerun.** The change only re-attaches the
set to fetches that previously lost it, so every flight with a non-empty
candidate list in the stock arm still has one in the patched arm: patched is
greater than or equal to stock by construction. A draft made "falls, apart" the
reject row and routed an overlap to "rerun with more runs", which put the only
genuine negative into a row that can never fire and the real null result into an
endless rerun.

The predicted effect is large, from roughly 180 today to roughly the chunk count
of the download. An effect of that size cannot hide inside three overlapping
runs, so **overlapping ranges here refute the mechanism** rather than
under-power the test. That is the opposite reading from an overlap in the other
two columns, and the difference is deliberate: those columns ask whether a
second-order harm appeared, where three runs genuinely cannot separate small
moves.

**Apartness applies to every column.** "Not risen apart" and "not worse apart"
each cover both an overlap and a move the other way, so no dataset matches two
rows and none matches none. Two drafts got this wrong in opposite directions:
one left every overlapping download time matching no row, and the next left an
overlapping rise in the overdraft column matching two.

**Download time is a harm check, not a signal.** Only a worse time with ranges
apart decides anything; an overlap is read as the absence of harm rather than as
the absence of information. That is the opposite reading from the first column,
and deliberately so: a rise there is the claim, and a claim needs separation,
while harm is what has to be ruled out.

**The last two columns are not independent of the first.** More candidates
selected is what produces more credit decisions, so the overdraft column is
expected to move with the primary one; that is why it changes the disposition
rather than the verdict.

Two quantities are recorded and deliberately decide nothing on their own. The
**provider's served share** is credit-capped, so it may not move even when the
mechanism works. **Total chunk requests** falling is corroboration that the
prefetch and the reader now share a singleflight key, which is a second
signature of the same cause; it is expected, and its absence alongside a rising
candidate count is worth reporting but is not a reject.

Delivered bytes are a **floor**: the content is network-held, so both arms
should complete, and any shortfall in either is a reject for that arm.

**What invalidates a run** rather than deciding it: a different `Swarm-Cache`
setting between arms; a provider grant other than zero; a peer count that
changes between arms; `preferred_misses` rising far above the stock arm of the
same session, which says the provider is missing chunks and the arm is measuring
that; a stock-arm hash failure; or any difference between stock and
patched in the level-NONE control.

## Rollout and rollback

Nothing new to turn on. The path is reached only with `providers-enable` on and
a hint present, so a node running the shipped defaults never enters it. Rolling
back is reverting the two files named below; nothing persists and no peer
state depends on it.

## Upstream portability

**Deliberately not upstreamable, and that is the point of the design.** The
change lives entirely in fork-authored code, `pkg/api/providers.go` and
`pkg/retrieval/preferred.go`, and leaves `pkg/file/` byte-identical to upstream.

The underlying observation is about upstream code: the decoder's prefetch runs
from a context with no values, so **any** caller-supplied context value is lost
to it, not only this fork's.

**Upstream loses something of its own to it, and a first draft asserted a design
intent instead of checking.** `pkg/storer/netstore.go:87` starts a span from the
caller's context and `pkg/retrieval/retrieval.go:371` follows it, so from a
background context both become root spans with no parent. Every data-shard
retrieval of an erasure-coded download is therefore detached from that
download's trace in unmodified Bee.

It is still **no `affects-upstream` label**, for two reasons that are about
evidence rather than judgement. The claim is read from code and not reproduced,
and rule 11 says to leave such a case untagged and record what would justify the
tag: here, an actual trace of an erasure-coded download showing the orphaned
spans. And detaching a prefetch from its caller's cancellation is a deliberate
choice, whatever it costs in tracing, so the question is whether losing the
other values with it was intended, which a trace would not settle on its own.

Two smaller corrections to that draft's reasoning: the prefetch context is
cancelled by `defer cancel()` when `runStrategy` returns (`getter.go:243`), so
what it outlives is the request's cancellation rather than the request; and the
change touches two files, not one.

## Files and test plan

- `pkg/retrieval/preferred.go`: `HasPreferredPeers`.
- `pkg/api/providers.go`: the re-attach in `providerGetter`.
- `pkg/retrieval/retrieval.go` and `pkg/retrieval/metrics.go`: the
  `PreferredCandidatesSelected` counter, incremented once per **flight** whose
  candidate list is not empty. Without it there is nothing to measure, for the
  reasons under the Hypothesis, so it is part of the change rather than of the
  harness.
- `pkg/retrieval/preferred_test.go`, `package retrieval_test`:
  - `TestPreferredCandidatesSelectedCountsOncePerFlight`, including a flight
    with an empty candidate list, which must not move it, and two deduplicated
    callers of one chunk, which must move it once. It belongs here and not in
    `package api_test`, because the counter lives in `pkg/retrieval` and rises
    inside `RetrieveChunk`.
  - `TestHasPreferredPeersDistinguishesAbsentFromNil`, which pins the Go
    semantics the helper rests on. It is not a discriminating test against the
    naive check, because no reachable path today tells the two apart, and the
    spec says so rather than implying the test proves the helper necessary.
- `pkg/api/providers_test.go`, `package api_test`, following the existing idiom
  at `:75-79`, which asserts on the context a call receives:
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
