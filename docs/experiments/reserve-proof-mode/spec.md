# Spec: reserve-proof-mode, an experimental windowed sublinear proof behind a switch

Issue: [#273](https://github.com/crtahlin/wasp/issues/273). Umbrella:
[#234](https://github.com/crtahlin/wasp/issues/234). Follows the sound sublinear proof
study ([#271](https://github.com/crtahlin/wasp/issues/271)) and the probe-proof soundness
finding ([#248](https://github.com/crtahlin/wasp/issues/248)).

## Problem

The redistribution reserve sample costs one loaded-and-hashed chunk per chunk within
committed depth, which is a few million chunks and tens to hundreds of seconds a round.
[#271](https://github.com/crtahlin/wasp/issues/271) validated a proof that is both sound
and sublinear: a windowed order statistic. From the round anchor derive an unpredictable
window covering a fraction 1/f of the address space, then take the k-th smallest
transformed address of the chunks inside the window. It measures the same content-bound
transform the current proof does, so a node cannot forge it by spacing its chunks, and it
costs about N/f instead of N.

This spec makes that calculation real in the node, behind a switch, so its cost can be
measured on a live node rather than only in simulation. The default keeps the current
behaviour exactly.

## Why a contract change is unavoidable, and why it is small

The reserve sample is not node-local. The redistribution agent commits the sample hash on
chain and, at reveal, submits inclusion proofs that the on-chain Redistribution contract
verifies. The contract checks that the revealed chunks are real held chunks with valid
stamps, that their transformed addresses are computed correctly, and that the k-th (largest)
transformed address clears a threshold u. That threshold is the density check: k transformed
addresses that small require holding many chunks.

The windowed proof can reuse the entire proof format: the same sample chunk (the k
transformed addresses concatenated), the same committed hash, the same inclusion proofs, the
same reveal challenge. What it cannot do is pass the current contract, for two reasons that
are essential rather than cosmetic.

1. The threshold is calibrated for the whole reserve. A 1/f window holds about f times fewer
   chunks, so its k-th smallest transformed address is about f times larger, and it fails u.
   The chunks whose transform falls below u are scattered uniformly across the address space
   by the transform, so only about k/f of them land in the window. To clear the current u a
   window would have to hold about k sub-u chunks, which needs the window to cover almost the
   whole space, which is no saving. Passing today's contract is arithmetically equivalent to
   scanning the whole reserve. This is the [#248](https://github.com/crtahlin/wasp/issues/248)
   tension restated at the contract: the threshold is the whole-reserve requirement.
2. Soundness needs the contract to confirm the revealed addresses lie inside the window.
   Otherwise the sample size the threshold assumes is undefined and the density inference
   breaks.

A windowed-aware contract adds exactly two cheap checks: compare against a window-scaled
threshold, and range-check that each revealed chunk address lies in the anchor-derived
window (the window is derived from the anchor the contract already holds, so it needs no
extra submitted data). Everything else is reused. That contract change is out of scope here;
this spec builds the node side so that a windowed-aware contract, when it exists, accepts
wasp proofs without further node work.

Caveat on evidence: the node-side commitment and inclusion proofs were read directly
(`pkg/storageincentives/proof.go`, `pkg/storer/sample.go`). The contract-side verification
above is inferred from that structure and the redistribution design, not from the deployed
contract source, and is flagged to confirm against it before any contract work begins.

## Hypothesis

A windowed sampler produces a self-consistent commitment and proof in the identical format,
at a cost near N/f, and its soundness matches [#271](https://github.com/crtahlin/wasp/issues/271)
on a real reserve. On the current network it wins nothing, because the live contract rejects
it for the two reasons above, so it is an experimental mode for a testnet and for readiness.
The measurement confirms the real wall-clock saving on a node, which the N/f figure only
predicts.

## Design

The default path is byte-for-byte the current whole-reserve scan. The windowed path is a
parallel computation selected by one setting.

### The setting

- `reserve-proof-mode`, values `classic` (default) and `windowed`. Parsed and validated once
  at startup; an unknown value is rejected with a clear error. Threaded from
  `cmd/bee/cmd/cmd.go` config to the node and into the redistribution agent and the storer.
- `classic` selects the existing `ReserveSample`. Nothing changes.
- The window fraction is a fixed protocol-facing constant with a safe default (for example a
  divisor tied to a minimum window count of a healthy multiple of k), so that for small
  reserves the window degenerates toward the whole reserve and stays sound. Whether the final
  rule ties the fraction to storage depth (which a contract can see) or to reserve size (which
  it cannot) is a contract-design question recorded as open; the node uses the fixed default
  until then.

### The windowed sampler, Phase A

- `(*DB).WindowedSample(ctx, anchor []byte, committedDepth uint8) (Sample, error)` in
  `pkg/storer`, reusing the transform machinery of `ReserveSample`.
- Derive the window inside the node's neighbourhood: a contiguous address slice whose start
  is `keccak(anchor || "window")` reduced into the neighbourhood span, and whose width is the
  window fraction of that span. The window is unpredictable before the anchor is revealed.
- Iterate the retrieval index bounded to the window, load and hash only those chunks, take
  the k smallest transformed addresses. Returns the same `Sample` shape as `ReserveSample`,
  so downstream code is unchanged.
- Unit tests: even placement matches random placement, a slacker is rejected, the window
  holds at least the floor count, and `classic` output is unchanged.

### Agent wiring, Phase B

- When the mode is `windowed`, `reserveSampleAndHash` calls `WindowedSample`, and the
  commitment hash and inclusion proofs are produced by the existing `sampleHash` and
  `makeInclusionProofs` over the windowed items, so the node's own commit and reveal agree.
- The proof is built in the identical format on purpose, so it is forward-compatible with a
  windowed-aware contract. No contract call changes.
- Tests: the windowed commit and reveal are self-consistent; `classic` remains the default
  and is byte-for-byte the current path.

### Documentation and measurement, Phase C

- The setting in `docs/config-reference.yaml`, an operator warning in the wasp README
  experimental section, and the experiment ledger row.
- A real wall-clock measurement of windowed versus classic on a bench node, written up
  beside the [#271](https://github.com/crtahlin/wasp/issues/271) analysis.

## Protocol impact

Off by default, so a node upgrading to this build changes no behaviour and stays in
consensus and rewarding. When set to `windowed`, the node's redistribution proof diverges
from the whole-reserve proof the network and the live contract expect, so the node wins
nothing until a windowed-aware contract exists; this is the `consensus-risk` the label
records, and the documentation states it plainly. No peer wire message or serialization
format changes, so this does not touch `.github/protocol-freeze.lock`; the change is in the
redistribution proof semantics, gated behind an off-by-default switch.

## Measurement

Rule 7: at least three runs per condition, reported with the spread, matched node state.

- Baseline: `classic` on the node and condition (equivalently `/rchash`).
- Windowed: `windowed` at the default fraction, and a small sweep of fractions if useful.
- Conditions: warm, and cold with the OS page cache dropped, on bench-1 and bench-2; note the
  engine. Record wall time, chunks touched, the seek, load and hash split, allocations, and N.
- Report the real speedup versus classic and whether it scales as N/f, and state any tempering
  from random-read cost, exactly as the probe-cost study did. A tempered result is a result.
- Soundness is not re-measured on the node; it is established in
  [#271](https://github.com/crtahlin/wasp/issues/271).

## Configuration

- `reserve-proof-mode`: `classic` (default) or `windowed`. Documented as experimental and
  non-rewarding on the live network. Setting it on a real staked node stops it winning
  rounds, and the reference and README say so.

## Rollout and rollback

Default is `classic`, mainnet-safe, no action needed on upgrade. Enabling `windowed` is an
operator decision for a testnet or research node. Rollback is setting the value back to
`classic` and restarting; there is no persistent state to unwind.

## Upstream portability

Not intended for upstream as a production change, since the algorithm it prepares is a Swarm
protocol and contract change, not a client change. The node-side code is a self-contained
storer method plus agent wiring behind a flag, so it ports across an upstream sync without
conflict. Default behaviour is unchanged, so there is no upstream divergence by default. No
`affects-upstream` label.

Generated with help of AI.
