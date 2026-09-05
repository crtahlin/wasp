# Stabilise the advertised underlay so a NAT node keeps its peers

Issue: [#221](https://github.com/crtahlin/wasp/issues/221)

Status: spec for review. No code lands until this is merged.

## The problem, confirmed on a live node

A light node behind NAT sat at zero connected peers while dialling continuously.
The cause is in the handshake. On every handshake the node rebuilds the set of
underlay addresses it advertises from the peer's **observation** of it, resolved
through the advertisable-address resolver, plus its host addresses. Behind NAT with
no fixed public address, each peer observes a different address for the node, so the
set changes every handshake. The signed address is minted from that set and cached
"session-stable," keyed by the chequebook plus the underlay set, so a changing set
means the cache never hits: the node re-mints and re-advertises a changing signed
address to everyone, which churns connections and leaves it isolated.

This was confirmed directly. The metric `handshake_address_minted_total` reached
about 180,000, roughly one mint per connection, at zero peers. Setting `nat-addr`
to a fixed address makes the resolver return one stable address regardless of what
peers observe; the mint count then dropped to one for 139 connections and the node
peered straight back up. So a stable advertised address is the fix, and the node
should reach that state itself rather than depend on manual `nat-addr`.

## What is upstream, and where this belongs

The whole apparatus is upstream Bee: multiple-underlay advertisement (upstream PR
#5204) and the session-stable minting with its churn diagnostic (upstream PR
#5493). The fork has not touched the handshake. So this is an upstream behaviour on
a node whose observed address is unstable, and it carries the `affects-upstream`
marker. The fix here is a targeted fork patch that is also the right shape to
propose upstream.

## Design

Add a step between building the advertisable underlay set and signing it, that
keeps the advertised set steady across handshakes while still following a genuine
change of the node's public address.

Let the freshly computed set be `F`, and its public members (global-scope IP
addresses, by `manet.IsPublicAddr`) be `Fp`. Hold a pinned advertised set `A` on
the handshake service, guarded by its own mutex. On each handshake:

1. **Bootstrap.** If `A` is empty and `Fp` is non-empty, adopt `A = F`. The node
   advertises a public address as soon as one is observed.
2. **No public seen.** If `Fp` is empty, return `A` unchanged (a single handshake
   that observed no public address does not drop the pin), or the computed set if
   nothing is pinned yet, so discovery can still happen.
3. **Still valid.** If any public member of `A` shares its **IP** with a public
   member of `Fp`, the node's public address is unchanged; return `A` unchanged and
   reset the change counter. This is the common case and it is what stops the churn:
   the same address is returned every handshake, so the mint cache hits.
4. **Sustained change.** If no public IP of `A` appears in `Fp`, the public address
   may have changed. Increment a change counter. Only when it reaches a small
   threshold of consecutive handshakes without the pinned IP do we re-pin, `A = F`,
   and reset the counter. Until then `A` is returned unchanged.

The comparison is by IP, not by the full address, on purpose. NAT commonly remaps
the source port per connection, so different peers observe the same public IP with
different ports. Comparing whole addresses would treat every remapped port as a
change and re-pin, which is the churn this spec removes. Keying on the IP makes the
port irrelevant: the pin holds as long as the public IP is stable, and a set pinned
once (with whatever port a peer first reported) is advertised byte-stable
thereafter. The advertised port need not be dialable for a light node, which peers
outbound; stability is what matters, and a stable address is what restored peering
in the confirmation.

Step 4 is the hysteresis that separates a real IP change from noise. A transient or
malicious single observation of a different address does not re-pin, because the
next handshake that sees the real address resets the counter. A genuine IP change,
observed consistently, re-pins after the threshold and the node advertises the new
address stably from then on. Re-pinning re-mints once, which is correct: the
address really did change.

A configured `nat-addr` is unaffected. Its static resolver already returns one
fixed address, so `F` is constant, `A` is adopted once and never changes, and this
step is a no-op.

### Corner cases

- **Public IP change.** Handled by step 4. The node advertises the old address for
  at most the threshold number of handshakes, then adopts the new one. This is the
  case this spec must survive, and the test plan exercises it explicitly.
- **Multi-homed (IPv4 and IPv6, or two public addresses).** `A` holds all public
  members it was pinned with. It stays valid while any of them is still observed, so
  losing one address does not force a re-pin while another is still reachable.
- **Transient or hostile observation.** A single differing observation does not
  re-pin; the counter needs consecutive confirmations and is reset by any handshake
  that sees the pinned address.
- **No public address ever (a purely private or LAN-only node).** Nothing is
  pinned; the computed set is returned each time. On such a network the observed
  address is the stable LAN address, so there is no churn to fix, and the node is
  not internet-reachable regardless.
- **Concurrency.** Handshakes run concurrently; the pin and counter are guarded by a
  dedicated mutex.

### The threshold

The change threshold is a compiled-in constant, kept small. It is not exposed as
configuration yet: it is an internal hysteresis, not a per-deployment tuning knob,
and rule 8 asks for a measurement before adding permanent surface. If a real
deployment shows it needs tuning, it can become a flag then.

## Protocol impact

No wire change. The handshake message format, `ProtocolName` and `ProtocolVersion`
are untouched, so the freeze check does not fire and `.github/protocol-freeze.lock`
does not change.

The behaviour that changes is which underlay address the node advertises, and it
moves in the safe direction: instead of a different address per handshake, the node
advertises one stable address, so peers write it to their address book once and
gossip it once instead of on every connection. A stock peer sees strictly fewer
address updates from this node, never more. When the node's public address genuinely
changes, the new address is advertised, stably, after a short confirmation.

This carries `affects-upstream`: the minting apparatus is upstream, and the same fix
applies to a stock Bee node behind NAT.

## Test plan

Unit tests on the stabilisation step, which is a pure function of the input set and
the held state:

- Bootstrap adopts the first set containing a public address.
- A repeated public observation returns the same pinned set (the anti-churn case).
- The same public IP observed with a different port each time returns the same
  pinned set (the port-remapping case, the real driver of the churn).
- A single differing observation does not re-pin; the pinned set is kept.
- A sustained different public address re-pins after the threshold.
- Two public addresses (IPv4 and IPv6): the pin stays valid while either is observed.
- No public address observed: the computed set passes through unchanged.
- A configured static resolver (the `nat-addr` case) is a no-op: the set never
  changes.

Node-level confirmation, on the affected host, the same way `nat-addr` was
confirmed: build the patched binary, remove `nat-addr`, restart, and check that
`handshake_address_minted_total` stops climbing per connection and the node holds
its peers. This is the acceptance gate before the default behaviour is relied on.

## Documentation

- A short operator note that a node behind NAT now stabilises its advertised address
  automatically, that `nat-addr` still overrides it, and that
  `handshake_address_minted_total` climbing roughly one per connection is the signal
  of the old failure.

## Rollout and rollback

The change applies to every node and needs no configuration. It is effectively a
no-op for a node with `nat-addr` set or a node whose observed address is already
stable. Rollback is reverting the patch; `nat-addr` remains the manual workaround in
the meantime.

## Order of work

1. This spec merged.
2. Implement the stabilisation step with the re-pin hysteresis and the corner cases.
3. Unit tests as above, and the operator note.
4. Node-level confirmation on the affected host before the behaviour is relied on.
5. Open with `affects-upstream`, and propose the same change upstream.

Generated with help of AI.
