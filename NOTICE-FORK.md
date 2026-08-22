# Fork notice

`wasp` is a derivative work of
[`ethersphere/bee`](https://github.com/ethersphere/bee), the reference Go
implementation of an Ethereum Swarm node.

## Licence

Upstream Bee is licensed under the **BSD 3-Clause License**, and this derivative
work is distributed under the same licence. The original copyright notice and
licence text are retained unmodified in [`LICENSE`](LICENSE).

Under clause 3 of that licence, the names of the copyright holder and its
contributors may not be used to endorse or promote this derivative work. This
project therefore makes no claim of affiliation with, endorsement by, or support
from the Swarm Foundation, the Ethersphere organisation, or any Bee contributor.
Any such impression is unintended — please open an issue if something here reads
that way.

## Copyright headers

Files inherited from upstream retain their original
`Copyright <year> The Swarm Authors` headers unchanged. Files authored for this
fork carry `Copyright <year> The Wasp Authors`. Both are accepted by
the `goheader` linter, configured in [`.golangci.yml`](.golangci.yml).

## What has been changed

The authoritative answer is the git history — every change this fork makes to
upstream Bee is one merge commit on `main`:

```bash
git log --first-parent main
```

The human-readable summary is [`docs/experiments/INDEX.md`](docs/experiments/INDEX.md).

Beyond the experiments themselves, the following structural changes were made
when the fork was created:

- Removed CI workflows that depend on Ethersphere organisation secrets and
  infrastructure (beekeeper integration clusters, the OpenAPI docs preview, the
  Codecov upload, the swarm-cli version dispatch).
- Rewrote the release pipeline to publish to this project's own namespace rather
  than Ethersphere's Docker Hub, Quay, Gemfury, Homebrew tap, and Scoop bucket.
- Corrected the package licence metadata, which upstream's `.goreleaser.yml`
  declares as `GPL-3` while the project is BSD 3-Clause.
- Replaced the README, issue templates, and pull request template, and prepended
  fork-specific rules to `AGENTS.md`.
- Added an independent version line, a build-time record of the upstream base,
  and a startup warning identifying the binary as experimental.

## The upstream project

Bee is developed by the Swarm community at
<https://github.com/ethersphere/bee>. Problems with **that** software belong
there. Problems with **this** software belong at
<https://github.com/crtahlin/wasp/issues> and must not be reported
to upstream as Bee bugs.
