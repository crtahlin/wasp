# Consistent status codes when the chain is disabled

Issue: [#33](https://github.com/crtahlin/bee-experimental/issues/33)

## Problem

`chequebookBalanceHandler` in `pkg/api/chequebook.go` makes two calls into the
chequebook service and treats their failures differently.

The first is handled correctly:

```go
balance, err := s.chequebook.Balance(r.Context())
if errors.Is(err, postagecontract.ErrChainDisabled) {
	jsonhttp.MethodNotAllowed(w, err)
	return
}
```

The second is not:

```go
availableBalance, err := s.chequebook.AvailableBalance(r.Context())
if err != nil {
	jsonhttp.InternalServerError(w, errChequebookBalance)
	return
}
```

If `Balance` succeeds and `AvailableBalance` returns `ErrChainDisabled`, the
caller receives **500 Internal Server Error** where **405 Method Not Allowed** is
correct. 500 says "this node is broken"; 405 says "this node does not offer that
because the chain is disabled". An operator debugging a node started with
`--swap-enable=false` is told the wrong thing.

Confirmed present at `v2.8.1`. Related to upstream issue #5233.

## Hypothesis

The check was added to the first call when the bug was reported and not applied
to the second. Nothing about the second call makes `ErrChainDisabled` less
likely or less meaningful there.

## Design

Check `ErrChainDisabled` on the `AvailableBalance` error the same way, before
the generic error branch.

Deliberately kept to that. The handler's logging is duplicative — it calls both
`logger.Debug` and `logger.Error` for the same failure — but that pattern is
used throughout `pkg/api` and changing it here would mix an unrelated style
change into a defect fix.

## Protocol impact

**None.** `pkg/api` is the local HTTP interface an operator talks to, not a peer
interface. No file in the frozen wire surface is touched, so
`scripts/protocol-freeze.sh --check` is unaffected.

It is an observable API behaviour change: a caller that previously saw 500 in
this case will now see 405. Nothing in `openapi/Swarm.yaml` enumerates the
status codes for this endpoint in a way that changes, so no spec version bump
is required.

## Measurement

Not a performance change, so there is no before-and-after to measure. Done when:

1. A unit test drives `AvailableBalance` to return `ErrChainDisabled` and
   asserts 405 — and fails against the current code, which is what makes it a
   real test rather than a restatement.
2. The existing `TestChequebookBalance` still passes, showing the success path
   is untouched.

## Rollout and rollback

No configuration, no migration, no state. Reverting the commit fully restores
previous behaviour.

## Upstream portability

**The cleanest upstream candidate in the backlog.** Self-contained, obviously a
defect, no design argument or configuration attached, and it applies unchanged
to upstream `master`. If anything from this fork is offered to Ethersphere
first, it should be this.

`scripts/export-patch.sh chequebook-chain-disabled` produces the series against
current upstream once the work is merged and tagged.

Generated with help of AI.
