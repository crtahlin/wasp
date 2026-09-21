# Spec: answer a malformed index-document header with 400, not 500

Issue: [#366](https://github.com/crtahlin/wasp/issues/366). Type: fix. Area: api.
Affects upstream: yes (`pkg/api/dirs.go` is unmodified from bee v2.8.2).

## Problem

A directory upload whose `Swarm-Index-Document` header contains a slash is answered
with **HTTP 500**. The request is malformed, so the correct answer is 400.

`Swarm-Index-Document` names the file a directory serves when a visitor asks for the
directory itself, for example `index.html`. It is a **suffix** appended to a directory
path rather than a path of its own, which is why a slash in it is meaningless and the
code rejects it.

The rejection returns an error built on the spot:

```go
// pkg/api/dirs.go:170-172
if indexFilename != "" && strings.ContainsRune(indexFilename, '/') {
	return swarm.ZeroAddress, errors.New("index document suffix must not include slash character")
}
```

`storeDir` hands it back to `dirUploadHandler`, which classifies errors by identity:

```go
// pkg/api/dirs.go:88-97
switch {
case errors.Is(err, postage.ErrBucketFull):
	jsonhttp.PaymentRequired(w, "batch is overissued")
case errors.Is(err, errEmptyDir):
	jsonhttp.BadRequest(w, errEmptyDir)
case errors.Is(err, tar.ErrHeader):
	jsonhttp.BadRequest(w, "invalid filename in tar archive")
default:
	jsonhttp.InternalServerError(w, errDirectoryStore)
}
```

An error created by `errors.New` at the point of failure is a fresh value that matches
no case, so it reaches `default` and the caller is told the node failed. The two
neighbouring validation failures in the same switch, an empty directory and a bad tar
header, both correctly return 400.

## Cost of leaving it

A client cannot tell this from a genuine node fault. 500 means "try again, it might
work"; 400 means "do not send this again". So the natural client response is to retry an
upload that can never succeed, however many times it is sent. The node also logs an
operator-facing error for what is a caller mistake, which puts a false fault in the
operator's log.

## The change

Give the check a sentinel in the same shape as the `errEmptyDir` that sits above it, and
add a case for it:

```go
var errInvalidIndexDocument = errors.New("index document suffix must not include slash character")
```

```go
case errors.Is(err, errInvalidIndexDocument):
	jsonhttp.BadRequest(w, errInvalidIndexDocument)
```

A sentinel error is a package-level error value compared by identity with `errors.Is`,
which is the pattern `CODING.md` prescribes and the pattern the two working cases in
this switch already use. So this adds no new mechanism.

## The second caller, found by review

**`storeDir` has two callers, not one.** The fork's own local-ingest route calls it at
`pkg/api/localingest.go:182` with the same `Swarm-Index-Document` header, and has its own
error switch handling `errEmptyDir`, `tar.ErrHeader` and `io.ErrUnexpectedEOF`. A first
version of this work changed only `/bzz`, which would have left `POST /wasp/ingest`
answering 500 for a request `POST /bzz` had just started answering 400.

That is worse than the defect being fixed, because the two routes would disagree about
the same header on the same node. The sentinel case is added to that switch too, with its
own test.

This is also what the issue asked for. It says "Worth checking in the same pass whether
any other bare `errors.New` inside `storeDir` reaches the same `default`". The first pass
covered the returns inside `storeDir` and missed the second caller of it, which is the
same question asked one level up.

## What was checked in the same pass, and left alone

**The other errors returned by `storeDir` are correctly 500.** Every one of them
(`read dir stream`, `store dir file`, `add to manifest`, `store manifest`) wraps a
failure of the node's own storage or manifest machinery, which is a genuine node fault.
`errors.New` at line 171 is the only bare one, and the only one describing something the
caller did.

**`Swarm-Error-Document` is deliberately not validated the same way.** It holds a path
rather than a suffix, so a slash in it is legitimate. Adding the same check there would
reject valid requests.

**The `logger.Error(nil, "store dir failed")` line above the switch runs for client
errors too**, so a 400 still writes an error line to the operator's log. That is worth
fixing and is **not** fixed here: it would mean moving the logging inside the switch,
which touches every branch including the ones this issue is not about. Recorded here so
the next reader knows it was seen rather than missed.

**A body that is not a tar archive at all is also answered with 500**, and that is the
same class of defect rather than the same defect. `pkg/api/dirs_test.go:59-74` asserts it
today, as `ErrDirectoryStoreError` with status 500, for a nine byte body reading
`some data`. That failure arrives from `reader.Next()` wrapped as `read dir stream`,
which is a wrapped error rather than a bare `errors.New`, so the fix here does not reach
it and changing it would break a test this issue says nothing about. Filed separately as
[#409](https://github.com/crtahlin/wasp/issues/409) rather than folded in.

## Verification

- A request with `Swarm-Index-Document: dir/index.html` against the directory upload
  route returns **400** with the sentinel's message, where it returned 500 before. This
  is a new test; no existing test covers the slash case, checked by grep over
  `pkg/api/dirs_test.go`.
- A request with a valid `Swarm-Index-Document: index.html` still succeeds and still
  writes the index suffix into the manifest root metadata, so the change did not turn a
  working header into a rejected one. The existing tests already cover this and must
  keep passing.
- A mutation: returning the sentinel from a path that is a genuine node fault must fail
  the 500 case, and removing the new switch case must fail the 400 case.

## Scope

`pkg/api/dirs.go` and `pkg/api/dirs_test.go`. No wire change, no configuration, no new
endpoint. The status code for one malformed request changes, which an operator can
observe, so `docs/DIFFERENCES.md` gains a row per rule 13.
