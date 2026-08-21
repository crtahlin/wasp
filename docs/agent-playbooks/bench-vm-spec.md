# Bench machine specification

What the measurement and validation nodes need, and why. Sized from the actual
constraints in `docs/analysis/storage-layer-briefing-2026-01.md` rather than
guessed.

Nothing here reuses any existing node estate. These machines are provisioned
fresh for this project.

## Summary

Two machines cover the whole backlog. One is enough to start.

**Table — Bench machines for bee-experimental, minimum viable through full coverage**

| Role | vCPU | RAM | Disk | Network | Purpose |
|---|---|---|---|---|---|
| `bench-1` | 8 | 16 GB | **500 GB NVMe/SSD** | 100 Mbit+, static or forwarded port | Benchmarks and the canary node. Start here |
| `spin-1` *(optional)* | 4 | 8 GB | 200 GB **mechanical HDD** | any | The one thing no SSD can answer |

If only one machine is possible, make it `bench-1`.

## Why those numbers

**Disk is the binding constraint, and 500 GB is not padding.** The
device-bound benchmarks need a corpus larger than RAM to force reads to reach
the storage device — page-cache-resident numbers measure software overhead and
overstate the benefit by roughly double. With 16 GB RAM the corpus must exceed
that comfortably; the original measurements used 67 GB. Add a filled reserve
(17 GB at default capacity, 34 GB doubled, and issue #17 proposes raising the
cap to 10 doublings), LevelDB indexes, and room for a before-and-after pair of
builds, and 500 GB is the honest figure. **NVMe or SSD, not network-attached
storage** — an EBS-style volume with its own queueing measures the storage
service, not Bee.

**RAM at 16 GB is chosen to be realistic, not generous.** A `retrievalIdx` for
a full reserve is 213 MB and the default LevelDB block cache is 32 MB. Issue
#12 is precisely about that ratio, and its measured 1.17x is explicitly a
*lower bound* because the index stayed in OS page cache during testing. A
machine with so much RAM that everything caches cannot reproduce the effect a
real node sees. 16 GB is enough to run comfortably and still create genuine
cache pressure against a filled reserve.

**8 vCPU because the sampler scales workers with `runtime.NumCPU()`.** Issues
#8 and #9 are about the interaction between I/O concurrency and CPU-bound
hashing, so core count is a variable in the experiment. Fewer than 8 makes the
sampler-restructuring result hard to read; far more would flatter it.

**Network: inbound port 1634 must be reachable.** A node that cannot accept
inbound connections gets a poor peer set, which silently distorts every
sync-related measurement (#18–#26). A static address or a forwarded port is
required; the HTTP API on 1633 must stay bound to localhost.

## The mechanical drive is a real gap, not a nice-to-have

Issue #11 sorts sampler reads into physical order. On SSD it measures 11–25%.
On a seek-bound device the effect should be far larger — plausibly the
difference between completing a reserve sample within a redistribution round
and failing to, which is the difference between earning and not earning.

**That has never been tested.** No mechanical drive was available for the
original work, and a VM with SSD-backed storage cannot answer it either. A
single cheap spinning disk — even a USB-attached one on an existing machine —
would settle it. Until then #11 is ranked on its SSD numbers alone, which
probably understates it.

## Operating system and software

- **Linux, x86-64.** Debian 12 or Ubuntu 22.04/24.04. The packages this project
  builds are `.deb` and `.rpm`, and Debian-family is the shortest path to
  testing the drop-in replacement behaviour.
- **ext4.** Measurements to date were on macOS with APFS; production nodes are
  overwhelmingly Linux on ext4, and absolute throughput does not transfer
  between them.
- Go 1.26 to build, or install the released `.deb` to test packaging.
- SSH access. Keys and addresses stay **out of this repository** — it is public.
  See `docs/agent-playbooks/test-bench.md`.

## Network and stake posture

- **Mainnet**, so peer behaviour and sync rates are real. Testnet peer counts
  and traffic patterns do not represent what these changes are tuning for.
- **Unstaked.** Running unreviewed code on a staked node risks real value if it
  misbehaves during a redistribution round. Every measurement in the backlog can
  be taken unstaked; nothing requires stake at risk.
- Funded only enough for chequebook operation, if swap is enabled at all. Several
  issues can be measured with `--swap-enable=false`.

## What one machine unblocks

With `bench-1` alone:

- **#13** — `SampleStats` from a real node under real conditions. Cheapest data
  available and it may reorder the whole storage track, since every optimization
  there assumes sampling is disk-bound.
- **#8, #9, #10, #11, #12** — measured on Linux and ext4 rather than macOS and
  APFS, which is what the results need to be quoted against.
- **#17–#27** — the reserve-capacity and sync cluster, whose metric is
  time-to-full-reserve and cannot be produced on a laptop.
- **#24** — a reproduction attempt for the LevelDB write deadlock, which today
  rests on an operational diagnosis rather than a reproducible case.
- The **merge gate** for every change: run it, confirm it peers and stays
  healthy.

## What to check on first boot

```bash
bee version                                    # fork version and upstream base
curl -s localhost:1633/health    | jq
curl -s localhost:1633/readiness | jq
curl -s localhost:1633/status    | jq
curl -s localhost:1633/peers     | jq '.peers | length'
curl -s localhost:1633/topology  | jq '{depth, connected, population}'
```

An HTTP 200 is not proof. Watch the logs for handshake failures and for a peer
count that climbs then collapses — a node that starts, reports healthy, and
quietly fails to peer is the exact failure this fork is most likely to
introduce, and `/health` will not show it.

Generated with help of AI.
