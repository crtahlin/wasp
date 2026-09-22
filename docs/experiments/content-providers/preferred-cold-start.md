# Measured: the first hinted download asks the provider for everything except the content

Issue: [#435](https://github.com/crtahlin/wasp/issues/435).

Measured 2026-09-22 on the two-node bench, `bench-1` as the provider and
`bench-2` as the requester, both running `edd2c4b0`. Harnesses `t435-verify.sh`,
`t435-sets.sh`, `t435-size.sh`, `t435-why.sh`, `t435-2x2.sh`, `t435-holds.sh`,
`t435-corr.sh` and `t435-which.sh`, outside this repository.

Two mechanisms have already been proposed for this issue and both were
withdrawn. This document does not propose a third. It reports what the
measurements below say, and the mechanism that follows from them is the one
that fits every number, including the number that fitted nothing before.

## The symptom is unchanged by #438

[#438](https://github.com/crtahlin/wasp/issues/438) was the standing competing
explanation: the flight exits on the error budget while a preferred delivery is
still outstanding, so the delivery is discarded. It was fixed and merged as
`4f88343c`, and `edd2c4b0` contains it.

| trial | run A, provider disconnected | run B, same reference, five seconds later |
|---|---|---|
| 1 | 404, 36 bytes, 2.37 s | 200, 4,194,304 bytes, checksum matches, 1.79 s |
| 2 | 404, 36 bytes, 2.35 s | 200, 4,194,304 bytes, checksum matches, 1.80 s |
| 3 | 404, 36 bytes, 2.34 s | 200, 4,194,304 bytes, checksum matches, 1.88 s |

**#438's fix does not change this.** That is not a criticism of #438, which
fixed a real defect and was measured on its own terms. It removes it as the
explanation here.

## The dial is not the problem, measured against the right set this time

The earlier dial measurement polled `GET /peers`. That is the **libp2p**
connected set. `preferredCandidates` keeps a peer only when
`connectedFullNode` is true (`pkg/retrieval/preferred.go:202`), and that asks
**Kademlia**, `ClosestPeer(peer, false, topology.Select{})` (`:222-228`), which
is the set `GET /topology` reports under `connectedPeers`. They are different
sets and a dial enters one before the other.

Polling both at 100 ms, three trials, identical to two decimal places in all
three. `dial-race.md` records that identical readings can be measuring the poll
interval rather than the event, so the figures below are resolved to about
0.1 s and no finer; the argument needs only that 0.38 s is far short of 2.34 s.

| | provider appears |
|---|---|
| `/peers`, libp2p | **0.16 s** |
| `/topology`, Kademlia | **0.38 s** |
| request runs for | **2.34 to 2.37 s** |

So the provider is an eligible preferred candidate for about **84%** of the
request, and the download still returns nothing. Waiting for the dial would not
have helped, and this time the claim rests on the set the code actually reads.

> A first version of the poller grepped the `/topology` body for the overlay.
> That body carries `disconnectedPeers` as well, so it reported the provider as
> still connected immediately after a successful disconnect and skipped every
> trial. The poller parses the JSON and reads `connectedPeers` only.

## Content freshness is not the variable either

Every measurement of this symptom so far, here and in `dial-race.md`, ingested
the content immediately before the download **and** disconnected the provider.
Two variables moved together. Separating them, one run per cell, 4 MiB
sole-source at redundancy NONE:

| arm | content | provider connected at start | result | preferred attempts | hits |
|---|---|---|---|---|---|
| 1 | fresh | no | **404**, 36 bytes | +14 | **0** |
| 2 | fresh | yes | 200, 4 MiB, checksum matches | +1040 | +1038 |
| 3 | aged 90 s | no | **404**, 36 bytes | +14 | **0** |
| 4 | aged 90 s | yes | 200, 4 MiB, checksum matches | +1043 | +1041 |

Age does nothing. Connection state at the moment the request starts decides it.

**One number in that table is not understood and is flagged rather than passed
over**, since this document's whole argument is that a figure fitting nothing
must be chased. Arms 2 and 4 record 1,038 and 1,041 preferred hits on an object
of **1,033** chunks. `dial-race.md` and `flight-exit-results.md` both record the
invariant 1,035 attempts and 1,033 hits for the same object, over ten runs
between them. Hits above the chunk count are unexplained. They do not bear on
the 404 against 200 result, which is what these arms exist to separate, but
they are not noise either.

## The 14 attempts are not the download

`dial-race.md` recorded the failing run moving `preferred_attempts` by 14 with
zero hits, and that number has never fitted any account of this issue. It
reproduces exactly, 14 in every trial. Three measurements say what it is.

**It does not scale with the content.** One run at each size, run A only:

| object size | chunks | preferred attempts | candidates selected | misses | hits |
|---|---|---|---|---|---|
| 4,096 bytes | 1 | 14 | 14 | 14 | 0 |
| 65,536 bytes | 17 | 14 | 14 | 14 | 0 |
| 4,194,304 bytes | 1,033 | 14 | 14 | 14 | 0 |

A quantity that is 14 for a one-chunk object and 14 for a 1,033-chunk object is
not per-chunk work of the download.

**The error is the provider refusing.** Raising the retrieval logger to debug
through `PUT /loggers` for the duration of one failing run, the requester
records the provider answering:

```
"msg"="failed to get chunk" "peer_address"="<the provider>"
"error"="delivery of chunk failed: wasp: chunk not held locally"
```

That is `errNotHeldLocally` from `localOnlyMiss`, which the provider sends only
when its own `s.storer.Lookup().Get(ctx, addr)` returns `storage.ErrNotFound`
(`pkg/retrieval/retrieval.go:681-686`).

**And the provider does hold the content it is refusing.** Asking the provider
directly, and at redundancy NONE the reference is the root chunk address, so
this is exactly the chunk in question:

| moment | `GET /chunks/<root>` on the provider | local ingest chunks |
|---|---|---|
| before the disconnect | 200, 264 bytes | 1,225,315 |
| while the requester sees it disconnected | 200, 264 bytes | 1,225,315 |
| right after the refusal | 200, 264 bytes | 1,225,315 |

## The measurement that settles it

Correlating the requester's debug log against the exact chunk the download
needs, one-chunk content so the reference **is** the chunk address:

| | count |
|---|---|
| log lines mentioning our chunk | 33 |
| of those, requests to the **provider** | **0** |
| log lines mentioning the provider | 14 |
| distinct chunk addresses asked of the provider | 14 |
| of those, ours | **0** |
| errors from the provider | 14, all `wasp: chunk not held locally` |

Our chunk was asked of more than thirty ordinary peers, every one answering
`storage: not found`, and **never once of the provider**, which was connected
in Kademlia from 0.38 s and holding it throughout.

**The log alone does not carry that conclusion, and the counter does.**
`pkg/retrieval` logs a peer when a result arrives, not when a request is
dispatched, so a request whose result is discarded on `quit` leaves no line,
which is exactly #438's mechanism. `PreferredAttempts` is incremented at
dispatch (`preferred.go:254`) and moved by exactly 14, matching the fourteen
log lines one for one. So no fifteenth dispatch is hidden behind a discarded
result, and the absence of our chunk from the provider's fourteen is a fact
about dispatches rather than about logging.

The 14 are a different set of chunks, and they are different chunks each run:
two consecutive runs with different content gave 14 addresses each and **zero
overlap**, spread close to uniformly across the address space.

## The mechanism, and it fits every number

A chunk's preferred candidate list is built **once**, before the retry loop,
and never rebuilt inside it (`pkg/retrieval/retrieval.go:232`), and
`preferredCandidates` keeps only peers already connected in Kademlia.

- The content chunk's flight starts at about t=0, when the provider is not yet
  in Kademlia's connected set. Its candidate list is empty and stays empty for
  that chunk's whole retry budget, so the provider is never asked, which is
  exactly what the correlation shows.
- For sole-source content the first chunk is the root, and without the root
  there is nothing else to fetch, which is why the answer is 404 rather than a
  truncated body.
- Chunks whose flights begin **after** 0.38 s would get a non-empty list. That
  is consistent with fourteen attempts existing at all, but this document does
  **not** claim the 14 are those flights: the only identification it tried is
  withdrawn above, and nothing else here establishes what they are. They are refused because the provider does not hold them.
- Run B succeeds because the provider is already connected when the content
  chunk's flight starts.
- Age is irrelevant because nothing here depends on the store.
- #438's fix does not help because the delivery is not discarded. It is never
  requested.

**What the 14 chunks are is unexplained, and the one explanation this document
first offered is withdrawn.** They are not content: constant at 14 across three
content sizes, different addresses for different content, uniformly spread, and
refused as not held.

An earlier revision proposed that they are the provider index and record
lookups this fork performs for a hinted request, and called it a strong fit.
**Review refuted it against the code it cited, on three independent grounds**,
and the withdrawal is recorded rather than the paragraph quietly rewritten:

- `Discover` is called with the preferred set deliberately set to nil,
  `s.providers.Discover(retrieval.WithPreferredPeers(ctx, nil), ...)`
  (`pkg/api/providers.go:114-119`), precisely so that a lookup's own reads do
  not go to the download's preferred peers. So a slot or record read cannot
  become a preferred attempt at all.
- `Lookup` reads **one** window, `w := WindowAt(now)`
  (`pkg/providers/providers.go:306`). With `Slots = 8` the hypothesis predicts
  eight reads, not fourteen.
- Discovery only starts after `discoverAfterChunks = 64` fetches
  (`pkg/api/providers.go:35`). The failing runs die on the root chunk, and the
  one-chunk correlation run makes about one fetch, so discovery almost
  certainly never began.

The document had `Slots = 8` in hand, did not multiply it, and did not read the
call site. That is the failure rule 11 exists for, and it is named here rather
than removed.

So the 14 are unidentified. What is established about them stands on its own:
they are not the download's chunks, they do not scale with the content, and the
provider refuses them.

It does not change the mechanism above either way. The 14 are noise with
respect to the download, and the defect is the content chunk being asked of
everyone except the one peer that has it.

## What this means for the two withdrawn explanations

The second withdrawn comment on #435 proposed refreshing the candidate list
when it runs out, and was withdrawn because the design would have livelocked
and because the 14 attempts contradicted the account it rested on. **The
account was right about the mechanism and the 14 were never part of the
download**; only the proposed fix was wrong. The withdrawal stands as to the
fix and is corrected as to the mechanism.

The first explanation, that the dial is not waited for, stays withdrawn and is
now refuted against the correct connected set.

## What a fix has to deal with, from the record

Not proposed here, listed so the spec does not have to rediscover it:

- **The livelock.** A preferred miss returns through the `res.preferred` arm
  before `errorsLeft--` and before `s.errSkip.Add`, so it spends no error
  budget and is recorded in neither skip list. Any rebuild that can re-add a
  peer already tried must carry a per-flight set of peers already used for that
  chunk, or it will retry the same peer with no exit but the request context.
- **The cost to other operators.** The error budget is suspended while a
  candidate is present (`retrieval.go:464`), and `docs/DIFFERENCES.md` already
  records roughly a fourfold rise in outbound retrieval requests for a chunk on
  a 150-peer node for the neighbouring guard.
- **Discovery has the same gap and a rebuild alone does not close it.**
  `preferredPeers` is a snapshot taken before the flight (`retrieval.go:201-202`),
  so rebuilding from it cannot see a provider that `Discover` appended later.
- **The acceptance test is delivered bytes**, not counters.
  `gate-terms-measured.md:204-209` records that widening a window can let the
  debt rise to meet it and deliver no more bytes, and this issue is itself a
  case where the counters pointed away from the defect for two attempts.

## Node state

Both nodes settled, no restarts, 124 and 121 connected peers, no blocklist
entries, the stock `bee.service` on the provider inactive throughout. The
provider was disconnected from the requester with `DELETE /peers` before each
run A and the disconnect confirmed in both connected sets before the request
was issued.

Rule 7 asks for three runs per condition with the spread. **Only the
three-trial table meets that.** The size table is one run at each of three
sizes, which is three conditions at one run each and not three runs per
condition, and an earlier revision of this paragraph said otherwise. The 2x2
and the correlation are one run per cell and are labelled so where they appear. They are included because each is a qualitative
question, 404 against 200 and zero against fourteen, not a rate, and the 404
arms have now reproduced in every failing run recorded here: three in the
three-trial table, three in the size table, two in the 2x2, and one each in the
log capture, the holds check and the two correlation runs, which is twelve
distinct runs. The three-trial table and the dial table are the same three
runs seen through two instruments, not six, and an earlier revision counted
them twice.

Generated with help of AI.
