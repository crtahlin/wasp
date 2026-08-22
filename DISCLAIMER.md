# Disclaimer

## No warranty

This software is provided "as is", without warranty of any kind, express or
implied, including but not limited to the warranties of merchantability, fitness
for a particular purpose, and noninfringement. In no event shall the authors or
copyright holders be liable for any claim, damages, or other liability, whether
in an action of contract, tort, or otherwise, arising from, out of, or in
connection with the software or the use or other dealings in the software.

This is the standard BSD 3-Clause disclaimer, restated here because people
install software without reading `LICENSE`. See [`LICENSE`](LICENSE) for the
authoritative text.

## Not the reference client

`wasp` is a personal fork of
[`ethersphere/bee`](https://github.com/ethersphere/bee). It is **not**
affiliated with, endorsed by, supported by, or reviewed by the Swarm Foundation,
the Ethersphere organisation, or any Bee contributor.

Do not report issues with this software to the upstream project. Do not
represent it as an official Bee release. Do not assume that a fix present here
has been reviewed to upstream's standards — by construction, it has not.

## Specific risks of running this

**Financial loss through staking.** A Swarm node with stake at risk can lose it
if it misbehaves during a redistribution round. This software carries changes
that have not been through upstream's review or integration testing, so the
probability of misbehaviour is higher than for stock Bee — and the failure mode
may be one nobody has seen before. **Do not stake an amount you are not prepared
to lose on a node running this software.**

**Data loss.** Changes to storage, chunk handling, or the reserve can corrupt or
lose locally stored data, including data other people are paying you to hold.
Postage batches spent on data that is subsequently lost are not recoverable.

**Network misbehaviour.** A node that misbehaves harms its neighbourhood, not
just itself. Experiments that affect peering, syncing, or retrieval are tested on
unstaked nodes first. If you operate nodes that others depend on, do not run
experimental builds on all of them at once.

**Protocol compatibility is enforced, not guaranteed.** This project freezes the
wire surface and checks it in CI on every pull request, precisely so that these
nodes stay compatible with stock Bee. That check is a strong guard against
accidental incompatibility. It is not a proof of correct behaviour, and it does
not cover emergent effects of changed timing, concurrency, or resource use.

**No support and no stability promise.** There is no service level of any kind.
Releases may break compatibility with their own previous versions, change
defaults, or be withdrawn. Experiments that fail are reverted.

## Before you run it

- Do not run it as your only node.
- Do not run it on a node holding data you cannot lose.
- Do not run it staked, unless the stake is expendable.
- Keep upstream Bee installed and know how to roll back to it — the packages are
  drop-in replacements in both directions.
- Read [`docs/experiments/INDEX.md`](docs/experiments/INDEX.md) to see what is
  actually in the build you are installing.

If any of that is unwelcome, run [upstream
Bee](https://github.com/ethersphere/bee) instead. That is the correct choice for
almost everyone.
