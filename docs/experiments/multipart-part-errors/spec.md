# The last two malformed multipart bodies are the caller's mistake

Issue: [#455](https://github.com/crtahlin/wasp/issues/455).
Type: fix.

## Problem

Two malformed multipart bodies still answer **500**. Both are the caller's
mistake, so both should be 400. They are what
[#424](https://github.com/crtahlin/wasp/issues/424) did not cover, and its
spec's claim that the caller-side error set was covered is already withdrawn
inside that spec.

| Body | Answer today | Underlying error | Where it is built |
|---|---|---|---|
| A part carrying more than 10000 header lines | 500 | `multipart: message too large` | `mime/multipart/multipart.go:177` |
| Garbage where a new part was expected, a body ending `--BOUNDARY`, a tab, then a further character | 500 | `multipart: expecting a new Part; got line "--BOUNDARY\tx\r\n"` | `mime/multipart/multipart.go:423` |

Both were reproduced against `POST /wasp/ingest`. `POST /bzz` builds the same
reader through the same `multipartReader` and carries its own copy of the same
error switch, so both apply to it too.

### What each limit actually is, read rather than assumed

`Reader.NextPart` calls `nextPart(false, maxMIMEHeaderSize, maxMIMEHeaders())`
(`multipart.go:372`). `maxMIMEHeaderSize` is `10 << 20`, ten megabytes of header
bytes (`:348`). `maxMIMEHeaders()` reads a `GODEBUG` setting and returns 10000
by default (`:355`). Exceeding either makes `readMIMEHeader` return the text
`message too large`, which `populateHeaders` replaces with the exported sentinel
`ErrMessageTooLarge` (`:176-178`).

So the first case is not a size limit on the upload. It is a limit on the
headers of a single part, and a well-formed upload of any size never reaches it.

## The two cases are not equally hard, and that is the whole design question

`multipart.ErrMessageTooLarge` is an **exported sentinel**
(`mime/multipart/formdata.go:20`), and `populateHeaders` assigns that exact
value rather than wrapping it. `errors.Is` reaches it. One case in each switch
settles it, and this is the cheapest fix in this whole class of issues.

`multipart: expecting a new Part; got line %q` at `:423` is a bare
`fmt.Errorf` with the caller's own bytes quoted into its text. It has a sibling
at `:440`, `multipart: unexpected line in Next()`, built the same way. Neither
`errors.Is` nor `errors.As` can reach either. This is exactly the matching
problem #424 was about.

### Revisiting the sentinel that #424 rejected

#424's spec rejected giving `multipartReader.Next` its own sentinel, on the
grounds that the two cases then in hand did not need it. That reasoning was
sound for those two cases and does not carry to this one, so the question is
opened again here rather than inherited.

Three ways to reach `:423`:

**Match the message text.** Rejected. It is matching on the text of a standard
library error that carries no compatibility promise, and avoiding exactly that
is why #424 used `errors.As` on `textproto.ProtocolError` instead of a string
compare. It would also need a second match for the sibling at `:440`, and a
third for whatever the next Go release adds.

**Leave both at 500.** Rejected. A 500 tells the caller the node failed, which
is false, and it is the reason this issue exists.

**Give `multipartReader.Next` a sentinel.** Accepted, and the argument is
attribution rather than convenience.

`multipartReader.Next` calls exactly one thing, `m.r.NextPart()`
(`pkg/api/dirs.go:338`). Every error that call can return comes from parsing
bytes the caller sent or from reading the caller's request body. None of them
can be a node-side failure. That is a property of the call site, not of any
individual error, so a sentinel applied there is **provably** about the caller
in a way that matching individual errors is not.

That has a consequence worth stating plainly, because it runs the opposite way
from the usual worry about widening a match. The two existing multipart cases
in the switch, `errors.As(err, &protoErr)` and
`errors.Is(err, io.ErrUnexpectedEOF)`, match anywhere in the error chain,
including a chain that never passed through the reader. `dirs.go:129-136`
already records that concern about the `io.ErrUnexpectedEOF` case and explains
why it was left wide. The new sentinel is not narrower in the sense of
matching fewer errors, and an earlier draft of this spec said it was, which is
false: it matches the two cases above and also `ErrMessageTooLarge`, which
neither of them does. What is true is **attribution**. It is attached at the
reader and nowhere else, so nothing that did not come from the caller's body
can carry it, while both existing cases can be satisfied by a chain that never
passed through the reader.

This spec does not narrow the two existing cases onto the sentinel. That would
change what #409 and #424 do for the tar path as well as the multipart one, for
no defect anyone has reported, and it belongs on its own issue if it is wanted.
It is recorded here so the next person does not have to rediscover that the
option exists.

**`tarReader` does not get the same treatment**, and symmetry is not a reason to
give it one. Its caller-side errors are `tar.ErrHeader` and
`io.ErrUnexpectedEOF`, both already reachable by identity and both already
answered 400. A sentinel there would add a layer and change nothing.

## The change

Two edits, in both callers.

**A sentinel at the reader.** `multipartReader.Next` wraps every error from
`NextPart` except `io.EOF`, which is the normal end of the parts and not an
error at all:

```go
func (m *multipartReader) Next() (*FileInfo, error) {
	part, err := m.r.NextPart()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, err
		}
		// wasp #455
		return nil, fmt.Errorf("%w: %w", errMalformedMultipart, err)
	}
```

`fmt.Errorf` with two `%w` verbs keeps both chains reachable, so the wrap does
not hide anything the switch already matches. The `io.EOF` guard is load
bearing: wrapping it would make every well-formed upload fail, and a test below
exists to catch exactly that.

**Two cases in each switch**, placed after the existing multipart cases so the
precise messages keep winning over the general one:

```go
case errors.Is(err, multipart.ErrMessageTooLarge):
	jsonhttp.BadRequest(w, "multipart part headers are too large")
case errors.Is(err, errMalformedMultipart):
	jsonhttp.BadRequest(w, "malformed multipart body")
```

Order inside the switch is behaviour here, not layout, which is why the tests
below assert the response **message** and not only the status code.

**The snippet above is `dirs.go`'s shape and must not be copied literally into
`localingest.go`.** Every case there answers through `ow`, the cleanup writer
built at `:176`, so that a refused collection is released rather than left on
disk until the next restart, and every case carries its own `logger.Debug` and
`return`. Answering through the plain writer there would reintroduce exactly
the leak #424 added a test for. `dirs.go`'s switch ends in a `default:`;
`localingest.go`'s has none and falls through to the 500 below it, so "after
the existing multipart cases" means a slightly different place in each.

### Checked against the standard library, not reasoned about

Both bodies were driven through `multipart.NewReader` directly, before and
after the wrap, rather than the behaviour being argued from the source:

| body | raw error | `Is` `ErrMessageTooLarge` | `As` `ProtocolError` | after the wrap |
|---|---|---|---|---|
| 10001 part headers | `multipart: message too large` | **true** | false | sentinel **and** `ErrMessageTooLarge` both true |
| ends `--BOUNDARY`, tab, `x` | `multipart: expecting a new Part; got line ...` | false | false | sentinel true, nothing else |
| part header with no colon (#424) | `malformed MIME header: missing colon: ...` | false | **true** | sentinel **and** `ProtocolError` both true |
| well formed | `EOF` | false | false | **not wrapped**, `io.EOF` passes through |

The third row is the one that matters for the switch: after the wrap a #424
body matches **both** the `protoErr` case and the new sentinel case, so which
message the caller sees is decided by their order and by nothing else. That is
the mutation the order test exists to catch.

**The fourth row is where an earlier draft of this spec was wrong.** It said
the `io.EOF` guard is load bearing, and that without it every well-formed
upload would answer 400. That is false, and it contradicts the very property
the rest of this section relies on.

`storeDir` ends its loop on `errors.Is(err, io.EOF)`
(`pkg/api/dirs.go:220-226`), not on `==`, and two `%w` verbs keep `io.EOF` in
the chain. So a wrapped `io.EOF` still ends the loop and a well-formed upload
still answers 201. There is one consumer of `Next` in the repository, that
loop, so there is no other path where it could matter.

The guard is kept anyway, on honest grounds rather than the false one: it keeps
`Next` from putting a fork sentinel in the chain of something that is not an
error, and it keeps the contract clean for a future consumer that compares with
`==`. **No handler-level test can detect its removal**, and this spec says so
rather than listing a mutation that would not fail.

One more thing about the guard is worth stating, because the word "normal" hides
it: `errors.Is(err, io.EOF)` is wider than the normal end of the parts.
`multipart.go:403-405` wraps a read error as `multipart: NextPart: %w`, so an
empty body, a body with no boundary at all, and a body cut off at a boundary
all come back matching it. They are malformed, and they already answer 400
through `errEmptyDir`; the guard does not change that, and this spec does not
claim it does.

### Both callers, again

`pkg/api/dirs.go` and `pkg/api/localingest.go` each build the reader and each
carry their own copy of the switch. [#366](https://github.com/crtahlin/wasp/issues/366)
was merged having fixed one of them and the other had to be found afterwards;
#424 changed both. Both change here, and both endpoints are tested rather than
one being assumed to follow the other.

The duplication itself stays unaddressed, for the reason #424's spec gives:
collapsing two handlers that differ in postage handling is a larger change than
this issue justifies.

## What it costs

A caller that sends either body sees 400 where it saw 500. That is the point.

The cost that needs saying is the one attached to the sentinel rather than to
the two cases: **any future error from `NextPart` will be answered 400 without
anyone deciding that individually.** That is deliberate, and it is safe only
because of the property argued above, that `Next` calls nothing but `NextPart`.
If `Next` ever grows a second call, to the node's own storage for example, the
sentinel stops being a statement about the caller and must be narrowed. The
comment on the wrap says so in the code, where someone adding that call will
read it.

Nothing that sends a well-formed body changes, and a node-side failure still
answers 500.

`openapi/Swarm.yaml` documents 400 and 500 for both endpoints without tying
either to a condition, so the published contract stays true.

## Protocol impact

None. HTTP status codes on two local endpoints.

## Tests

In `pkg/api`, on both endpoints, mutation checked.

- **A part with more than 10000 headers gives 400**, with the headers message.
  Answers 500 today.
- **A body ending `--BOUNDARY`, a tab and a further character gives 400**, with the malformed-body
  message. Answers 500 today.
- **The two #424 bodies keep their own messages**, not the new general one.
  This is the test that pins switch order, and it fails if the sentinel case is
  moved above `errors.As(err, &protoErr)`.
- **A well-formed multipart upload still succeeds**, which is what catches a
  wrap that swallows `io.EOF`.
- **A node-side failure still answers 500**, driven by a chunk store that
  refuses every write, so the failure is the node's and the request is
  well formed. This is the guard against the sentinel being attached too
  widely, and it **is** mutation checked: moving the sentinel onto
  `storeDir`'s `store dir file` wrap makes it fail.

  A useful distinction came out of running that mutation. Attaching the
  sentinel to the `read dir stream` wrap instead does **not** break this test,
  because that wrap carries reader errors rather than the node's. It is still
  the wrong place, and not merely a near-equivalent one: `reader` there is the
  `dirReader` interface, so that wrap covers `tarReader.Next` as well, and a
  sentinel named for multipart would label tar failures as multipart ones.
  Nothing catches that today, and no reachable `tarReader.Next` error escapes
  the existing `tar.ErrHeader` and `io.ErrUnexpectedEOF` cases, so it is a
  hazard reasoned about rather than reproduced. `store dir file` is the wrap
  that carries the pipeline, which is where the node's own failures come from,
  and that is the widening the test guards.
- **A refused body leaves nothing reserved**, carried from #424: the assertion
  is only meaningful for a body that stores a file before it fails, because a
  body that stops on its first part never reserves anything and the usage reads
  zero either way.

Mutations to run, each paired with the test it must break:

| mutation | test it must break |
|---|---|
| drop the `ErrMessageTooLarge` case | the headers test, **on its message**: the body still answers 400 through the sentinel, so a status-only assertion would not notice |
| drop the sentinel wrap | the malformed-body test. It does **not** break the headers test, which keeps its own case |
| move the sentinel case above `protoErr` | the #424 message test |
| attach the sentinel at `storeDir`'s wrap instead of at the reader | the node-side 500 test |

Dropping the `io.EOF` guard is deliberately **not** in this list, for the
reason given above: it is not detectable at the handler. A mutation that fails
to compile proves nothing and is redone until it compiles.

## Measurement

None. Status codes with no timing component. Rule 7 is for claims about how a
node performs.

## Rollout and rollback

No configuration, no migration, no on-disk change. Rollback is reverting the
merge commit.

## Upstream portability

**Verified.** `git show upstream/v2.8.2:pkg/api/dirs.go` carries the same
`multipartReader` and the same handler switch with no case for either error, so
both bodies answer 500 there too. Upstream has no `localingest.go`; that file is
this fork's own.

The issue carries `affects-upstream`, which per rule 11 is a marker for a later
human decision and nothing more.

## Files

- `pkg/api/dirs.go`, `multipartReader.Next`, the new sentinel and the switch.
- `pkg/api/localingest.go`, the same switch.
- `pkg/api/dirs_test.go` and `pkg/api/localingest_multipart_test.go`.
- `docs/experiments/multipart-status/spec.md`: the withdrawal note gains the
  outcome, so the correction and its repair sit together.
- `docs/DIFFERENCES.md`: the existing multipart row covers two more bodies.
- `docs/UPSTREAM.md`: the #455 row gains its branch and merge commit.

Generated with help of AI.
