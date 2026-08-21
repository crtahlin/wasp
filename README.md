# bee-experimental

An experimental downstream distribution of [Ethereum Swarm
Bee](https://github.com/ethersphere/bee).

> ### ⚠️ Read this before running it
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
>
> If you want a Swarm node that works, run [upstream
> Bee](https://github.com/ethersphere/bee). Run this only if you understand what
> you are opting into and are prepared to lose whatever the node is holding.

## What it is

Upstream Bee is the reference implementation, and it is conservative for good
reasons — it is the client most of the network runs. That conservatism means
some optimizations and fixes are not worth upstream's risk budget, even when
they are worth an individual operator's.

This repository is where those get built properly instead of living as loose
branches: specified before they are written, developed one to a branch, tested,
documented, measured on real nodes, and shipped in versioned releases.

**It stays protocol-compatible with stock Bee.** These nodes are meant to run on
the real Swarm network alongside everyone else's. The wire surface — protocol
versions, the handshake, chunk geometry, network ID — is frozen and enforced by
a CI check on every pull request. See
[`docs/agent-playbooks/protocol-compatibility.md`](docs/agent-playbooks/protocol-compatibility.md).

## Relationship to upstream

- Tracks upstream **release tags**, absorbed as real merges. The current base is
  in [`.upstream-base`](.upstream-base) and is reported by `bee version`.
- **Nothing is pushed back to `ethersphere/bee`.** This is deliberately not a
  GitHub fork, and the upstream remote is fetch-only. If a change here turns out
  to be worth upstreaming, it gets offered as a normal contribution, by a human,
  on purpose — `scripts/export-patch.sh <slug>` generates a clean patch series
  against current upstream for exactly that.
- Versions are this fork's own line (`v0.1.0` onward) and do not mirror
  upstream's, so nothing here can be mistaken for an official release.

## Install

Packages are drop-in replacements for upstream's `bee` package — same binary
path, same systemd unit, same `/etc/bee/bee.yaml`. They deliberately conflict
with upstream's package, so installing one replaces the other.

```bash
# Debian / Ubuntu
dpkg -i bee-experimental_<version>_amd64.deb

# Docker
docker run ghcr.io/crtahlin/bee-experimental:<version>
```

Existing upstream configuration and data directories are used unchanged, so
rolling back means reinstalling upstream's package.

Releases: <https://github.com/crtahlin/bee-experimental/releases>

## What is in it

- [`docs/ROADMAP.md`](docs/ROADMAP.md) — what is planned
- [`docs/experiments/INDEX.md`](docs/experiments/INDEX.md) — what has landed, and
  what came of it, including the things that did not work

Every experiment is one merge commit on `main`, so the history answers the
question directly:

```bash
git log --first-parent main          # every change this fork makes to Bee
git diff <merge>^1 <merge>           # exactly what one of them changed
```

## Contributing

The process is issue → spec → branch → pull request → merge, and it is not
optional. See [`AGENTS.md`](AGENTS.md) for the rules and
[`docs/agent-playbooks/experiment-lifecycle.md`](docs/agent-playbooks/experiment-lifecycle.md)
for the walkthrough. Upstream's [`CODING.md`](CODING.md) and
[`CODINGSTYLE.md`](CODINGSTYLE.md) still govern the Go itself.

## Licence

BSD 3-Clause, inherited from upstream. See [`LICENSE`](LICENSE) and
[`NOTICE-FORK.md`](NOTICE-FORK.md).
