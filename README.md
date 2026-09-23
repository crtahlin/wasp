# wasp

*It's Bee with the safety off.*

An experimental downstream distribution of [Ethereum Swarm
Bee](https://github.com/ethersphere/bee).

> ### WARNING: read this before running it
>
> This is **not** the Bee client. It is a personal fork carrying changes that
> the upstream project has not adopted, has declined, or has not evaluated.
>
> - **Not affiliated with, endorsed by, or supported by** the Swarm Foundation
>   or the Ethersphere team. Do not report problems with this software to them.
> - **Unaudited and experimental.** It may lose your data, misbehave on the
>   network, or lose staked funds during a redistribution round.
> - **No warranty of any kind.** See [`DISCLAIMER.md`](DISCLAIMER.md) and
>   [`LICENSE`](LICENSE).
> - **Some options are experimental even by this fork's standards.** Settings
>   marked experimental in the config reference, such as `reserve-proof-mode:
>   windowed`, can stop the node winning redistribution rounds entirely. Leave
>   them at their defaults unless you are on a testnet or know exactly why.
>
> If you want a Swarm node that works, run [upstream
> Bee](https://github.com/ethersphere/bee). Run this only if you understand what
> you are opting into and are prepared to lose whatever the node is holding.

## What it is

Upstream Bee is the reference implementation, and it is conservative for good
reasons: it is the client most of the network runs. That conservatism means
some optimizations and fixes are not worth upstream's risk budget, even when
they are worth an individual operator's.

This repository is where those get built properly instead of living as loose
branches: specified before they are written, developed one to a branch, tested,
documented, measured on real nodes, and shipped in versioned releases.

**It stays protocol-compatible with stock Bee.** These nodes are meant to run on
the real Swarm network alongside everyone else's. The wire surface (protocol
versions, the handshake, chunk geometry, network ID) is frozen and enforced by
a CI check on every pull request. See
[`docs/agent-playbooks/protocol-compatibility.md`](docs/agent-playbooks/protocol-compatibility.md).

## Relationship to upstream

- Tracks upstream **release tags**, absorbed as real merges. The current base is
  in [`.upstream-base`](.upstream-base) and is reported by `bee version`.
- **Nothing is pushed back to `ethersphere/bee`.** This is deliberately not a
  GitHub fork, and the upstream remote is fetch-only. If a change here turns out
  to be worth upstreaming, it gets offered as a normal contribution, by a human,
  on purpose, `scripts/export-patch.sh <slug>` generates a clean patch series
  against current upstream for exactly that.
- Versions are this fork's own line (`v0.1.0` onward) and do not mirror
  upstream's, so nothing here can be mistaken for an official release.

## Install

Packages are drop-in replacements for upstream's `bee` package, same binary
path, same systemd unit, same `/etc/bee/bee.yaml`. They deliberately conflict
with upstream's package, so installing one replaces the other.

```bash
# Debian / Ubuntu
dpkg -i wasp_<version>_amd64.deb

# Docker
docker run ghcr.io/crtahlin/wasp:<version>
```

Existing upstream configuration and data directories are used unchanged, so
rolling back means reinstalling upstream's package.

Releases: <https://github.com/crtahlin/wasp/releases>

<!-- highlights:start -->
## What wasp changes

Grouped by what a node does, not by what was edited. This is a summary and is
deliberately incomplete. [`docs/DIFFERENCES.md`](docs/DIFFERENCES.md) is the
complete record, entry by entry, against the latest released Bee.

**Serving content you hold, without postage.** The largest fork-only feature.
`POST /wasp/ingest` stores content in the node's own store with no stamp and
without pushing it to the network, returning the same reference a stamped upload
would. `POST /wasp/providers/{reference}` then announces that the node serves it,
and `GET /wasp/providers/{reference}/lookup` finds who does. A download can name
up to eight providers to try first with the `Wasp-Providers` header on `/bzz`,
`/bytes`, `/chunks` and `/feeds`, falling back to ordinary retrieval.

**Downloads that were failing now work.** A download starting in the first
seconds after a restart no longer waits for the network radius, which used to
make it truncate silently. A hinted download whose provider finishes connecting
after the request began now uses that provider instead of returning 404. A chunk
is no longer abandoned while a provider is still answering for it.

**Caller mistakes answer 4xx, not 500.** Malformed multipart and archive
uploads, and a malformed index-document header, answer 400. `GET /chunks` and
`POST /pins` answer 404 when the search for a peer runs out, where Bee answers
500 and tells the caller its server is broken.

**Storage and shutdown.** A new data directory uses Pebble, with its compaction
and write-slowdown triggers exposed as settings. The index store is closed after
the reserve worker stops, rather than racing it, which is what caused a shutdown
segfault and a write-ahead log replay on the next start. Optional SIMD hashing.

**Accounting and settlement.** Overdraft limits and payment amounts stay correct
when the clock steps backwards. Cheque allocation is serialized per beneficiary.
Chain calls are off the cheque path.

**Recovering stranded stake.** `GET` and `POST /stake/legacy` list and recover
stake held in retired staking contracts. Recovery on startup is off by default.

**Identity.** The libp2p user agent leads with `wasp/<version>` and keeps
`bee/<upstream base>`, so a crawler reads a wasp node as its own client. Version
numbers are this fork's own line and `bee version` reports both.
<!-- highlights:end -->

## What is in it

- [`docs/DIFFERENCES.md`](docs/DIFFERENCES.md): every way wasp differs from the
  latest released Bee, kept current as either side changes
- [`docs/ROADMAP.md`](docs/ROADMAP.md), what is planned
- [`docs/experiments/INDEX.md`](docs/experiments/INDEX.md), what has landed, and
  what came of it, including the things that did not work

Every experiment is one merge commit on `main`, so the history answers the
question directly:

```bash
git fetch origin                     # main is a local ref and is often behind
git log --first-parent origin/main   # every change this fork makes to Bee
git diff <merge>^1 <merge>           # exactly what one of them changed
```

## Contributing

The process is issue -> spec -> branch -> pull request -> merge, and it is not
optional. See [`AGENTS.md`](AGENTS.md) for the rules and
[`docs/agent-playbooks/experiment-lifecycle.md`](docs/agent-playbooks/experiment-lifecycle.md)
for the walkthrough. Upstream's [`CODING.md`](CODING.md) and
[`CODINGSTYLE.md`](CODINGSTYLE.md) still govern the Go itself.

## Licence

BSD 3-Clause, inherited from upstream. See [`LICENSE`](LICENSE) and
[`NOTICE-FORK.md`](NOTICE-FORK.md).
