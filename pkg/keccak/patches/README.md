# Patches applied to the XKCP source before building the blobs

`scripts/rebuild-keccak-syso.sh` applies every `*.patch` in this directory, in
lexical order, to a clean checkout of `ethersphere/XKCP` at the commit pinned in
`../REMOTE_COMMIT`. The build fails if a patch does not apply, rather than
silently producing an unpatched blob.

Keeping the changes here rather than editing the upstream fork means the
divergence is visible, reviewable, and re-appliable after a future XKCP bump —
and nothing is ever pushed to `ethersphere`.

| Patch | Issue | What it changes |
|---|---|---|
| `0001-clamp-negative-tail.patch` | [#91](https://github.com/crtahlin/wasp/issues/91) | Clamps the final-block `tail` index in both wrappers, which goes negative for any lane shorter than the longest — including the nil filler lanes the API documents as permitted — and writes before a stack buffer. |
| `0002-generated-stub-frame-size.patch` | [#89](https://github.com/crtahlin/wasp/issues/89) | Raises the Go stack frame the generator emits for the 4-lane stub from 8192 to 16384 bytes. The blob needs up to 11484, so the generated glue overflows the goroutine stack by about 3.3 KB — silently, because the digests stay correct. |

Each patch should be offered upstream separately. See the issue for where.
