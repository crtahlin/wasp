# Spec: keep the error chain when a dispersed-replica put fails

Issue: [#337](https://github.com/crtahlin/wasp/issues/337). Type: fix. Area: storage.
Affects upstream: yes (`pkg/file/pipeline/hashtrie` is unmodified from bee v2.8.2).

## Problem

`hashtrie.Sum` flattens the error from the dispersed-replica put into a string, so no
caller can test what actually went wrong.

A **dispersed replica** is an extra copy of a file's root chunk, stored at a deliberately
different address so that the file stays reachable when the neighbourhood holding the
original is unavailable. It is written once, at the end of a split, when redundancy is
switched on.

`pkg/file/pipeline/hashtrie/hashtrie.go:265-268`:

```go
err = h.replicaPutter.Put(h.ctx, swarm.NewChunk(swarm.NewAddress(rootHash[:swarm.HashSize]), rootData))
if err != nil {
	return nil, fmt.Errorf("hashtrie: cannot put dispersed replica %s", err.Error())
}
```

The format verb is `%s` against `err.Error()` rather than `%w` against `err`. `%w` keeps
the original error reachable, which is what lets `errors.Is` and `errors.As` find it.
`%s` copies its text and drops the value, so both return false for anything the putter
returned, and the original error is unreachable from the returned one.

## Cost of leaving it

This is the **last** put in a split, made inside `Sum` after the whole body has been
read, and it runs through `replicas.putter.Put`, which fans its work out to several
goroutines and combines their errors with `errors.Join`. `errors.Join` is built to be
searched with `errors.Is`, so the chain survives every step up to this line and is
destroyed only here.

That matters for a caller supplying its own putter, which is the only way to observe a
split as it happens: `requestPipelineFn` returns just
`func(context.Context, io.Reader) (swarm.Address, error)` and runs to end of input, so a
handler enforcing a condition of its own has nowhere else to stand. Such a caller cannot
tell its own condition from a genuine storage failure, sees an opaque string, and has to
answer 500 to a request it could have answered precisely.

`CODING.md` states the convention this breaks: propagate errors with context using
`fmt.Errorf("context: %w", err)`.

It is also a slip rather than a house style, which is part of why it is worth fixing
rather than working around. `grep -rn 'fmt.Errorf(.*%s".*err.Error())' pkg/file/` returns
exactly this line, and `pkg/file` contains one occurrence of `err.Error())` in total.

## The change

```go
return nil, fmt.Errorf("hashtrie: cannot put dispersed replica: %w", err)
```

Note the added colon before the verb, which is the separator the rest of the codebase
uses and which the original line is missing.

## What this does change, found by review

An earlier version of this spec said nothing an operator can observe changes. **That was
wrong**, and the same claim was in the pull request.

Making the cause reachable makes it reachable for every `errors.Is` upstream of the call,
and three upload handlers test for exactly one cause that can arrive this way:
`pkg/api/dirs.go`, `pkg/api/bzz.go` and `pkg/api/bytes.go` each answer 402 Payment
Required on `postage.ErrBucketFull`. The pipeline passes the same putter as the replica
putter (`pkg/file/pipeline/builder/builder.go:38`), and in the API that putter stamps
every chunk it is given, so a bucket filling on the dispersed-replica put is a real
condition rather than a hypothetical one.

Before this change those handlers could not see it and answered **500**. After it they
answer **402**, which is the correct answer and the one they already give when a bucket
fills earlier in the same upload. `docs/DIFFERENCES.md` gains a row for this, per rule 13.

No other error changes status. The empty-directory, bad-tar-header and
malformed-index-document cases cannot appear in a putter's error chain.

## What this does not do

It does not change what `Sum` returns to a caller that only prints the error: the text is
the same but for that colon. It does not change when the error occurs, what is stored, or
how many replicas are attempted. Nothing on the wire is involved.

## Verification

- A new unit test: a putter returning a sentinel error, driven through `hashtrie.Sum` at
  a redundancy level above `NONE`, asserting `errors.Is` finds the sentinel in what
  `Sum` returns. That test fails on the unmodified line and passes after it, which is
  the whole of the claim.
- The test must use a level above `NONE`, because `replicas.putter.Put` returns nil
  immediately at level 0 and the failing line is never reached. **Only `NONE` is
  vacuous**, and `NONE` is the Go zero value rather than a package default: an earlier
  version of this spec said "a test written at the default level would pass either way",
  which is wrong, since `DefaultUploadLevel` is `MEDIUM` and `DefaultDownloadLevel` is
  `PARANOID` and the test works at both.
- The existing `hashtrie` tests keep passing, so no error that previously reached a
  caller stops reaching it.
- A mutation: putting `%s` and `err.Error()` back must fail the new test.

## Scope

One line in `pkg/file/pipeline/hashtrie/hashtrie.go` and one new test in
`pkg/file/pipeline/hashtrie/hashtrie_test.go`. No configuration, no wire change, and
nothing an operator can observe, so `docs/DIFFERENCES.md` gains no row.
