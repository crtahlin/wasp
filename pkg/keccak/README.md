# pkg/keccak — SIMD Keccak-256 binaries

This package wraps the SIMD-accelerated, legacy Keccak-256 (Ethereum-compatible,
0x01 padding suffix — **not** FIPS 202 SHA3-256) primitives produced by
[XKCP](https://github.com/XKCP/XKCP). The actual permutation code is shipped as
two pre-linked, relocation-free `.syso` blobs that the Go linker pulls in
directly — no CGO, no toolchain dependency at `go build` time:

- `keccak_times4_linux_amd64.syso` — AVX2, 4-way parallel
- `keccak_times8_linux_amd64.syso` — AVX-512, 8-way parallel

The `.syso` files are checked in and verified against `CHECKSUM` by
`TestSysoChecksums` in `keccak_test.go`, so bee builds reproducibly without
the XKCP toolchain. Re-build the blobs only when XKCP itself changes (or when
porting to a new platform).

## Rebuilding the syso files

```bash
./scripts/rebuild-keccak-syso.sh            # linux/amd64, needs gcc + xsltproc
```

The script clones our XKCP fork at the commit pinned in `REMOTE_COMMIT`, builds
both blobs, checks they are relocation-free with no undefined symbols, and
prints their checksums. Run it on linux/amd64; the blobs are linux/amd64
artefacts.

Do the rebuild through the script rather than by hand. The manual procedure has
three traps, each of which produces a failure that points somewhere other than
its cause (issue #90):

**Fortification breaks the link.** Modern gcc enables `-D_FORTIFY_SOURCE=3` at
`-O2` and above, which rewrites `memcpy` into `__memcpy_chk`. The whole approach
here is to link each object so that no relocations remain, and an unresolvable
libc symbol makes that impossible:

```
ld: build_temp/combined_avx2.o: in function `KeccakP1600times4_AVX2_AddBytes':
(.text+0x63f): undefined reference to `__memcpy_chk'
```

The machine the original blobs were built on evidently did not fortify by
default, so the documented build was environment-dependent without saying so.
The script adds `-U_FORTIFY_SOURCE` to all four compile sites.

**The build is not idempotent.** `build_go_asm.sh` has no clean step. `make`
will not rebuild `bin/*/libXKCP.a` if it looks up to date, and the script then
extracts a *stale* object from it. The symptom is that a corrected compile flag
appears to do nothing: the freshly compiled wrapper is clean while the archived
object still carries `__memcpy_chk`, and the link fails exactly as before. The
script removes `bin/`, `build_temp/` and `go_keccak/` first.

**The output embeds its own build path.** `ld -r -b binary` names its symbols
after the absolute path of the input, so the shipped blob records the directory
it was built in:

```
_binary__home_acud_repos_XKCP_build_temp_blob_avx2_bin_start
```

Two builds of identical source on one machine therefore differ, purely because
the working directories differ. Nothing references these symbols, so the script
renames them to a canonical form. With that, the same source and toolchain
produce the same bytes wherever the build ran — which is what makes `CHECKSUM`
mean something. Verified: two builds in different directories, byte-identical
after normalisation.

### Do not copy the generated assembly

The build also emits Plan 9 assembly glue under `go_keccak/`. **Do not copy it
into this directory.** It declares an 8192-byte Go stack frame, while the blob
needs up to 11484 bytes (times4) and 12229 (times8) — see issue #89. More
importantly, the stubs here deliberately do not use a Go stack frame at all:
they run the blob on a pooled scratch stack, which is the fix for the memory
corruption in issue #92. Copying the generated stub reinstates the crash.

Only the `.syso` files are taken from the build. The `.s` stubs and the Go
wrappers in this directory are hand-maintained.

### Adopting a rebuild

The script prints the exact commands. In short: copy the two `.syso` files in
under their `linux_amd64` names, refresh `CHECKSUM`, and run the tests.

### Refreshing CHECKSUM

`CHECKSUM` pins the SHA-256 of each `.syso`, and `TestSysoChecksums` enforces
it. Note what that test does and does not prove: it shows the files have not
changed since they were pinned. It does not show they correspond to any
particular source — that is what the reproducible rebuild above is for.

```bash
cd pkg/keccak
sha256sum keccak_times4_linux_amd64.syso keccak_times8_linux_amd64.syso > CHECKSUM
go test -race ./pkg/keccak/... ./pkg/bmt/...
```

`TestSysoChecksums` parses `CHECKSUM`, recomputes the digests of the on-disk
`.syso` files, and fails the build if either drifts — that's the CI tripwire
that catches an accidental rebuild or a corrupted check-in.
