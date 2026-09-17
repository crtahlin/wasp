# Carrying the preferred set into erasure-coded downloads

Issue: [#299](https://github.com/crtahlin/wasp/issues/299). This is phase 2 of
content providers ([spec.md](spec.md)), separated into its own document because
it is the one phase-2 item that decides whether phase 1 works at all for
ordinary content.

## Problem

**Content providers phase 1 reaches the root and intermediate chunks of a
default upload and almost none of its data.** Uploads are erasure coded by
default, and the data shards of erasure-coded content are fetched from a context
that does not carry the preferred set, so they never try the hinted or
discovered provider.

Every step below was checked against the tree at `6eaaa651`.

- **The default upload is erasure coded.** `DefaultUploadLevel = MEDIUM`
  (`pkg/file/redundancy/level.go:181`).
- **Data shards are prefetched from a fresh context.** Creating a decoder starts
  `go d.prefetch()` at once (`pkg/file/redundancy/getter/getter.go:81`). With
  the default strategy, `DefaultStrategy = DATA`
  (`pkg/file/redundancy/getter/strategies.go:17`), the prefetch fetches every
  data shard from `context.WithCancel(context.Background())` (`getter.go:242`),
  and each fetch derives from that (`getter.go:130`, `:146`).
- **The preferred set lives in the request context.**
  `retrieval.WithPreferredPeers` stores it as a context value
  (`pkg/retrieval/preferred.go:142-144`), set once per download at
  `pkg/api/providers.go:98`. A background context carries no values, so
  `PreferredPeers` returns nil (`preferred.go:147-150`) and the preference
  branch at `pkg/retrieval/retrieval.go:171-175` is skipped entirely.
- **Plain content is not affected.** `GetOrCreate` returns the plain fetcher
  when there are no parity shards, `len(addrs) == shardCnt`
  (`pkg/file/joiner/joiner.go:92-94`), so non-erasure-coded downloads keep the
  request context throughout.

What that costs, in the two things phase 1 was built for:

- **Speed.** Preference applies to the root and intermediate chunks only, a
  small fraction of a file. A 4 MiB default upload is 1,120 chunks against 1,033
  without erasure coding, and all but the trie chunks go through normal
  retrieval.
- **Availability.** For erasure-coded content the network has lost, a hinted
  download is expected to fail even though the provider holds every chunk. The
  trie comes from the provider; the data does not.

**What this does not explain.** The sole-source failure in
[#313](https://github.com/crtahlin/wasp/issues/313) was measured on a plain copy
as well as an erasure-coded one, and both failed. By `joiner.go:92-94` the plain
copy never touches a decoder, so #313 has a separate cause and this change must
not be presented as fixing it.

## Hypothesis

Re-attaching the preferred set to fetches that arrive without one will make the
provider serve the data shards of erasure-coded content as it already serves the
trie chunks.

**Expected effect, stated before measuring.** The provider's share of a default
upload should rise from the trie chunks alone, a few per cent, toward the share
measured for plain content in phase 1. It should **not** reach 100%: phase 1
measured the provider serving about 4.3% of a plain download at the shipped
payment threshold, because the per-peer credit window, not the preference, is
what bounds a single peer's share
([results.md](results.md)). This change removes a reason the provider is not
asked; it does not change what happens once it is asked.

So the honest prediction is that the erasure-coded share moves from near zero to
about what plain content already gets, and that the total download time barely
moves, because phase 1 showed a provider contributes 3% to 5% of a download and
the spreads overlap. **The availability case is where the effect should be
visible**, and it is the one worth measuring.

## Design

**One change, in a file this fork owns.** `providerGetter`
(`pkg/api/providers.go:109-122`) already wraps the getter that the decoder
fetches through. The chain, each link verified:

1. `pkg/api/bzz.go:782` passes `s.providerGetter(ctx, s.storer.Download(cache))`
   into `joiner.New`.
2. `NewJoiner` hands that getter to `NewDecoderCache` as `fetcher`
   (`pkg/file/joiner/joiner.go:184`, `:52-59`).
3. The decoder calls `g.fetcher.Get(fctx, g.addrs[i])` (`getter.go:146`).

So every data-shard prefetch already passes through fork code. The wrapper
forwards whatever context it is called with; it should instead re-attach the set
when the incoming context does not carry one:

```go
return storage.GetterFunc(func(ctx context.Context, addr swarm.Address) (swarm.Chunk, error) {
    if retrieval.PreferredPeers(ctx) == nil {
        ctx = retrieval.WithPreferredPeers(ctx, hint.set)
    }
    ...
    return g.Get(ctx, addr)
})
```

**Why the guard is not optional.** The lookup's own reads deliberately run with
the set cleared, `retrieval.WithPreferredPeers(ctx, nil)`
(`pkg/api/providers.go:118`), so that discovering providers does not itself go
through this download's providers. `PreferredPeers` returns nil both for "never
set" and for "set to nil", so a plain nil check cannot tell them apart and would
re-attach the set to exactly the reads that must not have it. The implementation
must distinguish the two, either by storing a marker alongside the value or by
checking a separate context key that `providers.go:118` sets. **The
implementation must say which, and a test must cover the lookup path.**

**Three things this deliberately does not do.**

- **It does not touch `pkg/file/redundancy/getter`.** That package is unmodified
  upstream code. Both options in #299 changed it; neither needs to.
- **It does not change the decoder's cancellation.** `getter.go:242` detaches
  from the request on purpose, so a prefetch outlives the read that started it.
  Re-attaching one context value adds nothing to that lifetime and must not be
  turned into a cancellation change.
- **It does not define whose set wins.** #299 asked the spec to settle that,
  on the premise that decoders are shared between requests. **That premise is
  wrong.** `NewDecoderCache` has one caller, inside `NewJoiner`
  (`joiner.go:184`), so each download has its own cache, its own map and its own
  config. What is shared between downloads is the `PreferredSet` itself, through
  `providerSet` keyed by content key (`providers.go:129-134`), which is
  deliberate and already specified.

## What this risks

- **More load on one provider.** The provider goes from serving the trie to
  being asked for every data shard. It is asked first, not exclusively, and the
  credit window still bounds what it can serve, but an operator who announced
  content should expect more requests than phase 1 produced. This is the change
  working, not a fault, and the operator documentation must say so.
- **A slow provider now sits in front of far more chunks.** The 500 ms
  `preferredWait` (`pkg/retrieval/preferred.go:36`) applies per chunk before the
  normal peer selection starts. Applying preference to about 1,100 chunks rather
  than a handful multiplies any per-chunk delay the preference adds. The
  measurement must therefore report total download time and not only the
  provider's share, because this change could make a download slower.
- **Re-attaching to the wrong reads**, if the guard above is got wrong. The
  failure is subtle: provider discovery would query through the providers it is
  trying to discover.

## Protocol impact

**None.** No wire format, protocol ID, handshake or message changes. This alters
which peer a requester asks first for a chunk it is already entitled to request,
using the existing retrieval protocol and the existing local-only header. The
frozen surface is untouched and `make protocol-freeze` must pass with the
fingerprint unchanged.

No peer-visible behaviour changes for a stock node beyond receiving more
retrieval requests from a wasp requester that hinted it, which stock nodes
already serve.

## Measurement

On the bench, following [test-bench.md](../../agent-playbooks/test-bench.md) and
the existing #290 harness: provider and requester on separate machines, about
30 ms added between them, fresh 4 MiB files.

Conditions, interleaved within each cycle, never in blocks:

1. **Default upload level, stock build.**
2. **Default upload level, patched.**
3. **No erasure coding, stock build**, as the control that should not move,
   since `joiner.go:92-94` keeps plain content off the decoder path.
4. **No erasure coding, patched**, likewise.

At least three runs per condition, reported with the spread (rule 7). Record for
every run: the provider's preferred attempts, hits and misses; chunks delivered
by the provider against total chunks; total download time and time to first
byte; the requester's accounting balance with the provider; and the upload
level in force. Write all of them into the results file rather than collecting
them, which is the mistake the cheque experiment made.

**The availability arm is the one that matters.** Repeat the
[#313](https://github.com/crtahlin/wasp/issues/313) setup with an erasure-coded
copy: content whose batch has expired, held complete by the provider, announced
with a live batch. Stock is expected to fail. If patched also fails, this change
does not deliver the availability half of phase 1 and #313's cause dominates,
which is a useful result and must be recorded as one.

**A negative result** is the provider's share of a default upload not rising
beyond the spread. That would mean the set is still not reaching the fetches,
and the next step is to log the preference decision per chunk rather than to
change more code.

## Acceptance

**Accept** if the provider's share of an erasure-coded download rises beyond the
spread, toward the share already measured for plain content, **and** total
download time does not rise beyond the spread. The second clause exists because
of the `preferredWait` risk above: a change that raises the provider's share
while making downloads slower has not delivered what phase 1 was for.

**Accept separately, and report separately**, the availability arm: content the
network has lost is retrieved when erasure coded, where stock fails.

**Expect the speed effect to be small or absent.** Phase 1 measured a provider
contributing 3% to 5% of a download with overlapping spreads, and the credit
window rather than the preference is what bounds a peer's share. A null speed
result alongside a working availability result is the predicted outcome and
closes this issue.

## Test plan

Unit tests in `package api_test` and `package joiner_test`:

- a fetch arriving with no preferred set in its context is given the download's
  set;
- a fetch arriving with the set explicitly cleared, as the lookup at
  `providers.go:118` does, is **not** given it back;
- a fetch arriving with a different set keeps its own;
- a download with no provider hint is unchanged in behaviour;
- an erasure-coded download asks the provider for data shards, not only trie
  chunks, asserted through the preferred attempt counters;
- a plain download's behaviour is byte-identical to stock, since it never
  reaches a decoder.

Plus the mixed-version check in [mixed-version.md](mixed-version.md), showing a
stock provider serves the additional requests and blocklists nobody.

## Configuration

No new setting. This fixes a path that was meant to work, rather than tuning a
constant, so rule 8 does not apply. `providers-enable` already gates the whole
feature and continues to.

`preferredWait`, 500 ms (`pkg/retrieval/preferred.go:36`), becomes materially
more important with this change, because it now applies to every data shard
rather than to trie chunks alone. It stays a compiled-in constant here. If the
measurement shows total download time rising, exposing it becomes a candidate
under rule 8, with its own issue and the current value as the default, and this
spec should be cited as the measurement that justified it.

## Upstream portability

**Nothing to port, and that is the point of the design.** The change is confined
to `pkg/api/providers.go`, which is fork-authored and has no upstream
counterpart, so `pkg/file/redundancy/getter` and `pkg/file/joiner` stay
byte-identical to `upstream/v2.8.2`.

No `affects-upstream` marker applies. Under rule 11 this is not an upstream
defect: upstream has no preferred-peer concept, so a background prefetch context
loses nothing there. Detaching the prefetch from the request is a deliberate
upstream design choice that suits upstream, and calling it a defect because our
feature needs a context value would be tagging a preference.

Anyone adopting content providers wholesale would take `providers.go` with the
rest of the feature and get this behaviour with it.

## Rollout and rollback

- Nothing new to turn on. The change takes effect for downloads that already
  carry a provider hint, on nodes that already run with `providers-enable`.
- Rollback is a revert of the single commit.
- No migration, no on-disk format change, no stored state.

---

Generated with help of AI.
