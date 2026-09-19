# Giving provider discovery a lifetime it owns: results

Issue: [#369](https://github.com/crtahlin/wasp/issues/369). Spec:
[discovery-lifetime.md](discovery-lifetime.md), merged as
[`f74f0234`](https://github.com/crtahlin/wasp/commit/f74f0234). Implementation
merged as [`d48b11e5`](https://github.com/crtahlin/wasp/commit/d48b11e5), tagged
`exp-discovery-lifetime`.

Measured 2026-09-19 on the two-node bench, `bench-1` as the provider and
`bench-2` as the requester. Harnesses `cp290/t20b.sh` and `cp290/t20c.sh`, rows
in `cp290/t20b-discovery-lookup-after.txt`, all outside this repository per rule
10.

**Only arm 1 was run.** Arms 2, 3, 4, 5 and 6 were not. This document is scoped
to what arm 1 establishes and is explicit about what it does not, which is most
of the spec: arm 1 does **not** test the lifetime claim, and arms 2 and 6 are
what do.

## The result

**The lookup completes, 6 of 6.** Before the change it was cancelled 3 of 3 at
the shipped default redundancy level.

| Level | Runs | `lookups_canceled` | `lookups_completed` | `connects_dialed` | `lookups_served_from_cache` | HTTP |
|---|---|---|---|---|---|---|
| default | 3 | 0 of 3 | +1 each | +1 each | 0 | 200, 16,777,216 bytes |
| NONE | 3 | 0 of 3 | +1 each | +1 each | 0 | 200, 16,777,216 bytes |

Across the whole pass the counters move monotonically and without gaps:
`discoveries_started` 0 to 6, `lookups_completed` 0 to 6, `connects_dialed` 0 to
6, with `lookups_canceled`, `lookups_served_from_cache`, `connects_failed`,
`connects_already_connected` and `hinted_connects_started` all flat at 0.

Download times were 8.16, 6.38, 7.28, 5.62, 5.76 and 3.73 seconds.

**Three of those counters are doing work rather than decorating the table.**

- `lookups_served_from_cache` at 0 is the guard that every run performed a real
  lookup. Each run used a reference uploaded for it and used exactly once, so a
  cache hit would have meant the arm was not exercising the fix. This is the
  condition the spec added after an earlier version defined the counters so that
  a cache hit raised both it and `lookups_completed`.
- `connects_already_connected` at 0 means every connect was a dial rather than a
  peer that was already connected. That is the confound arms 2 and 6 are built
  around, and it does not arise here because the provider was disconnected
  before each run and confirmed absent from `/peers`.
- `discoveries_started` rising once per run is the denominator: without it, a
  completed lookup and a discovery that never ran are the same reading.

Acceptance condition 1 asks for the lookup not to be cancelled in three runs at
the default level, `lookups_completed` to rise by exactly one, the cache counter
not to rise, and the lookup to have **found** the provider, shown by one of the
`Connects` counters rising. All four hold in all three runs, and the level-NONE
control holds in all three of its own.

## What this does not show, and it is most of the spec

- **Nothing about the dial outliving the request**, which is the actual lifetime
  claim. That is arm 2 at level NONE and arm 6, and neither was run. Arm 1 shows
  the lookup completes; the dial completing **inside** the request would satisfy
  it equally, and at the default level the spec predicts exactly that, since the
  lookup finishes about 1.7 seconds into a download lasting several seconds.
- **Nothing about the pre-registered negative**, that the triggering download
  does not get faster. That is arm 4, which needs before and after in one
  session and therefore the alternating build design. Affordable, at about half
  an hour of recovery per run, and not yet run.
- **Nothing about a later download using the provider**, arm 3, which is where
  any value would be and which the spec allows to come back negative.
- **Nothing about the cache warming**, arm 5.
- **Nothing measured on the hinted path at all.** `hinted_connects_started`
  stayed 0 throughout, which simply means no arm here used a hint.

## The before state is from another session, and that is a deviation

The before numbers are the run recorded on
[#369](https://github.com/crtahlin/wasp/issues/369#issuecomment-5739263845): the
lookup cancelled 3 of 3 at the default level and 0 of 3 at level NONE. That
session used a **different observable**, the `node/providers` log line, because
these counters did not exist then.

The spec forbids before and after in different sessions. The deviation is taken
deliberately and is argued in the spec rather than passed over: **arm 1's
observable is not wall time.** Whether a lookup was cancelled is a structural
consequence of which context it inherited, and it does not vary with how warm
the node is. The same-session rule stands unchanged for arms 3 and 4, whose
observables are timing and credit.

## Why the alternating design was abandoned, which is a fact about the bench

The first harness swapped the two builds between runs so that before and after
shared a session, as the spec asks. That is not affordable here.

**Every restart of the requester leaves the local store dirty and triggers a
recovery.** The node comes back logging `localstore sharky .DIRTY file exists:
starting recovery` and rebuilds before `/readiness` reports ready or a single
peer reconnects. Measured on a store of about 2.5 million chunks: still
`notReady` with **zero peers after five and a half minutes**, then ready with 79
peers about 47 seconds after the rebuild finished. So a restart costs roughly six
minutes, and the design needed six of them per run, about half an hour of
recovery in a run that is otherwise a few minutes of work.

**An earlier version of this section called that a risk to the data and stopped
on those grounds. That premise was wrong and is withdrawn.** These are experiment
nodes: they are not playing the redistribution game and their stores are not
precious, so a dirty store and a rebuild cost time and nothing else. The
constraint is cost, not risk, which means the alternating design is affordable
where it is actually needed rather than ruled out. It is still not needed for arm
1, whose observable is structural, so this arm's result stands as it is; it is
needed for arm 4, and that arm is worth the half hour.

The spec records the cost so that the next person budgets for it rather than
discovering it mid-run.

## Three harness faults, all caught by gates, none of which existed at first

The pattern from the sibling experiment repeated, and the gates added in response
to it are what stopped each of these becoming a result.

**A postage batch that the provider no longer had.** The first run took
`BATCH_A` from a stored configuration file. Every upload returned `batch with id
not found`, every reference came back empty, every announcement answered 404, and
all twelve downloads then answered 404 **in 0.01 seconds**. Twelve rows of
nothing. It was obvious only because the times were instant: a batch that existed
but was wrong would have produced something that looked like a result. The batch
is now read from the provider at run time.

**No gate on the first upload.** That run performed twelve uploads and twelve
downloads without ever checking that the first upload had produced a reference.
One small upload must now succeed before anything long starts, and the run
refuses to download unless every reference and every announcement came back good.
That gate immediately earned itself: on the next attempt one upload in twelve
failed transiently, and the run stopped instead of reporting two downloads as
failures. Uploads are now retried before a run is abandoned over one.

**Waiting on `/health` rather than readiness.** `/health` answered four seconds
after a restart while the node went on to spend minutes in recovery. The run
disconnected the provider and downloaded against a node with almost no peers: ten
of twelve downloads returned **HTTP 503 in 0.01 seconds**, on both builds, with
the counter reads coming back empty for the same reason. A harness must wait for
`/readiness` and for the peer count to return, and must record a 503 as an
invalid run rather than as a result.

## What would settle the rest

Arms 2 and 6 need no build swap and are the ones that test the lifetime claim
rather than the lookup. Arm 6 is the sharper of the two, because the hinted
request is short and is not repeated, so the dial is certainly still running when
the request ends. Both need the ordered three-leg conjunction the spec sets out,
which needs `/peers` and `/metrics` polled once a second through the interval.

Arm 4 needs the before build, and so needs either a second requester or an
accepted cost of one restart pair.

---

Generated with help of AI.
