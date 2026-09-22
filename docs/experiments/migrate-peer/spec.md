# A failed migration should not leave two peers on one beneficiary

Issue: [#430](https://github.com/crtahlin/wasp/issues/430).
Type: fix.

> ## Withdrawn at implementation: the reordering is worse than the defect
>
> **The change this spec proposes was implemented, reviewed, measured against
> the code, and withdrawn before it reached `main`.** It is not a smaller
> improvement than claimed. It replaces a failure the node recovers from with
> one it cannot recover from at all. Everything below the withdrawal is the
> original argument, kept because the reasoning is what turned out to be
> wrong and deleting it would hide that.
>
> **What the spec missed.** The whole argument rests on one sentence, that a
> beneficiary mapped to nobody "is an availability gap that the next
> announcement repairs, because `PutBeneficiary` is the same call that
> established it". The announcement never reaches `PutBeneficiary`.
> `pkg/settlement/swap/swap.go:255-271` reads the **reverse** mapping first:
>
> ```go
> oldPeer, known, err := s.addressbook.BeneficiaryPeer(beneficiary)
> if known && !peer.Equal(oldPeer) {
>     return s.addressbook.MigratePeer(oldPeer, peer)   // taken every time
> }
> _, known, err = s.addressbook.Beneficiary(peer)
> if !known {
>     return s.addressbook.PutBeneficiary(peer, beneficiary)  // never reached
> }
> ```
>
> `MigratePeer` deletes only the forward key, so after a delete-first migration
> whose put fails, the reverse key still names the old peer while the old
> peer's forward key is gone. Call that state **the wedge**: a reverse mapping
> naming a peer that has no forward mapping. Every later handshake takes the
> migrate branch and `MigratePeer` returns `old beneficiary not known` from its
> own guard at `addressbook.go:67-69`.
>
> **That is not a missed payment.** `Handshake` is reached from
> `swapprotocol`'s `init`, which is registered as both `ConnectOut` and
> `ConnectIn` (`swapprotocol.go:101-102`, calling `Handshake` at `:109`), and
> libp2p disconnects the peer when either returns an error
> (`pkg/p2p/libp2p/libp2p.go:693` inbound, `:1220` outbound). The node cannot
> complete a swap handshake with that peer again, across restarts.
>
> **Correction, found by review of this withdrawal.** An earlier version of
> this section said "nothing in the repository ever deletes
> `beneficiaryPeerKey`" and that `addressbook.go:80` is "the only delete of a
> forward beneficiary key in the repository". **Both are false**, and saying so
> here matters more than usual, because the lesson this withdrawal draws is
> that the original spec asserted something about the code without checking it.
>
> `ClearForHopping` (`pkg/statestore/storeadapter/storeadapter.go:201-217`)
> preserves only `swap_chequebook`, `batchstore` and `transaction`, and
> bulk-deletes the rest. It therefore deletes **both** beneficiary keys, and
> `swap_chequebook_peer_` survives only because the preserve list carries the
> `swap_chequebook` prefix. So the wedge does have a clearing path: an operator
> restarting the node with a new `target-neighborhood`
> (`pkg/node/node.go:540-559`). `bee db nuke` is not one, because `Nuke`
> preserves the whole `swap` prefix, but `bee db nuke --forget-overlay` empties the
> statestore directory and is. (`removeContent` deletes the directory's
> contents rather than the directory itself, and returns before `Nuke` is
> reached, `cmd/bee/cmd/db.go:829-839`.)
>
> None of that is a repair a node performs for itself, so the substance stands:
> nothing the node does on its own recovers from the wedge.
>
> **The shipped order recovers from every one of these failures**, which is
> what the spec should have checked and did not.
> `TestMigratePeerPartialWriteLeavesTheHandshakeAbleToRepair` injects a store
> failure and then asks the real `swap.Service.Handshake` to put the
> addressbook right. It does, in every case. Reapplying the reordering makes
> the beneficiary case fail with `old beneficiary not known`, which is the
> mutation that decides this.
>
> **`MigratePeer` makes six store mutations, not four**, and the test fails
> each of them in turn:
>
> | | write | injected by |
> |---|---|---|
> | 1 | `PUT swap_peer_beneficiary_<new>` | `failPut: "peer_beneficiary"` |
> | 2 | `PUT swap_beneficiary_peer_<ba>` | `failPut: "beneficiary_peer"` |
> | 3 | `DELETE swap_peer_beneficiary_<old>` | `failDelete: "peer_beneficiary"` |
> | 4 | `PUT swap_chequebook_peer_<new>` | `failPut: "chequebook_peer"` |
> | 5 | `PUT swap_peer_chequebook_<cb>` | `failPut: "peer_chequebook"` |
> | 6 | `DELETE swap_chequebook_peer_<old>` | `failDelete: "chequebook_peer"` |
>
> **A second correction belongs here, for the same reason as the first.** An
> earlier version of this section said the two reverse puts, 2 and 5, "share no
> substring that distinguishes them from the forward ones" and left them
> untested on that basis. That is false: the prefixes are mirrored, so
> `peer_beneficiary` selects write 1 and `beneficiary_peer` selects write 2,
> and the chequebook pair works the same way round. The test already relied on
> exactly that discrimination to select writes 1 and 4.
>
> Write 2 then turned out to be the most valuable case of the six, because
> failing it produces the wedge by the shortest route: the new peer's forward
> mapping is written while the reverse mapping still names the old peer. The
> shipped order survives it, because the old peer's forward mapping has not
> been deleted yet and the next handshake finishes the job. Failing write 5
> catches a reordering of the two writes inside `PutChequebook`, which nothing
> else did.
>
> **The recovery repairs the beneficiary mapping only.** `Handshake` re-runs
> `MigratePeer` just when the reverse beneficiary mapping still names somebody
> else, so a migration that failed after the beneficiary half completed looks
> settled and the chequebook half is never finished. The new peer is then left
> with no chequebook, which `ReceiveCheque` treats as not known and fills in
> from the next cheque it accepts (`swap.go:110-115`). That is a gap which
> closes, not a wrong value, and the test asserts it per case rather than
> assuming it.
>
> That last clause is the whole reason the unfinished half is acceptable, and
> it was itself untested: deleting the `if !known` branch from `ReceiveCheque`
> left every package under `pkg/settlement` passing.
> `TestReceiveChequeRecordsAnUnknownChequebook` now pins it.
>
> **So the issue is closed without a code change**, and what remains of it is
> recorded rather than dropped:
>
> - The double mapping the issue describes is real, but its original
>   consequence has already been removed by
>   [#317](https://github.com/crtahlin/wasp/issues/317)'s per-beneficiary lock
>   around the cumulative payout, as the *Relationship to #317* section below
>   already said. What is left is two overlays resolving to one chequebook,
>   which the per-beneficiary lock serializes correctly.
> - The wedge is **not reachable on the shipped order**, and this spec says so
>   rather than leaving the withdrawal sounding like a bug report.
>   `MigratePeer` is the only code that deletes a forward beneficiary key by
>   key, and it always writes the reverse mapping before that delete, so it
>   cannot produce the wedge.
>
>   The bulk path needs its own sentence, since it deletes both keys.
>   `collectKeysExcept` iterates with `storage.Query` carrying no `Order`, and
>   `KeyAscendingOrder` is that field's zero value
>   (`pkg/storage/storage.go:68-69`). A zero value alone does not establish
>   that the production store sorts, so the rest of the chain was checked too:
>   `leveldbstore.Iterate` uses `nextF := iter.Next` and only reverses for
>   `KeyDescendingOrder` (`pkg/storage/leveldbstore/store.go:305`), goleveldb's
>   iterator is key-ordered, `cache.Cache` does not override `Iterate` so it
>   passes through, and `InitStateStore` always builds a leveldbstore. So the
>   keys do come back sorted, and `swap_beneficiary_peer_` sorts before
>   `swap_peer_beneficiary_`. `deleteKeys` then deletes them in that order, one
>   at a time with no batch, so a crash part way through loses the reverse key
>   first, which is the harmless direction.
>
>   **That is incidental and nothing enforces it**: renaming either prefix
>   reverses the sort and makes the wedge reachable through an interrupted hop,
>   and no test covers the dependency. The preserve entry that saves
>   `swap_chequebook_peer_` is incidental in the same way, since its intended
>   target is the single key `swap_chequebook`
>   (`pkg/settlement/swap/chequebook/init.go:25`) and the peer mapping survives
>   only by sharing that prefix.
>
>   So the ordering is load-bearing in two places, undocumented in both, and
>   unrecoverable by the node if violated. That is hardening rather than a
>   defect, and it is
>   [#462](https://github.com/crtahlin/wasp/issues/462), with the reproduction
>   from here.
> - The tests are kept. They pin the recovery property, and they cover the two
>   reverse mappings, which had no coverage at all: deleting the reverse write
>   from `PutBeneficiary` left the whole package passing.
>
> **The general lesson, since this is the second withdrawal in this area.** The
> spec argued from which *state* a failure leaves behind and never asked which
> states the node can *get out of*. For anything without a transaction that is
> the only question that matters, and it is answered by driving the real
> recovery path, not by reading the write order.

## Problem

`MigratePeer` moves a beneficiary from one peer to another with separate
writes and no transaction, so a failure between them leaves **both peers mapped
to the same beneficiary, durably** (`pkg/settlement/swap/addressbook.go:62-94`):

```go
	if err := a.PutBeneficiary(newPeer, ba); err != nil {
		return err
	}

	if err := a.store.Delete(peerBeneficiaryKey(oldPeer)); err != nil {
		return err
	}
```

`PutBeneficiary` itself writes two keys (`:145-151`), the forward
`peerBeneficiary[peer]` and the reverse `beneficiaryPeer[beneficiary]`, so the
window is wider than the two lines above suggest.

Two peers resolving to one beneficiary means two payment paths pointing at one
chequebook. The per-peer gate in `settle` is keyed by overlay and does not
serialize them.

## What this spec does not do

**It does not make the migration atomic, and cannot without a larger change.**
`storage.StateStorer` (`pkg/storage/statestore.go:15-29`) offers only `Get`,
`Put`, `Delete`, `Iterate` and `Close`. There is no batch and no transaction, so
"write both or neither" is not expressible against this interface.

Adding one is the real fix and is out of scope here: it changes an interface
with several implementations and every caller of the state store, for a defect
that reaches one function. That trade should be made on its own issue,
not smuggled into this one. What it would buy is stated at the end so the next
person does not have to work it out again.

## The change: make the durable failure the harmless one

Delete the old mapping **before** writing the new one, rather than after.

The failure modes are not symmetric, which is the whole argument:

| order | durable state after a failure between the writes |
|---|---|
| today, put then delete | **both** peers map to the beneficiary |
| after, delete then put | **neither** peer maps to it |

Both are wrong. Only one is dangerous. Two payment paths onto one chequebook is
a correctness hazard; a beneficiary briefly mapped to nobody is an availability
gap that the next announcement repairs, because `PutBeneficiary` is the same
call that established it in the first place.

The chequebook pair moves the same way, for the same reason.

### A retry converges, checked rather than assumed

The reordering is only useful if running the migration again finishes the job.
It does: `Delete` on a missing key is a no-op in both implementations,
goleveldb's at `pkg/statestore/leveldb/leveldb.go:110-112` and the mock's at
`pkg/statestore/mock/store.go:64-70`. So a migration interrupted after the
delete can be re-run without a special case.

### The reverse mapping, stated because it is still imperfect

Between the delete and the put, `beneficiaryPeer[ba]` still names the old peer
while `peerBeneficiary[oldPeer]` is gone. That is inconsistent. It is not a
double mapping, and the put that follows overwrites it.

Saying this plainly matters more than the fix: the point of the reordering is
that it moves the residue to somewhere harmless, not that it removes it.

## What it costs

A migration that fails part way now leaves the beneficiary unresolved rather
than doubly resolved, so a payment to that peer fails until the addressbook is
repopulated, where before it would have been made, possibly twice, onto one
chequebook. That is the trade and it is deliberate.

Nothing changes when the migration succeeds, which is every case where no write
fails.

## Relationship to #317

[#317](https://github.com/crtahlin/wasp/issues/317) was filed and fixed on the
understanding that two peers cannot share a beneficiary. That turned out not to
be an invariant, which is what produced this issue, and the correction is
recorded on #317 already.

With #317's per-beneficiary lock merged the cumulative payout allocation is safe
whether or not two peers share a beneficiary, so **this issue is no longer
load-bearing for that one**. It is wrong on its own terms and is fixed on those.

## Protocol impact

None. Local state, no wire component.

## Tests

In `pkg/settlement/swap`, mutation checked: restore the old order and confirm
the test fails.

- **A failure at the delete leaves no peer mapped to the beneficiary**, and in
  particular does not leave both. Driven by a store whose `Delete` fails once.
  This is the defect; it fails against the current order.
- **A failure at the chequebook write leaves the same property** for the
  chequebook pair.
- **A successful migration moves both mappings and leaves nothing on the old
  peer**, so the reordering did not simply drop a write.
- **Re-running an interrupted migration completes it**, which is what makes the
  chosen failure mode recoverable rather than merely different.

A failing store is needed for all of these; `pkg/statestore/mock` does not
support injected errors, so the test supplies its own wrapper around it rather
than changing the shared mock.

## Measurement

None. The claim is about which state a failed write leaves behind, which a unit
test with an injected failure states exactly. Rule 7 is for claims about how a
node performs.

## Rollout and rollback

No migration and no on-disk change: the same keys are written, in a different
order. Rollback is reverting the merge commit.

## Upstream portability

**Verified.** `git show upstream/v2.8.2:pkg/settlement/swap/addressbook.go`
carries the same `MigratePeer` at its lines 62 to 94, byte for byte, with the
same put-then-delete order, and upstream's `StateStorer` has no batch either.

The issue carries `affects-upstream`, which per rule 11 is a marker for a later
human decision and nothing more.

## Files

- `pkg/settlement/swap/addressbook.go`, `MigratePeer`.
- `pkg/settlement/swap/addressbook_test.go`.
- `docs/DIFFERENCES.md`: a node leaves different state behind after a failed
  migration than Bee does.
- `docs/UPSTREAM.md`: the #430 row gains its branch and merge commit.

Generated with help of AI.
