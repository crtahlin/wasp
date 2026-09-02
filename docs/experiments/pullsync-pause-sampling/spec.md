# Pause pullsync during reserve sampling

Issue: [#23](https://github.com/crtahlin/wasp/issues/23)

## Problem

The redistribution game makes each node compute a reserve sample once per round.
The sample reads across the whole within-radius reserve, and it must finish inside
the round's commit window. While it runs, pullsync keeps writing pulled chunks into
the same store, and those writes compete with the sample for CPU and disk, which
slows the sample and, on goleveldb, triggers compaction that competes harder. The
proposal is to pause pullsync for the duration of the sample so the sample runs
against a quiet store.

## Network impact: uploads are not affected

The one thing that must not happen is stalling uploads. It does not.

- **pushsync and pullsync are separate protocols** (`pkg/pushsync`, `pkg/pullsync`),
  with separate stream handlers. Pausing pullsync stops the node pulling historical
  chunks from neighbours; it does nothing to pushsync, which keeps storing uploaded
  chunks through `ReservePutter().Put`. Uploads continue during sampling.
- **The reserve has no global lock.** It guards writes with a per-key `multex`,
  locked per batch ID and per bin, and every operation runs through the transaction
  layer, where the storage engine allows concurrent reads and writes. The sample
  holds no lock that blocks `Put`. So even today the sample does not block pushsync
  writes; it only competes for CPU and disk, and pausing pullsync reduces that
  competition without touching the upload path.
- **Retrieval is unaffected.** It is a third, separate protocol and is not touched.

The only thing paused is historical replication (pullsync), and only for the sample
window, roughly 50 to 150 seconds per round on the bench nodes. The node falls
briefly behind on pulling historical chunks and catches up after the sample. It
does not lose data, miss uploads, or stop serving retrievals.

## Evidence that the contention is real

The read-under-write measurement in
[`../storage-engine-eval/results.md`](../storage-engine-eval/results.md) shows a
full index scan slowing under a concurrent write load: at 20,000 writes per second
goleveldb's scan grew from about 2.2 to 4.4 seconds and Pebble's from about 1.6 to
3.0 seconds, against roughly 1.8 seconds with no write load. Live pullsync rates are
far lower than that, so the effect on a real node is smaller, but it is in the same
direction, and removing it is free for the sample.

## Design

Keep the two services decoupled. The storer already runs the sample, so it is the
natural place to publish a "sampling in progress" signal, and the puller already
holds a storer interface (`storer.RadiusChecker`) it can extend to read that signal.

1. The storer exposes a sampling-state signal, set on entry to `ReserveSample` and
   cleared on exit (including on error and context cancellation, so a failed sample
   never leaves pullsync paused).
2. The puller reads that signal in its sync loop. While sampling is in progress it
   stops issuing new pull requests and lets in-flight ones drain; when the signal
   clears it resumes. Pausing is preferable to dropping the rate limiter to zero
   because it leaves the existing `PullSyncMaxChunksPerSecond` limiter untouched for
   the normal path.
3. The pause is bounded by the sample itself. If a sample hangs, the same context
   that bounds the sample bounds the pause, so pullsync cannot be paused
   indefinitely.

The wire protocol does not change; this is local scheduling only.

## Protocol impact

None. No constant in `.github/protocol-freeze.lock` changes, no message format
changes, and a paused node still speaks pullsync and pushsync identically to a stock
peer. It only chooses not to originate pull requests for a short window. The
`protocol-change` label is not required.

## Redistribution and stake

This changes when the node does its historical pulling, not whether it plays the
game. It should make the sample faster and more reliable inside the commit window,
which helps rather than harms participation. Because it touches the incentive path,
the change is gated on the operator's sign-off before it lands on a staked node, per
fork rule 4.

## Verification

1. A unit test that pullsync is paused while the sampling signal is set and resumes
   when it clears, including that an error or cancelled sample clears the signal.
2. On the bench, a sample run with pullsync active against one with pullsync paused,
   at a matched peer count and pull rate, three runs each with the spread, to confirm
   the sample is at least no slower and to size the gain. The `rchash` endpoint and
   the `evictbench` readwrite harness both already measure this shape.

## Upstream portability

Cleanly upstreamable. Pausing background pulling during the incentive sample is a
general improvement with no fork-specific assumption, worth offering to Ethersphere.
