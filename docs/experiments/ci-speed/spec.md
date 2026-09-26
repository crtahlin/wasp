# Cut pull request CI from about 20 minutes to about 11

Issue: [#519](https://github.com/crtahlin/wasp/issues/519). Also settles
[#507](https://github.com/crtahlin/wasp/issues/507).
Type: chore.

## Problem

A pull request that changes code waits about 20 minutes for the Go workflow,
and one flaky test adds a full re-run.

**Table: Go workflow job durations on recent green runs, before this change**

| Job | Duration |
|---|---|
| Test (macos-latest), race detector | 15 to 21 min |
| Test (ubuntu-latest), race detector | 16 to 17 min, the test step 960 s |
| Test (windows-latest), no race detector | 7 to 9 min |

On Ubuntu with the race detector, three packages take most of the test step:

- `pkg/api` takes 648 s, and almost all of that is one upstream test,
  `TestBzzUploadDownloadWithRedundancy`: 123 of the package's 127 s locally.
- `pkg/storageincentives` takes 567 s on CI but 30 s locally, so its mining
  tests scale badly on a 4-core runner with the race detector.
- `pkg/storer` takes 392 s.

Packages already run in parallel, so the job cannot finish before `pkg/api`
does.

The test matrix runs with GitHub's default `fail-fast: true`. On 2026-09-25,
`TestDiscoverDNS` failed twice on macOS on a transient DNS error (#507), and each
time GitHub cancelled the healthy Ubuntu job as well, so both needed a full
re-run.

## Hypothesis

Most of the wait is duplicated or serialised work that adds no coverage:

- **The race detector runs twice.** It runs on both macOS and Ubuntu, and a
  data race does not depend on the operating system.
- **The critical path is one package.** Running `pkg/api` in its own job lets
  everything else finish in parallel with it.
- **Re-runs come from a test that depends on the internet** and from
  `fail-fast`, not from defects.

## Design

### 1. No fail-fast

`strategy.fail-fast: false` on every test matrix, so a failure on one job never
cancels another.

### 2. The race detector on Ubuntu only

macOS runs `make test-ci`, without the race detector, as Windows already does.
It still builds and tests on macOS, which is what that job is for.

### 3. The Ubuntu race run split into three parallel jobs

| Shard | Packages |
|---|---|
| `api` | `./pkg/api/...` |
| `storage` | `./pkg/storageincentives/...`, `./pkg/storer/...` |
| `rest` | every other package from `go list ./...` |

`rest` is computed as the complement of the other two, so a new package can
never be left out.

The shards run through `make`, which exports `GOEXPERIMENT=nogreenteagc` (#69).
Calling `go test` directly would silently drop that setting. The Makefile's
`test-ci` and `test-ci-race` targets take a `TEST_PKGS` variable, which
defaults to `./...`, so running them locally is unchanged.

The SIMD and Green Tea guard steps run once, in the `rest` shard.

**The required check keeps its name.** Branch protection requires
`Test (ubuntu-latest)`. A final job with exactly that name depends on the three
shards, runs even when one fails, and passes only when all three passed. A
documentation-only pull request still reports it, since the shards report
success after skipping their work, as the current job does.

### 4. `TestDiscoverDNS` skips on a DNS resolution error (#507)

When the lookup fails with a resolver error (`*net.DNSError` anywhere in the
error chain), the test skips with the error in its message. It still fails when
the records resolve to something wrong, or resolve to nothing, which is what it
exists to catch. If the error chain does not carry `*net.DNSError`, the
implementation says so and matches the resolver's error explicitly.

## Protocol impact

**None.** Only the CI workflow, the Makefile test targets and one test change.

## Measurement

- **The pull request's own run**, and the next five code pull requests: the
  duration of each Go workflow job, and the time until the last required check
  reports, compared with the table above.
- **Re-runs**: count the runs that needed one.
- **The shards cover every package.** In the pull request's run, the union of
  the packages the three shards tested equals `go list ./...`.

The expected result is about 11 minutes until the last required check reports,
set by `pkg/api`, and no job cancelled by another's failure. A negative result
is a shard that runs as long as the old job, which would mean the runner, not
the package, is the limit.

## Rollout and rollback

Takes effect for every pull request once merged. Rollback is reverting the
merge.

## Upstream portability

Not applicable. The workflow and the Makefile targets are the fork's own, and
the DNS test change is a test-only robustness fix.

## Files

- `.github/workflows/go.yml`
- `Makefile`: `TEST_PKGS` in `test-ci` and `test-ci-race`
- `pkg/p2p/discover_test.go`
- `docs/experiments/INDEX.md`

Generated with help of AI.
