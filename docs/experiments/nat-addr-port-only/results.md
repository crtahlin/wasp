# Results: a nat-addr that follows the public IP

Issue: [#500](https://github.com/crtahlin/wasp/issues/500). Spec: [spec.md](spec.md).
Merged in `5575f71f` (#502).

Measured on 2026-09-26 on `bench-1`, which sits behind a port-forwarding NAT, on
build `0.1.4-main-2026-09-25-cba69e16`, which contains the change. `stake-1`, a
full node with a public address on v0.1.4, dialled it from outside.

**Table: bench-1 with a stale IP in nat-addr, then with nat-addr ":1634"**

| Check | Result |
|---|---|
| `nat-addr` set to the network's previous public IP, which is stale | `bee_handshake_nat_addr_mismatch_total` rose within about 11 s of the restart, and the warning `configured nat-addr IP is not the IP peers observe; ...` was logged with `advertised_ip` and `observed_ip` |
| `nat-addr: ":1634"`, 60 s after restart | `/addresses` lists the current public IP with port 1634; mismatch counter 0 |
| TCP connect from `stake-1` to that address | open |
| `POST /connect` from `stake-1` to that address | 200 in 0.23 s |
| `bee_handshake_address_minted_total` | 1 after 1 minute and still 1 after 5 minutes, with 129 to 132 peers |

The last row is the check that the churn fixed by #221 has not come back: one
signed address minted for the whole session, not one per connection.

**Not measured:** a real change of the public IP while the node runs, which
cannot be caused on demand. The re-pin after three handshakes is covered by
`TestHandle_PortOnlyNATFollowsPublicIP`. The warning counted more than once in
the first minute because the node made many handshakes at start-up, each run of
three disagreeing ones counting once, as specified.

`bench-1` now runs with `nat-addr: ":1634"`.

Generated with help of AI.
