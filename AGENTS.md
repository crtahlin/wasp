# AGENTS.md

> **This repository is `crtahlin/wasp` — an experimental downstream
> distribution of [`ethersphere/bee`](https://github.com/ethersphere/bee).**
> It is not the upstream project and is not affiliated with or endorsed by the
> Swarm Foundation. Upstream's own instructions follow below and still apply;
> where the two disagree, **this section wins**.

## Fork rules (non-negotiable)

**1. Nothing goes back to Ethersphere.**
The `upstream` remote is fetch-only — its push URL is deliberately set to
`DO_NOT_PUSH`. Never push to it, never open a pull request against
`ethersphere/bee`, never comment on an upstream issue or pull request on the
maintainer's behalf. Always pass `--repo crtahlin/wasp` to
`gh pr create` and `gh issue create`. This repository is deliberately **not** a
GitHub fork, so nothing about the platform will suggest upstream as a target;
keep it that way.

**2. No code before an issue and a merged spec.**
The order is: issue → spec merged → branch → implement with tests and docs →
pull request → merge → ledger row → marker tag. See
`docs/agent-playbooks/experiment-lifecycle.md`. A change that arrives as code
first gets sent back, however good it is.

**3. Merge commits are the changelog.**
Every change lands on `main` as a `--no-ff` merge commit whose **subject is the
conventional-commit line**, for example
`feat(pushsync): parallel chunk dispatch (#12)` — not GitHub's default
`Merge pull request #12 from …`.

```bash
gh pr merge <n> --merge --subject "feat(scope): what changed (#<n>)"
```

This one rule carries three separate loads: it is what `git-cliff` filters on to
build the changelog, what makes each experiment permanently inspectable
(`git diff M^1 M`), and what keeps the upstream merge base advancing so each
sync stays cheap. Breaking it breaks all three at once.

**4. Review before merging, then merge.**
Run `/review-pr` on a pull request before merging it, and act on what the review
finds rather than filing it away. Merging after a clean review is the normal
path and needs no further sign-off — waiting for approval on every change is
not the process. Pause for the operator only on something genuinely risky:
a protocol change, a migration, or anything that could affect a running node's
stake or data. There should rarely be such a change.

**5. Never squash-merge. Never delete a branch. Never force-push `main`.**
Squash and rebase merging are disabled at the repository level and automatic
branch deletion is off — do not re-enable them. Experiment branches are the
historical record.

**6. The wire surface is frozen.**
This client must stay protocol-compatible with stock Bee nodes.
`.github/protocol-freeze.lock` fingerprints every constant that determines
whether a stock peer will talk to us. Changing it requires an explicit
**Protocol impact** section in the spec and the `protocol-change` label. Read
`docs/agent-playbooks/protocol-compatibility.md` before touching anything under
`pkg/p2p/`, `pkg/swarm/`, or `pkg/config/`.

**7. Claims are measured, not asserted — and once is not measured.**
An optimization is not accepted because the code looks faster. It is accepted
because a before-and-after run on the test bench says so, with the numbers
recorded in the experiment's `measurement.md`.

**At least three runs per condition, reported with the spread.** Two runs on
this bench with identical code and identical peer count differed by 1.82x on
disk time per chunk. A single run measures the node's mood, and has already
produced one wrong published figure here. Match node state across a comparison
and normalise per unit of work. See `docs/agent-playbooks/test-bench.md`.

An HTTP 200 from an API endpoint is never proof that a node is healthy.

**8. Disclose AI assistance — in prose, not in commits.**
Issues, pull request descriptions, and comments in this repository end with:

```
Generated with help of AI.
```

That is deliberate for this repository and reverses the usual convention. The
work here is largely analytical — performance claims, protocol-compatibility
arguments, assessments of whether an upstream behaviour is a bug. A reader
weighing such an argument is entitled to know how it was produced.

Commit messages stay clean: **no `Co-Authored-By` trailers and no tool names.**
The commit log is a permanent technical record, and branches here are never
deleted, so anything landed there is effectively immovable. Note that
`git cherry-pick` preserves trailers by default — when re-landing work from
elsewhere, write the message explicitly rather than using `cherry-pick -x`.

**9. Nothing sensitive in the repository.**
This repository is public. No node addresses, hostnames, SSH configuration,
private keys, wallet addresses, or postage batch identifiers — in code, issues,
specs, or results. Bench machines are referenced by role name (`bench-1`,
`mainnet-canary`) only.

## Where the detail lives

| If you are… | Read |
|---|---|
| Starting or shipping an experiment | `docs/agent-playbooks/experiment-lifecycle.md` |
| Absorbing a new upstream release | `docs/agent-playbooks/upstream-sync.md` |
| Cutting a release | `docs/agent-playbooks/release-process.md` |
| Touching anything protocol-adjacent | `docs/agent-playbooks/protocol-compatibility.md` |
| Running or measuring on real nodes | `docs/agent-playbooks/test-bench.md` |
| Provisioning a bench machine | `docs/agent-playbooks/bench-vm-spec.md` |
| Looking for what has been tried | `docs/experiments/INDEX.md` |
| Looking for what is planned | `docs/ROADMAP.md` |

## Fork-specific facts

- `main` tracks upstream **release tags**, never `master` HEAD. The current base
  is recorded in `.upstream-base` and is the single source of truth for it.
- The Go module path stays `github.com/ethersphere/bee/v2`. Do not rewrite it.
- Version numbers are this fork's own line (`v0.1.0` onward) and do not mirror
  upstream's. `bee version` reports both.
- New fork-authored `.go` files carry
  `Copyright <year> The Wasp Authors.` — do not claim Swarm
  authorship for new work. Files inherited from upstream keep their existing
  headers untouched.
- **Wrap commit message bodies at 72 characters.** `commitlint.config.js` sets
  `footer-max-line-length: 72`, and commitlint treats a trailing paragraph
  containing an issue reference as a *footer* — so a normal-looking paragraph
  that mentions `#123` fails CI at 73 characters while the same paragraph
  without the reference passes. Wrapping everything at 72 avoids the trap.
- `make vet` does not exist despite the upstream checklist below mentioning it.
  The real sequence is `make format`, `make build`, `make test`, `make lint`,
  plus `make protocol-freeze` whenever the change is anywhere near the wire.

---

*Everything below this line is upstream's `AGENTS.md`, carried unmodified so it
stays mergeable. It describes the Bee codebase itself and remains accurate.*

Project instructions for **AI coding assistants and agents** (OpenAI Codex, Cursor, GitHub Copilot, Claude Code, and similar tools). This file is the canonical source of shared project instructions; `CLAUDE.md` imports this file for Claude Code.

## Project overview

Bee is the reference Go implementation of an Ethereum Swarm node. It implements decentralized storage and communication: content-addressed chunk storage, Kademlia-based routing, postage stamp accounting, push/pull syncing, PSS messaging, feeds, and storage incentives (redistribution game).

**Module**: `github.com/ethersphere/bee/v2`

**Go version**: 1.26 (see `go.mod`)

**License**: BSD 3-clause (see `LICENSE`)

Human-oriented contributing docs: `CONTRIBUTING.md`, `CODING.md`, `CODINGSTYLE.md`, `README.md`.

## Guidelines

Keep changes **minimal and focused**. Only touch code that belongs to the task. Do not refactor unrelated code, rename symbols for style only, or mix unrelated fixes in one commit or PR.

Read **`CONTRIBUTING.md`**, **`CODING.md`**, and **`CODINGSTYLE.md`** for process, patterns, and style. Prefer matching existing naming, types, imports, and log style in the files you edit.

Do **not** add, remove, or update `go.mod` dependencies unless the task **explicitly** requires it or the person asking for the work **explicitly** requests a dependency change.

Handle errors and logging the way this repo does: propagate errors with context (`fmt.Errorf("…: %w", err)`), avoid logging and returning the same error, and use structured logging with clear operator vs developer levels (see `CODING.md`).

Prefer **`package foo_test`** tests, **`export_test.go`** when you must export internals, and **`t.Parallel()`** only where it is safe. Add or update tests when behavior changes. Integration tests use **`-tags=integration`**.

## Pre-commit checklist

Before you finish a change set (especially before a commit or PR), run these and fix failures:

1. **Formatting** — `make format` (gofumpt + gci; see `CODING.md`).
2. **Compile** — `make build` (all packages) and, when you need the binary artifact, `make binary` (`dist/bee`, `CGO_ENABLED=0`).
3. **Tests** — `make test` (unit tests, `-failfast`). For a single package use `go test ./pkg/<name>/...`. Use `make test-race` when concurrency is central to the change. Use `make test-integration` only when you touch integration-tagged code.
4. **Static checks** — `make lint` and `make vet` (see `.golangci.yml`).

CI pipelines may use `make test-ci` / `make test-ci-race` (see `Makefile` for flags).

## Dev commands (quick reference)

```bash
make binary     # dist/bee
make build      # compile all packages
make test       # unit tests
make test-race  # unit tests + race detector
make lint       # golangci-lint (see .golangci.yml)
make vet        # go vet
make protobuf   # regenerate *.pb.go after changing .proto files
```

## Architecture

### Entry point and CLI

Binary built from `cmd/bee/main.go`. CLI uses Cobra + Viper:

- `bee start` — full or light node (`cmd/bee/cmd/start.go`)
- `bee init` — initialize data directory
- `bee deploy` — deploy smart contracts
- `bee db` — database management
- `bee version` — print version info

Configuration: option constants in `cmd/bee/cmd/cmd.go`. Viper reads CLI flags, environment variables (`BEE_` prefix), and YAML config.

### Node bootstrap

`pkg/node/node.go` is the main orchestrator. `NewBee()` wires subsystems via dependency injection; avoid global mutable state. The `Bee` struct holds service references and provides `Shutdown()` for teardown.

### HTTP API

- Router: `gorilla/mux` in `pkg/api/router.go`
- Route groups in `Mount()`:
  - `mountTechnicalDebug()` — `/node`, `/addresses`, `/health`, `/readiness`, `/metrics`, `/loggers`, pprof
  - `mountBusinessDebug()` — topology, accounting, settlements, stamps management
  - `mountAPI()` — `/bytes`, `/chunks`, `/bzz`, `/feeds`, `/soc`, `/stamps`, `/tags`, `/pins`, `/pss`, `/grantee`
- `checkRouteAvailability` can block endpoints during sync
- OpenAPI: `openapi/Swarm.yaml` (API versioning follows SemVer there; the main Bee release version does not)
- Endpoints exist at root (e.g. `/bytes`) and under `/v1/` (e.g. `/v1/bytes`)

### P2P networking

- Transport: libp2p (`pkg/p2p/libp2p/`)
- Wire formats: protobuf (gogo) — each protocol area has a `pb/` directory with `.proto` and `doc.go` (`go:generate` calling `protoc` + `--gogofaster_out`)
- Important protocol packages: `pushsync`, `pullsync`, `retrieval`, `pingpong`, `hive`, `pricing`

### Storage

- Chunk types: CAC (`pkg/cac/`), SOC (`pkg/soc/`)
- Interfaces: `pkg/storage/` (`Putter`, `Getter`, `Hasser`, `Deleter`)
- Local store: `pkg/storer/` (reserve, cache, upload, pinning)
- Blob engine: `pkg/sharky/`
- BMT: `pkg/bmt/`
- State: `pkg/statestore/` (LevelDB); `pkg/shed/` (typed LevelDB layer)

### Postage and incentives

- `pkg/postage/` — batches, stamps, services
- `pkg/postage/listener/` — on-chain events
- `pkg/postage/postagecontract/` — contract interaction
- Stamps: batch ID, depth (capacity), amount (per-chunk value)
- `pkg/storageincentives/` — redistribution / storage incentive game

## Key domain concepts

- **Address** — 32-byte hash (`pkg/swarm/`). Chunk and overlay addresses; proximity is XOR-based (more shared prefix bits = closer), not lexicographic ordering.
- **Chunk** — 4096 bytes of data (`ChunkSize = SectionSize * Branches = 32 * 128`), plus 8-byte span (`SpanSize`); `ChunkWithSpanSize = 4104`.
- **CAC** — content-addressed chunk; address from BMT root of data.
- **SOC** — single owner chunk; address from owner + id, with signature.
- **PO** — proximity order (shared prefix bits). `MaxPO = 31`, `ExtendedPO = 36`.
- **Neighborhood** — prefix / responsibility region for storage and sync.
- **Kademlia** — routing table over XOR distance (`pkg/topology/`).
- **Postage stamp** — payment signal attached to chunks.
- **Push sync / pull sync** — push new data toward neighborhood; pull historical sync between peers.
- **Redistribution** — incentive game proving reserve storage.

## Coding conventions (summary)

### Copyright (goheader)

Every `.go` file starts with:

```go
// Copyright <year> The Swarm Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.
```

### Errors, logging, concurrency

- Propagate errors; do not log and return the same error. Use `fmt.Errorf("context: %w", err)`. Avoid stacking "failed to" prefixes.
- Sentinel errors: `var ErrFoo = errors.New("package: description")` — identity only, compared with `errors.Is`.
- Typed errors: a struct implementing `error` with exported fields, inspected with `errors.As` when callers need data about the failure.
- Logging: separate operator-facing (`Error`/`Warning`) from developer detail (`Debug`, V-levels). Keys: `lower_snake_case`, specific names. Runtime log tuning: `/loggers` API.
- Every goroutine needs a clear shutdown path. Channels: prefer unbuffered or size 1 unless strongly justified; an owning goroutine sends or closes.

### Testing

- Prefer external test packages: `package foo_test` not `package foo`.
- `export_test.go` in the real package to export symbols only for tests.
- Use `t.Parallel()` where safe. Avoid the word `fail` in test names. Integration: `-tags=integration`. Prefer `t.Fatal` / `t.FailNow` over `panic` in tests.

### Style and tooling

- American English (e.g. marshaling, canceled).
- Avoid `init()` where possible (`gochecknoinits`).
- Enums often start at `iota + 1` when zero should mean "unset".
- Use `time.Time` / `time.Duration`, not raw ints for time.
- `var _ Interface = (*Impl)(nil)` where useful.
- Dependency injection over mutable globals. Exit only from `main()`.

### Commits

Never commit or push to git.

## Common pitfalls

- Do not confuse `ChunkSize` (4096 data bytes) with `ChunkWithSpanSize` (4104 including span).
- XOR distance: XOR between two addresses produces smaller integers as more prefix bits are shared, do not confuse this with lexicographic ordering of addresses.
- Goroutines must be stoppable (context cancel, quit channel, etc.).
- Full node vs light node: reserve and storage incentives are full-node concerns.
- Postage batches can be unusable (expired, depleted, unsynced); check before relying on stamps.
g