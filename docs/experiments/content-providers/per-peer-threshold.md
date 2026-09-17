# Extending a larger payment threshold to one peer at a time

Issue: [#327](https://github.com/crtahlin/wasp/issues/327). Analysis:
[per-peer-credit.md](per-peer-credit.md), merged as the argument this spec
implements.

Code references are to `main` at `a8772819`, base `upstream/v2.8.2`.
`pkg/accounting` and `pkg/pricing` are byte-identical to that base, so every
line cited in them is also upstream's.

## Problem

A provider serves a few per cent of a download and then runs out of credit with
the requester.

**The term.** The *payment threshold* a node grants a peer is how much that peer
may owe it before it stops serving them. It is `paymentThresholdForPeer` on the
per-peer accounting record, and a peer's next request is refused with
`ErrOverdraft` once its balance would cross that value plus at most one second
of the free refresh allowance
(`pkg/accounting/accounting.go:301-322`).

Two measurements, both recorded, say this is the binding constraint.

**Raising the threshold raises what a provider delivers.** From
[results.md](results.md) Table 6, three runs per step except the last, which has
two:

| Threshold granted | Chunks from the provider | Share of the download |
|---|---|---|
| 13,500,000, the default | 176 (164-235) | 4.3% |
| 27,000,000 | 453 (407-486) | 11.1% |
| 54,000,000 | 1,261 and 1,405 | 31% and 34% |

Read those shares with the caution results.md attaches to them. Credit available
over a download is a fixed amount plus a part that grows with how long the
download lasts, so counts from different blocks are not comparable and a slower
block flatters the same threshold. The direction is what this establishes.

**And credit is what stops the sole-source case.** With the
[#324](https://github.com/crtahlin/wasp/issues/324) fix in the requester, so
with a single refusal lasting 600 ms no longer removing the only holder of a
chunk for good, content only the provider holds still truncates at the shipped
lookahead buffer:

| Runs | Bytes of 4,194,304 | Time | Rate | Overdrafts |
|---|---|---|---|---|
| 3 of 3 truncated | 1,310,720, so 31.2% | 1.34 to 1.42 s | 924,411 to 976,922 B/s | 311 to 429 |

The same build with `Swarm-Lookahead-Buffer-Size: 0`, which reads one chunk at a
time instead of prefetching, completes 6 runs of 6. So the prefetch breaks it
through credit: many chunks in flight means many refused at once, and each
refused chunk is then sought from peers that do not hold it.

**The node-wide setting is not the answer.** `payment-threshold` is one number
for the whole node, so a provider raising it extends that credit to every peer
it has, including peers downloading nothing it announced. That is why the ledger
row for [#290](https://github.com/crtahlin/wasp/issues/290) records a raised
threshold as "not a setting to recommend". It is also capped: a node refuses to
start when its configured `payment-threshold` exceeds `maxPaymentThreshold`, 24
times the refresh rate or 108,000,000 (`pkg/node/node.go:241`, `:806`).

## Hypothesis

Granting the larger threshold only to a peer that is asking this node as a
provider, and only while it is asking, recovers most of the delivered share that
a node-wide raise to the same value would give, at a fraction of the credit
exposure.

**Falsifiable.** If the per-peer arm delivers materially less than a node-wide
raise to the same value measured in the same session, the restriction is costing
the benefit and this design is the wrong one.

## Design

### 1. What the trigger can and cannot be

A requester asking a preferred peer attaches the `wasp-local-only` header to
every attempt (`pkg/retrieval/preferred.go:28`, sent at `:240`). So a peer
asking this node *as a provider* is distinguishable per request, with no new
message and no chunk-to-content index.

**But that header is not proof the peer is downloading content this node
announced, and this spec does not pretend otherwise.** The handler answers a
local-only request from `s.storer.Lookup().Get` (`pkg/retrieval/retrieval.go:554`),
which reads the whole chunk store, reserve and cache included. Any peer can
therefore obtain a hit by asking for any chunk this node happens to hold.

**Gating on the content instead is not cheap with today's indexes**, and that is
worth stating so it is not rediscovered. Announcing a reference requires it to
be pinned first (`pkg/api/providers.go:249-259`), so "pinned" is the right set.
But pinning is indexed by collection: `pinChunkItem` is namespaced by the
collection's UUID and keyed by the chunk address
(`pkg/storer/internal/pinning/pinning.go:420-430`), and `HasPin` takes a root
reference, not a chunk (`:202`). Asking "is this chunk pinned" therefore means
one index read per pinned collection, on the hit path of every retrieval. That
is not viable.

**So the abuse question is answered by bounding the exposure, not by verifying
the claim.** The four bounds, in order of how much they carry:

1. **The operator opts in.** The threshold this grants is a new setting whose
   default disables the behaviour entirely. Nothing changes for a node that does
   not set it.
2. **`providers-enable` must be on, and it is off by default**
   (`cmd/bee/cmd/cmd.go:415`, honoured at
   `pkg/retrieval/retrieval.go:171` and `:558`).
3. **A total credit budget**, below, caps what all raised peers together can be
   granted beyond the node-wide value. This is the bound that makes the
   unverifiable trigger acceptable: the worst case is a fixed number the operator
   chose, not a number that grows with how many peers try it.
4. **The existing local-only rate limits**, 100 misses a second per peer and
   1,000 a second in total (`pkg/retrieval/retrieval.go:119-120`). These bound
   the miss path only, not the hit path, so they are listed for completeness
   rather than as a real bound on this change.

**The hardening is a separate issue, deliberately.** The requester already knows
which root reference it is downloading, since the provider hint is attached per
content key (`pkg/api/providers.go:129-134`). It could send that root in a second
header, and the provider could check it with one `HasPin` read. That narrows the
trigger to content this node actually hosts. It is not in this spec because it
adds a header for a benefit that is only worth having if the measurement below
shows the feature is worth hardening. Filed separately, gated on that result.

### 2. Where the read is missing

The handler reads the local-only header only inside the `storage.ErrNotFound`
branch (`pkg/retrieval/retrieval.go:556-560`). On a hit, which is exactly when
the provider serves and credit is consumed, it goes straight to pricing at
`:572` without looking at the headers.

So the hit path needs the read. That is the one part of this change that does not
already exist, and it is small: one header test where the chunk was found,
before `PrepareDebit`.

### 3. Raising it

`paymentThresholdForPeer` and `disconnectLimit` are already per-peer fields,
initialised from the node-wide values on connect
(`pkg/accounting/accounting.go:1415-1433`), and they already diverge per peer
through the growth path, which recomputes `disconnectLimit` from the new
threshold and announces the result (`:657-662`). `AnnouncePaymentThreshold`
(`pkg/pricing/pricing.go:125-150`) is an existing message this node already
sends whenever that path fires.

So raising a peer's threshold is: set the two fields together, announce, and
account for the budget. Three details that a naive version gets wrong:

- **Set, never lower, relative to what is already there.** The growth path only
  ever raises `paymentThresholdForPeer`, by one refresh rate per payment sent
  (`:657`). The raise takes the larger of the configured provider value and
  whatever is already on the record, and remembers the value it replaced so the
  decay below can restore it rather than overwrite a legitimate growth.
- **`disconnectLimit` moves with it, always.** It is derived from the threshold
  (`:659`) and the two are set together everywhere they are set today. A raise
  that forgets it would blocklist exactly the peers it meant to help, because
  `PrepareDebit` blocklists a peer whose next balance would cross
  `disconnectLimit` (`:1343-1353`).
- **Budget admission is node-level state and needs the node-level lock**, not
  the per-peer one, or two concurrent first-requests from different peers can
  both be admitted against the same last slot of budget.

### 4. Lowering it again, which is the part with teeth

Nothing in the current code lowers `paymentThresholdForPeer`. Lowering is
peer-visible and it is where this design can hurt a peer it meant to help, so it
happens in two steps rather than one.

**Step one, on idle.** A peer that has made no local-only hit for
`providerCreditIdle` is no longer downloading from this node as a provider. Its
`paymentThresholdForPeer` returns to the remembered pre-raise value and that is
announced. The peer stops being able to accrue more before paying. A stock peer
adopts the lower number without complaint: the pricing handler validates an
announced threshold against a minimum only (`pkg/pricing/pricing.go:100-102`) and
`NotifyPaymentThreshold` stores whatever arrives
(`pkg/accounting/accounting.go:992-1001`).

**Step two, on settlement, and not before.** `disconnectLimit` stays at the
raised value until the peer's balance is back within the pre-raise limit.
Lowering it while the peer still owes more than it would mean the very next chunk
this node serves them crosses `disconnectLimit` and blocklists them for debt
they were invited to take on. The budget is released at this second step, not the
first.

**That is the honest accounting of the exposure**: it ends when the money
arrives, not when the download does. It also means a peer that never settles
holds its slice of the budget, which is the correct behaviour, since that is
precisely the risk the operator agreed to. The metric below makes it visible.

**`providerCreditIdle` is a compiled-in constant**, 60 seconds, not a setting.
Rule 8 wants measurement before surface area, and 60 seconds is chosen only so
that it cannot fire inside a download: the sole-source runs above finish in under
2 seconds and the 16 MiB runs in under 15. If the measurement shows the value
matters, it gets its own issue.

### 5. Where the code goes

One exported method on `*Accounting` in `pkg/accounting`, because the state it
touches lives on `accountingPeer` and nowhere else. The retrieval handler reaches
it through a small fork-authored interface declared in `pkg/retrieval`, rather
than by widening upstream's `accounting.Interface`, so that the next upstream
sync sees an added file and an added method and not a changed interface.

## What this risks

- **Unsecured credit.** The whole point. Bounded by the budget setting and
  reported by the metrics, but a peer that takes the credit and never settles
  keeps it.
- **A peer blocklisted by a mishandled decay.** Addressed by step two above, and
  it is the single most likely way to get this wrong. The test plan covers it
  directly.
- **Nothing for stock requesters.** A stock Bee node never sends the local-only
  header, so it never gets the raise. The analysis recommended leaving them out
  as the safer default and this spec takes that. Note that a stock node would
  *accept* a larger announced threshold, since neither the pricing handler nor
  `NotifyPaymentThreshold` imposes a maximum; not doing it is a choice, not a
  limitation.
- **Above 108,000,000 is not attempted.** Per-peer values can exceed
  `maxPaymentThreshold`, because that check runs only against the node's own
  configured value at startup, and the growth path already drifts past it without
  re-checking. This spec still refuses to go above it: nothing above it has been
  measured, and crossing a limit upstream chose needs a reason better than
  "possible". The setting is validated against it.

## Protocol impact

**None.** No wire format, protocol identifier, handshake or message change. The
threshold is announced with the existing `AnnouncePaymentThreshold` message,
carrying a value the receiving side already accepts without an upper bound. No
new stream header. `make protocol-freeze` must pass with the fingerprint
unchanged.

## Configuration

Two new settings, and the reasoning for why only two.

| Setting | Default | Meaning |
|---|---|---|
| `providers-payment-threshold` | `0`, disabled | The threshold granted to a peer making local-only hits. Validated as either 0, or at least the node-wide `payment-threshold` and at most `maxPaymentThreshold`. |
| `providers-credit-budget` | `0` | The most extra credit, summed over all raised peers, this node will have granted at one time. Extra means the raised threshold minus the value it replaced. Required to be non-zero when the threshold above is set. |

**What raising `providers-payment-threshold` costs:** more unsecured credit to
each admitted peer, and a longer wait before that peer is required to pay. What
lowering it costs: the provider delivers a smaller share of each download, which
is the 4.3% in the table above.

**What raising `providers-credit-budget` costs:** more peers hold raised credit
at once, so the worst case this node can lose without being paid grows in
proportion. What lowering it costs: provider downloads beyond the budget are
served at the ordinary threshold, so they get the 4.3% behaviour. The cost falls
on this operator only. No other node pays for either.

`providerCreditIdle` stays a constant, for the reason in Design (4).

## Measurement

On the bench, following [test-bench.md](../../agent-playbooks/test-bench.md).
The sweep already establishes the direction, so this does not re-establish it.
What is unmeasured is whether restricting the raise keeps the benefit.

Four arms, interleaved, at least three runs each, all in one session so the
block is shared:

1. **Control.** Stock thresholds, hint to the provider.
2. **Node-wide raise** to the same value as arm 3, set in the provider's
   configuration.
3. **Per-peer raise**, this change, same value.
4. **Sole-source at the shipped lookahead buffer**, this change, against the 3
   of 3 truncations in Problem.

Recorded per run: chunks delivered by the provider and its share, total time,
time to first byte, bytes and SHA-256, the `curl` exit code where 18 means
truncated, `bee_retrieval_preferred_overdrafts` and
`bee_accounting_accounting_blocks_count` on the requester, and on the provider
the balance owed by the requester at the end plus the new budget metrics.

**Pre-registered predictions.**

- Arm 3 delivers a share within the spread of arm 2. A materially lower share
  refutes the hypothesis.
- Arm 4 completes with a matching SHA-256, and
  `bee_retrieval_preferred_overdrafts` falls towards zero. **If the overdraft
  count does not fall, the threshold was not the constraint in that run and
  nothing else in this document follows.**
- Time to first byte does not move between arms 1, 2 and 3. The hint moves it,
  not the threshold, and crediting it to this change would be wrong.
- Total time is the primary figure and is reported with the control spread beside
  it. The sweep did not settle it.

**A negative result** is arm 4 still truncating, or arm 3 tracking arm 1 rather
than arm 2. Either closes #327 with the evidence.

## Acceptance

**Accept** if arm 4 completes with a matching SHA-256 where it truncated before,
**and** arm 3's delivered share is within the spread of arm 2's, **and** no arm
blocklists a peer.

**Reject** if arm 4 still truncates, or if arm 3 is closer to arm 1 than to arm
2, or if any peer is blocklisted during a decay.

## Test plan

Unit tests in `package accounting_test` and `package retrieval_test`:

- a local-only hit raises the peer's threshold and announces the new value, and
  `disconnectLimit` is raised with it;
- an ordinary hit, with no local-only header, raises nothing;
- a local-only **miss** raises nothing, so the miss path cannot buy credit;
- the raise is refused once the budget is exhausted, and the peer is then served
  at the ordinary threshold rather than refused service;
- two concurrent first requests from different peers cannot both take the last
  slot of budget;
- a peer whose threshold the growth path had already raised above the configured
  provider value is left alone, and after decay is restored to the grown value
  rather than to the node-wide one;
- after `providerCreditIdle` with no local-only hit, the threshold is lowered
  and announced, while `disconnectLimit` is not;
- **a peer in debt above the pre-raise limit is not blocklisted by the decay**,
  and `disconnectLimit` and the budget are both released only once its balance
  is back within that limit;
- the setting is refused at startup when it is below the node-wide threshold,
  above `maxPaymentThreshold`, or set with a zero budget.

Plus the mixed-version check against a stock v2.8.2 node, which must accept both
the raise and the later lowering and blocklist nobody.

## Upstream portability

**No `affects-upstream` marker.** The per-peer threshold machinery is upstream's
and works as intended; what is new is driving it from a fork-authored signal that
upstream has no equivalent of. `pkg/retrieval/preferred.go` does not exist
upstream and `providers-enable` is a fork setting. This is a fork feature, not a
Bee defect, so rule 11 does not apply.

## Rollout and rollback

- Off unless `providers-payment-threshold` is set, and it cannot be set usefully
  without `providers-enable`, which is also off by default.
- Rollback is unsetting the option. No migration, no on-disk format change, no
  stored state: the raised thresholds live in memory on the accounting record and
  are reinitialised from the node-wide values on reconnect (`:1415-1433`).
- A node restarted mid-download loses the raises, and the peer's next request is
  served at the ordinary threshold. That is the same behaviour as any other
  reconnect and needs no special handling.

---

Generated with help of AI.
