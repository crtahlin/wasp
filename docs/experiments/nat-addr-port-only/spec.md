# A nat-addr that follows the public IP

Issue: [#500](https://github.com/crtahlin/wasp/issues/500).
Type: fix.

## Problem

A node behind a port-forwarding NAT sets `nat-addr` to tell peers where to dial
it. Today that value must contain a host, an IP or a DNS name, and the node
advertises that host for as long as it runs:

- **An IP is fixed.** When the network's public IP changes, the node keeps
  signing and advertising the old one.
- **A DNS name** was described here as fixed too, resolved once at startup.
  **That was wrong, corrected during review of the implementation.**
  `getMultiProto` (`pkg/p2p/libp2p/static_resolver.go:112-135`) looks the name
  up only to choose between `/dns4`, `/dns6` and `/dns`, and the node advertises
  `/dnsX/<name>/tcp/<port>`, which peers resolve each time they dial. So a
  dynamic DNS name already follows an IP change. The port-only form is still
  needed for operators who do not run dynamic DNS, or who do not want to publish
  a hostname in every signed address, which is why the bench nodes use a bare
  IP.
- **Nothing tells the operator.** The node keeps its outbound connections and
  its peer count, and `/status` keeps reporting `isReachable: true`.

Observed on 2026-09-25 on `bench-1` and `bench-2`, which share one NAT: the
public IP had changed, both nodes still advertised the old one, a TCP connect
to the advertised address timed out from another host while the same port on
the current IP was open, and a provider download failed because the requester
dialled the stale address (#499). Rewriting `nat-addr` and restarting fixed
both nodes at once.

The alternative, leaving `nat-addr` unset so that #225 learns the address from
peers, does not work behind this kind of NAT. Peers observe the correct **IP**
but the wrong **port**: they see the NAT's outbound mapping for the connection,
not the forwarded inbound port. `nat-addr` is the only way to state the port,
and today stating the port forces stating the IP as well.

## Hypothesis

Most of the fix already exists and is blocked by one check.

- **The resolver supports a port-only value.** `newStaticAddressResolver`
  (`static_resolver.go:20-38`) splits `nat-addr` into host and port. With an
  empty host it stores no IP, and `Resolve` (`:40-93`) then keeps the IP and
  protocol of each observed address and replaces only the port. Upstream's own
  test covers it: the case named `replace port` in
  `static_resolver_test.go` uses `natAddr: ":30123"`.
- **Startup validation rejects that value.** `validatePublicAddress`
  (`pkg/node/node.go`) returns `host is empty` for `":1634"`, so a node never
  reaches the resolver with it. This check is unmodified upstream code, present
  in `upstream/v2.8.2`.
- **#225 handles an IP change at runtime.** In each handshake the observed
  addresses go through the resolver, and the result goes through
  `stabilizeUnderlays` (`pkg/p2p/libp2p/internal/handshake/handshake.go:205-242`).
  That pins the first set with a public IP and re-pins after
  `advertisedUnderlayRepinThreshold`, three consecutive handshakes that observe a
  different public IP. `signedAddress` re-mints the signed address when the set
  changes, and later handshakes and hive gossip carry the new one. **No restart
  is involved.**

So with a port-only `nat-addr`, the advertised address becomes "the public IP
peers observe, on the forwarded port", and it follows an IP change within a few
handshakes. After a change, the node's old connections usually drop, its
outbound dials keep working, and the peers it reaches report the new IP.

## Design

### 1. Accept a port-only nat-addr

In `validatePublicAddress`, accept an empty host when the port is present and
valid. Every other rule stays: an empty port, a non-numeric port, `localhost`,
a loopback IP and a private IP are still rejected when a host is given. The
error for `":"` stays `port is empty`.

The accepted form is documented in the option help (`cmd/bee/cmd/cmd.go`), in
`packaging/bee.yaml`, and in `docs/DIFFERENCES.md`.

### 2. Keep #225 working in that mode

No code change is expected in `stabilizeUnderlays`. Its comment says a
configured `nat-addr` makes it a no-op, which becomes true only of a `nat-addr`
with a host. The comment is corrected, and a test pins the behaviour described
under Tests.

### 3. Warn when a fixed nat-addr disagrees with what peers observe

With a host in `nat-addr`, the advertised IP never changes, so the operator
needs a signal when it goes stale. In `Handshake` and `Handle`, after resolving,
compare the public IPs peers **observed** with the public IPs about to be
**advertised**:

- only IPs of the same family are compared, so a peer that reached the node
  over IPv6 says nothing about a configured IPv4 address
- a handshake **disagrees** when it observed at least one public IP of a family
  that is advertised, and none of the observed IPs of that family is advertised
- after `advertisedUnderlayRepinThreshold` consecutive disagreeing handshakes,
  log one warning and increment a counter; a handshake that agrees resets the
  run, and the warning is logged again only after a new run

The warning is operator-facing (`Warning` level) and names what to do:

```
configured nat-addr IP is not the IP peers observe; update nat-addr, or set it to ":<port>" to follow the observed IP
```

with keys `advertised_ip` and `observed_ip`. The counter is
`handshake_nat_addr_mismatch_total`.

In port-only mode and with no `nat-addr`, the advertised IP **is** the observed
one, so this never fires. It is therefore safe to run in every mode and needs no
switch.

The advertised address is **not** changed automatically when a configured IP
disagrees. An operator who wrote an IP asked for that IP. Changing it silently
could also be triggered by a peer that lies about what it observed, which is the
reason #225 already requires a sustained run.

## Protocol impact

**None.** Nothing on the wire changes: the handshake messages, the signed
address format and every constant in `.github/protocol-freeze.lock` stay as they
are. What changes is *which* IP a node that opts into the port-only form puts in
its signed address, which is the same kind of value #225 already puts there for
a node without `nat-addr`. Stock peers already accept a signed address whose IP
changes between connections, because that is how every NAT node without
`nat-addr` behaves. `make protocol-freeze` must report the surface unchanged.

## Tests

Mutation checked: each named mutation below must break a named test, and a
mutation that does not compile proves nothing and is redone.

- **`validatePublicAddress` accepts `":1634"`.** Fails today with `host is
  empty`. Table cases also pin what must stay rejected: `":"`, `":abc"`,
  `"localhost:1634"`, `"127.0.0.1:1634"`, `"192.168.1.10:1634"`.
- **Port-only advertises the observed IP on the configured port.** A handshake
  test with a port-only resolver: the peer observes `/ip4/<public>/tcp/<ephemeral>`,
  and the signed address carries `/ip4/<public>/tcp/<configured port>`.
- **Port-only follows an IP change.** Three handshakes observing a new public IP
  re-pin to it with the configured port; one or two do not. This is the property
  the whole fix rests on, and the one #225's existing tests cover only without a
  resolver.
- **The warning fires on a sustained disagreement and only then.** With a
  fixed-host resolver: three disagreeing handshakes increment
  `handshake_nat_addr_mismatch_total` once; two do not; an agreeing handshake in
  between resets the run; an IPv6 observation with an IPv4 `nat-addr` does not
  count; port-only mode never increments it.

Mutations: restore the empty-host rejection; drop the port replacement in
port-only mode, by making the relaxed validator also blank the port; make the
mismatch fire on one handshake; compare IPs across families.

## Measurement

This fixes a failure, so the measurement is that the failure is gone, on the
node where it happened.

On `bench-1`, which sits behind NAT:

1. set `nat-addr` to `":<forwarded port>"` and restart
2. `/addresses` lists the current public IP with the forwarded port
3. another host completes a TCP connect and a `POST /connect` to that address
4. `handshake_address_minted_total` stays low after start, which shows the set is
   stable and the #221 churn has not returned
5. a stale fixed IP in `nat-addr` produces the warning and the counter within
   minutes of start

A real public-IP change cannot be caused on demand, so the re-pin itself is
covered by the unit test, not by the bench. The result is recorded in
`results.md`.

A negative result would be the port-only node advertising an address with the
wrong port, or its minted count climbing per connection.

## Rollout and rollback

Opt-in. An operator changes `nat-addr: "203.0.113.5:1634"` to `nat-addr:
":1634"`. A node with a host in `nat-addr`, or without `nat-addr`, keeps its
current behaviour and gains only the warning.

Rollback is writing the IP back. A binary without this change **refuses to
start** with a port-only value (`host is empty`), so an operator downgrading
must restore the IP first. That is stated in `docs/DIFFERENCES.md`.

## Upstream portability

The validator change applies to upstream as is: the resolver mode it unlocks is
upstream code with an upstream test. Without #225, upstream would advertise the
observed IP from each handshake rather than a pinned one, which is stable in
practice because the port no longer varies, but it lacks the hysteresis. The
warning ports independently.

The issue is not labelled `affects-upstream`. The validator and the resolver
disagree upstream, which fits the rule's description of a defect, but that has
been established by reading the code, not by running upstream. The fork test
that fails today on `":1634"` is the reproduction that would justify the label
once run against the upstream tree.

## Configuration

No new option. `nat-addr` gains a form. Its documentation must say what each
form costs:

- **`<ip>:<port>`**: stable, but goes stale silently when the public IP changes,
  which the new warning now reports.
- **`<dns-name>:<port>`**: advertised as a name that peers resolve when they
  dial, so it follows a dynamic DNS record, but it publishes a hostname in every
  signed address. (Corrected; an earlier revision said it was resolved once at
  startup.)
- **`:<port>`**: follows the IP peers observe. It requires inbound and
  outbound traffic to share one public IP, since peers observe the outbound one
  (added during review). It trusts peers' observations,
  limited by #225's three-handshake run, so a minority of lying peers cannot move
  it. It costs other nodes nothing.

## Files

- `pkg/node/node.go`: `validatePublicAddress`.
- `pkg/node/` test for the validator.
- `pkg/p2p/libp2p/internal/handshake/handshake.go`: comment on
  `stabilizeUnderlays`, the mismatch check in `Handshake` and `Handle`.
- `pkg/p2p/libp2p/internal/handshake/metrics.go`: the counter.
- handshake tests for port-only advertising, re-pin and the warning.
- `cmd/bee/cmd/cmd.go`: the `nat-addr` help text.
- `packaging/bee.yaml`: the commented `nat-addr` entry.
- `docs/DIFFERENCES.md`, `docs/experiments/INDEX.md`.

Generated with help of AI.
