# Results: pull request CI from about 17 minutes to about 9

Issue: [#519](https://github.com/crtahlin/wasp/issues/519), also settling
[#507](https://github.com/crtahlin/wasp/issues/507). Spec: [spec.md](spec.md).
Merged as `c7812aef` (#521), tag `exp-ci-speed`.

## Outcome

**Validated.** Across the first three Go workflow runs that tested code after
the change, the required check `Test (ubuntu-latest)` reported 6.5 to 10.1
minutes after the run started, against 16.8 to 17.7 minutes in the three runs
before it. The spec expected about 11 minutes. No run needed a re-run and no
job was cancelled by another's failure.

## Time until the last required check reports

**Table: Go workflow, run start to `Test (ubuntu-latest)` result, code-changing runs on GitHub-hosted runners**

| Run | Before or after | Minutes |
|---|---|---|
| `fix/511-identify-wait`, pull request | before | 17.0 |
| `fix/511-identify-wait`, pull request | before | 17.7 |
| `main`, push after #516 | before | 16.8 |
| `fix/519-ci-speed`, pull request (the change's own run) | after | 10.1 |
| `main`, push after #521 | after | 6.5 |
| `fix/522-connect-already-connected`, pull request | after | 9.3 |

Documentation-only runs are left out, because the test jobs skip their work
there both before and after the change.

## Job durations

**Table: Go workflow job durations in minutes, the same runs**

| Job | Before (two runs) | After (three runs) |
|---|---|---|
| Test (ubuntu-latest), race detector, one job | 16.4, 16.8 | replaced by the three shards below |
| Test race (api) | | 6.7, 6.4, 6.5 |
| Test race (storage) | | 9.6, 6.0, 9.2 |
| Test race (rest) | | 5.6, 6.0, 5.8 |
| Test (macos-latest) | 15.0, 17.1 (race detector) | 4.8, 4.7, 4.7 (no race detector) |
| Test (windows-latest) | 8.6, 6.2 | 6.7, 7.1, 6.5 |
| Lint | 2.5, 2.3 | 2.6, 2.7, 2.1 |

**The critical path is now the `storage` shard, not `api`.** The spec expected
`pkg/api` to set the time. It takes a steady 6.4 to 6.7 minutes, while
`storage` (`pkg/storageincentives` and `pkg/storer`) varies from 6.0 to 9.6.
That matches the spec's observation that `pkg/storageincentives` scales badly
on a 4-core runner under the race detector: its time depends on the runner.
Splitting `storage` into two shards is the next step if the wait matters again.
It is not done here.

## The shards cover every package

In the run for `fix/522-connect-already-connected`, the three shards tested
223 distinct packages: 1 in `api`, 17 in `storage` and 205 in `rest`. That is
exactly the list `go list ./...` gives for the same tree, and no package was
tested in two shards.

## #507

`TestDiscoverDNS` did not fail in any of the three runs. With `fail-fast` off,
a failure of this test on one runner would no longer cancel the others, so even
a remaining transient DNS failure would cost one job re-run rather than two.

Generated with help of AI.
