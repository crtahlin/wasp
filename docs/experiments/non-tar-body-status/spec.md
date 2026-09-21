# Spec: answer a body that is not an archive with 400, not 500

Issue: [#409](https://github.com/crtahlin/wasp/issues/409). Type: fix. Area: api.
Affects upstream: yes. **Corrected**: an earlier version of this line said
`pkg/api/dirs.go` is unmodified from bee v2.8.2. It is not, and was not when this spec
merged: the #366 fix landed on `main` first and added twelve lines to that file. What is
unmodified is the part that matters, the handler's error switch, which upstream carries as
`ErrBucketFull / errEmptyDir / tar.ErrHeader / default` with no case for this one.

## Problem

A directory upload to `POST /bzz` whose body is not a valid tar archive is answered with
**HTTP 500**. The caller declared the body as tar and sent something else, so the request
is malformed and the correct answer is 400.

`storeDir` reads the archive one entry at a time and wraps any read failure:

```go
// pkg/api/dirs.go
fileInfo, err := reader.Next()
if errors.Is(err, io.EOF) {
	break
} else if err != nil {
	return swarm.ZeroAddress, fmt.Errorf("read dir stream: %w", err)
}
```

`dirUploadHandler` then classifies by identity. It has a case for `tar.ErrHeader`, which
returns 400, but a body that stops part way through does not produce `tar.ErrHeader`. It
produces `io.ErrUnexpectedEOF`, which matches no case and reaches `default`.

**`archive/tar` reports the two cases differently and both are the caller's fault.** A
full-size block of nonsense is an invalid header; anything shorter than one 512 byte
header block, or a body cut off mid-archive, is an unexpected end of input.

## It is asserted as correct today

`pkg/api/dirs_test.go`, the `non tar file` case, sends a nine byte body reading
`some data` with the tar content type and asserts 500 with `ErrDirectoryStoreError`. So
this is not an untested corner: the behaviour is pinned by a test, and fixing it means
changing that test. That is why it is a deliberate change rather than something to fold
into another pull request, and it is why [#366](https://github.com/crtahlin/wasp/issues/366)
left it alone.

## The fork already answers this correctly on its own route

`POST /wasp/ingest` calls the same `storeDir` and handles the case
(`pkg/api/localingest.go`):

```go
case errors.Is(err, io.ErrUnexpectedEOF):
	// A body that stops mid-archive, which includes anything shorter
	// than one 512-byte tar header block. ...
	jsonhttp.BadRequest(ow, "archive ends before it is complete")
```

Its comment already records that the stamped route answers 500 and that it was out of
scope there. So the two routes disagree about the same body on the same node, and this
change removes that disagreement rather than introducing a new judgement. The wording of
the fix is settled by what the fork route already does.

## Cost of leaving it

The same as #366: 500 means "try again, it might work" and 400 means "do not send this
again", so a client retries an upload that can never succeed. The node also logs an
operator-facing error for a caller mistake, putting a false fault in the operator's log.

## The change

Add the case to `dirUploadHandler`'s switch, beside the `tar.ErrHeader` case that already
answers 400:

```go
case errors.Is(err, io.ErrUnexpectedEOF):
	jsonhttp.BadRequest(w, "archive ends before it is complete")
```

and change the existing `non tar file` test to expect 400 with that message.

**Matching `io.ErrUnexpectedEOF` rather than giving `storeDir` its own sentinel.** The
alternative is for `storeDir` to wrap read failures in a sentinel of its own, which would
keep the decision in the place that knows what the error means. It is not taken here for
two reasons: the fork route already matches the standard library error and the two should
agree, and a sentinel would change what `storeDir` returns to both callers, which is a
wider change than this issue needs. If that shape is ever wanted it should be one change
covering both routes.

## What was checked in the same pass

**The multipart path.** `multipartReader.Next` feeds the same line, so the same question
applies to `multipart/form-data` bodies. What it returns for a truncated body must be
established before claiming the fix covers it, and the pull request must say which
readers were tested rather than assuming both behave alike.

**Whether any other error can reach `default` from a malformed request.** The remaining
wrapped errors (`store dir file`, `add to manifest`, `store manifest`) are failures of
the node's own storage or manifest machinery and are correctly 500.

## Verification

- The `non tar file` case returns **400**, where it returned 500. This is an existing
  test changed rather than a new one, so the diff shows the behaviour change directly.
- A body cut off part way through a valid archive also returns 400, which is the case the
  nine byte body does not cover: it is shorter than a single header block, and a
  mid-archive truncation is the more realistic failure.
- A valid archive still uploads, so the new case did not widen into the working path.
- A mutation: removing the new case must put the `non tar file` case back to 500.
- Mutations run over the whole package with `-run .`, never a name filter, and a mutation
  that fails to compile is reported as a build failure rather than as a caught mutation.

## Scope

`pkg/api/dirs.go` and `pkg/api/dirs_test.go`. No wire change, no configuration. The
status code for one malformed request changes, which an operator and a client can
observe, so `docs/DIFFERENCES.md` gains a row per rule 13, and the row for #366 is
amended, since it currently says this case still answers 500.
