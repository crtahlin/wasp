# Two malformed multipart uploads are the caller's mistake

Issue: [#424](https://github.com/crtahlin/wasp/issues/424).
Type: fix.

## Problem

Two malformed multipart directory uploads answer **500**. Both are the caller's
mistake, so both should be 400.

| Body | Answer today | Underlying error |
|---|---|---|
| `Content-Type: multipart/form-data` with no `boundary` parameter | 500 | `multipart: boundary is empty` |
| A part header line with no colon | 500 | `malformed MIME header: missing colon: ...` |

`TestDirsMultipartMalformed` in `pkg/api/dirs_test.go` already pins both as 500,
so the gap is recorded rather than invisible, and that test is what changes.

This is the class of [#366](https://github.com/crtahlin/wasp/issues/366) and
[#409](https://github.com/crtahlin/wasp/issues/409) reached through
`mime/multipart` rather than `archive/tar`. Those fixes do not cover it because
neither error is matchable by identity.

## Why identity matching is unavailable

Checked in the Go tree rather than assumed:

- `multipart: boundary is empty` is built with `fmt.Errorf` and never exported
  (`mime/multipart/multipart.go:389`), so there is no value to compare against.
- `malformed MIME header: missing colon: ...` is a `textproto.ProtocolError`, a
  string type whose text includes the offending line.

## The whole caller-side error set, established not guessed

The issue asked whether `mime/multipart` has other caller-side failures. What
`NextPart` can return:

| error | reachable here | status today |
|---|---|---|
| `io.EOF` | yes, the normal end of the parts | not an error |
| `multipart: boundary is empty` | yes | 500, fixed here |
| `textproto.ProtocolError` | yes, a malformed part header | 500, fixed here |
| `io.ErrUnexpectedEOF` | yes, a truncated body | already 400 via #409 |
| `ErrMessageTooLarge` | **yes** | 500, still |
| `multipart: expecting a new Part; got line ...` | **yes** | 500, still |

The writer errors in `mime/multipart/writer.go` are not reachable: this code
only reads.

> **Corrected at implementation.** The first draft of this table said
> `ErrMessageTooLarge` was unreachable because it "lives in `formdata.go` and
> is returned by `ReadForm`". Its *declaration* is there; its *return* is at
> `mime/multipart/multipart.go:177`, inside `populateHeaders`, which
> `NextPart` reaches. Reproduced with a part carrying more than 10000
> headers. A second error, `fmt.Errorf("multipart: expecting a new Part; got
> line %q")` at `:423`, was missed entirely, and was reproduced with a body
> ending `--BOUNDARY`, a tab, and a further character. **Corrected while
> specifying [#455](https://github.com/crtahlin/wasp/issues/455):** this note
> originally said "followed by a tab", and a tab alone does not reproduce it.
> `isBoundaryDelimiterLine` calls `skipLWSPChar`, so `--BOUNDARY` and a tab is
> a valid delimiter and the upload succeeds. It takes a non-whitespace
> character after the tab to reach `:423`.
>
> **So the set is not covered by this change**, and the claim that it was is
> withdrawn. The two cases below are fixed; the two above were fixed by
> [#455](https://github.com/crtahlin/wasp/issues/455), merged as `ca413858`,
> so the set is covered now and this note records how it was found rather
> than a gap that is still open. One of them, `ErrMessageTooLarge`, is an exported
> sentinel and so is cheaper to fix than anything here; the other is a bare
> `fmt.Errorf` and has exactly the matching problem this issue was about.

## The change

**Reject an empty boundary where the reader is built**, before any body is read.
`mime.ParseMediaType` already runs in the handler and its `params["boundary"]`
is what the reader is constructed from, so the check costs nothing and gives a
precise message:

```go
	case multiPartFormData:
		if params["boundary"] == "" {
			// 400, errNoBoundary
		}
		dReader = &multipartReader{r: multipart.NewReader(r.Body, params["boundary"])}
```

**Match `textproto.ProtocolError` with `errors.As`** in the handler switch,
which does not depend on the message text:

```go
		case errors.As(err, &protoErr):
			jsonhttp.BadRequest(w, "malformed multipart header")
```

Option 3 from the issue, giving `multipartReader.Next` its own sentinel, is
**rejected**. It is the largest change, it would want the same treatment for
`tarReader` to stay symmetrical, and it buys nothing the two changes above do
not already give: the boundary case never reaches `Next` at all, and
`errors.As` on an exported standard type is as stable as a fork-owned sentinel.

### Both callers, not one

`storeDir` has two callers and **both** build the multipart reader and carry
their own copy of the error switch:

- `pkg/api/dirs.go:74` and the switch at `:96`
- `pkg/api/localingest.go:92` and the switch at `:229`

[#366](https://github.com/crtahlin/wasp/issues/366) was merged having fixed only
one of them, and the second had to be found afterwards. Both are changed here,
and the tests cover both endpoints rather than assuming the second follows.

The duplication itself is not addressed. Two handlers that differ in postage
handling share an error switch by copy, and that is a real source of drift, but
collapsing them is a larger change than this issue justifies and would make the
diff hard to review against the merged spec.

## What it costs

A caller that sends either malformed body sees 400 where it saw 500. That is
the point. Nothing that sends a well-formed body changes, and a node-side
failure still answers 500.

`openapi/Swarm.yaml` documents 400 and 500 for both endpoints without tying
either to a condition, so the published contract stays true.

## Protocol impact

None. HTTP status codes on two local endpoints.

## Tests

In `pkg/api`, mutation checked: revert each half and confirm the matching test
fails.

- **No boundary parameter gives 400**, on `/bzz` and on `/wasp/ingest`. Fails
  today.
- **A part header with no colon gives 400**, on both. Fails today.
- **A well-formed multipart upload still succeeds**, so the boundary check
  cannot be satisfied by rejecting everything.
- **A truncated body still gives 400** through the existing `io.ErrUnexpectedEOF`
  case, so this change does not shadow #409's.
- **A node-side failure still gives 500**, the guard against widening the
  switch too far.

`TestDirsMultipartMalformed` currently asserts 500 for the two cases and is
updated to assert 400. That is the test changing meaning deliberately, which is
worth saying out loud: it was written to record a known gap.

## Measurement

None. Status codes with no timing component; rule 7 is for claims about how a
node performs.

## Rollout and rollback

No configuration, no migration, no on-disk change. Rollback is reverting the
merge commit.

## Upstream portability

**Verified.** `git show upstream/v2.8.2:pkg/api/dirs.go` carries the same
`multipartReader` and the same handler switch with no case for either error, so
both bodies answer 500 there too. Upstream has no `localingest.go`; that file is
this fork's own.

The issue carries `affects-upstream`, a marker for a later human decision and
nothing more.

## Files

- `pkg/api/dirs.go`, the reader construction and the switch.
- `pkg/api/localingest.go`, the same two places.
- `pkg/api/dirs_test.go`, where `TestDirsMultipartMalformed` changes.
- `docs/DIFFERENCES.md`: a node answers two requests differently from Bee.
- `docs/UPSTREAM.md`: the #424 row gains its branch and merge commit.

Generated with help of AI.
