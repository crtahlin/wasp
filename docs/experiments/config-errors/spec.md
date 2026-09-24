# A bad configuration value should stop the node, and say so

Issues: [#489](https://github.com/crtahlin/wasp/issues/489),
[#490](https://github.com/crtahlin/wasp/issues/490)

Both were found setting up a node for staking, from one typo. One spec because
the second defect is what made the first hard to diagnose, and the fixes are in
the same path.

## Problem

`stake-recovery-on-startup` takes the word `off`. YAML 1.1 coerces a bare `off`
to a boolean, so the obvious line gives the node `"false"`:

```yaml
stake-recovery-on-startup: off
```

```
invalid stake-recovery-on-startup "false": must be off, withdraw or migrate
```

The setting is wasp's own, its documented default is `off`, and nothing says it
must be quoted. Writing the default, changing no behaviour, produces a node that
will not start.

Then the node **exits 0**. `NewBee` returns the error, the deferred handler at
`pkg/node/node.go:477-483` logs `got error, shutting down...`, and the program's
start function at `cmd/bee/cmd/start.go:94-96` logs `failed to build bee node`
and returns. Nothing reaches `main`, which exits non-zero only when
`cmd.Execute()` returns an error. systemd therefore logs:

```
bee.service: Deactivated successfully.
bee.service: Scheduled restart job, restart counter is at 1.
```

With `Restart=always` in `packaging/bee.service`, that repeated every five
seconds. Measured on a real node: `NRestarts` reached 11.

Three consequences, all worse than the original typo:

- **`systemctl is-active` says `active`.** systemd has no reason to think
  otherwise. The obvious liveness check passes on a node that is restarting
  every five seconds, and only `NRestarts` distinguishes them.
- **The journal buries the cause.** Each cycle writes a full startup banner and
  shutdown sequence. After eleven cycles the most visible errors were
  `could not get price` and `could not get blockchain log`, which are
  consequences of the shutdown, and reading them first points at the chain
  endpoints rather than the config.
- **Each attempt dials both chain endpoints for nothing**, consuming public
  endpoint rate limit that a staking node needs for commit and reveal.

## The change

### 1. Accept `false` as `off`

At `pkg/node/node.go:299`, `case "", "off":` gains `"false"`.

A YAML `off` can only have meant `off`, so accepting it loses no information.
**`"true"` stays invalid**, because it does not say whether `withdraw` or
`migrate` was wanted, and guessing on a setting that moves stake would be worse
than refusing to start.

The error message also names the cause, so an operator who reaches it another way
is pointed at YAML rather than left to wonder where `"false"` came from.

### 2. A failed node build exits non-zero, and a config error exits distinctly

Required for anything in the unit to work: today there is no failure for systemd
to see.

- A new sentinel, `node.ErrConfig`, wraps configuration validation failures.
- `start.go` propagates a build failure instead of returning, so `Execute()`
  returns an error and `main` exits non-zero.
- `main` exits **78** when the error is `ErrConfig`, and **1** otherwise. 78 is
  `EX_CONFIG` from `sysexits.h`, which is conventional and does not collide with
  a signal-derived status.
- `packaging/bee.service` gains `RestartPreventExitStatus=78`.

Result: a bad config leaves the unit `failed` with the reason in
`systemctl status`, and a genuine crash still restarts as it does today.

### Which failures are unrecoverable, and why the line is drawn narrowly

This is the part that can do damage if it is wrong, because marking a recoverable
failure as unrecoverable turns a transient problem into an outage needing a
human.

**Unrecoverable, gets `ErrConfig`:** a value that fails validation. Restarting
cannot change the file.

**Recoverable, unchanged:** an unreachable or rate-limiting chain endpoint, a
peer failure, anything transient. A node whose RPC provider is briefly down must
come back on its own. This is the case the narrow scope protects, and it is why
"exit non-zero on any startup error" is rejected: it would stop a node that only
needed to wait.

**Deliberately not decided here:** a corrupt or locked data directory. It is
arguably unrecoverable, but the failure modes are not enumerated and guessing
would risk stopping a node that a retry would have fixed. It keeps today's
behaviour and gets its own issue if it proves to matter.

## Protocol impact

None. No wire format, no protocol version, nothing under `pkg/p2p`, `pkg/swarm`
or `pkg/config`. `.github/protocol-freeze.lock` is untouched.

## Tests

1. `stake-recovery-on-startup` accepts `off`, `""` and `false`, and rejects
   `true` and an arbitrary string. Table test on the validation function.
2. The rejection error is identifiable as `ErrConfig` with `errors.Is`.
3. A recoverable startup error is **not** `ErrConfig`, which is the assertion
   that stops the narrow scope being widened by accident later.
4. The exit-status mapping: `ErrConfig` maps to 78, any other error to 1, nil to
   0. Tested on the mapping function, not by running the binary.

**The mutation that must kill each:** removing `"false"` from the accepted set
must fail test 1; returning a bare `fmt.Errorf` instead of wrapping `ErrConfig`
must fail test 2; wrapping a chain-dial error in `ErrConfig` must fail test 3;
returning 1 for `ErrConfig` must fail test 4.

Test 3 matters most. Tests 1, 2 and 4 confirm the feature works; only test 3
defends the property that makes it safe.

## Verification

Unit tests cover the logic. On a real node, the check is that the original typo
now leaves the unit `failed` rather than `active`, with `NRestarts` not climbing,
and that a node with a valid config and a deliberately unreachable first RPC
endpoint still starts by failing over.

`stake-1` reproduced the original fault, so it is where the fix is confirmed,
after the reserve has filled and gate 1 has run. Not before: it is a staking
node and restarting it to test a startup path is not something to do while it is
being measured.

## Files

- `pkg/node/node.go`, the accepted set, the message, and `ErrConfig`
- `cmd/bee/cmd/start.go`, propagate the build failure
- `cmd/bee/main.go`, the exit-status mapping
- `packaging/bee.service`, `RestartPreventExitStatus=78`
- `docs/DIFFERENCES.md`, the setting's row, and the new packaging behaviour

Generated with help of AI.
