#!/usr/bin/env bash
#
# Rebuild pkg/keccak's .syso blobs from source, reproducibly.
#
# Why this exists: the documented procedure (pkg/keccak/README.md) did not work
# on a current toolchain, and the failure mode was misleading enough to cost an
# afternoon. See issue #90. The blobs are 180 KB of opaque machine code that
# runs in every process with SIMD hashing enabled; being unable to regenerate
# them is not an acceptable position for this subsystem, which has already
# produced one node-killing bug (#92).
#
# Run on linux/amd64 with a C toolchain and xsltproc. Nothing is pushed
# anywhere and the upstream XKCP fork is only ever read.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
KECCAK="$HERE/pkg/keccak"
COMMIT="$(cat "$KECCAK/REMOTE_COMMIT")"
WORK="${1:-$(mktemp -d)}"
REPO="$WORK/XKCP"

echo "==> rebuilding pkg/keccak blobs"
echo "    pinned XKCP commit: $COMMIT"
echo "    work directory:     $WORK"

if [ "$(uname -s)" != "Linux" ] || [ "$(uname -m)" != "x86_64" ]; then
  echo "ERROR: the blobs are linux/amd64 artefacts; build them on linux/amd64." >&2
  exit 1
fi
for t in gcc ld ar xsltproc git make; do
  command -v "$t" >/dev/null || { echo "ERROR: $t not found" >&2; exit 1; }
done

# --- 1. source, at the pinned commit, with submodules -----------------------
# XKCP's build system is generated from XSLT held in the XKCBuild submodule.
# Without it, make fails with "No rule to make target Main.makefile", which
# reads like a broken repository rather than a missing checkout step.
if [ ! -d "$REPO/.git" ]; then
  git clone --quiet https://github.com/ethersphere/XKCP.git "$REPO"
fi
git -C "$REPO" fetch --quiet origin
git -C "$REPO" checkout --quiet "$COMMIT"
git -C "$REPO" submodule update --init --recursive --quiet
echo "    source ready at $(git -C "$REPO" rev-parse --short HEAD)"

# --- 1b. our patches against the upstream source ----------------------------
# Applied to a clean checkout so the divergence from ethersphere/XKCP is
# visible and reviewable, rather than living as an edit in somebody's clone.
# A patch that does not apply is a hard failure: silently building an
# unpatched blob is exactly the kind of thing that produces a bug nobody can
# reproduce. See pkg/keccak/patches/README.md.
PATCHES="$KECCAK/patches"
if [ -d "$PATCHES" ]; then
  applied=0
  for patch in "$PATCHES"/*.patch; do
    [ -e "$patch" ] || continue
    if git -C "$REPO" apply --check "$patch" 2>/dev/null; then
      git -C "$REPO" apply "$patch"
      echo "    applied $(basename "$patch")"
      applied=$((applied+1))
    elif git -C "$REPO" apply --reverse --check "$patch" 2>/dev/null; then
      echo "    already applied $(basename "$patch")"
      applied=$((applied+1))
    else
      echo "ERROR: $(basename "$patch") does not apply to $COMMIT." >&2
      echo "       Rebase it against the pinned commit before rebuilding." >&2
      exit 1
    fi
  done
  echo "    $applied patch(es) in effect"
fi

# --- 2. the fortification fix ----------------------------------------------
# Modern gcc turns on -D_FORTIFY_SOURCE=3 at -O2 and above, which rewrites
# memcpy into __memcpy_chk. The entire point of this build is to link each
# object so that NO relocations remain, and an unresolvable libc symbol makes
# that impossible:
#     undefined reference to `__memcpy_chk'
# The machine the shipped blobs were built on evidently did not fortify by
# default, so the build was environment-dependent without saying so.
BEFORE=$(grep -c -- '-U_FORTIFY_SOURCE' "$REPO/build_go_asm.sh" || true)
if [ "$BEFORE" -eq 0 ]; then
  sed -i 's/-fno-stack-protector -fno-asynchronous-unwind-tables/-fno-stack-protector -fno-asynchronous-unwind-tables -U_FORTIFY_SOURCE/g' \
    "$REPO/build_go_asm.sh"
fi
SITES=$(grep -c -- '-U_FORTIFY_SOURCE' "$REPO/build_go_asm.sh")
[ "$SITES" -eq 4 ] || { echo "ERROR: patched $SITES compile sites, expected 4" >&2; exit 1; }
echo "    -U_FORTIFY_SOURCE applied to $SITES compile sites"

# --- 3. clean, because the build is not idempotent --------------------------
# build_go_asm.sh has no clean step. make will not rebuild bin/*/libXKCP.a if
# it looks up to date, and the script then `ar x`-tracts a STALE object from
# it. The symptom is that a corrected compile flag appears to have no effect:
# the freshly compiled wrapper is clean while the archived object still
# carries __memcpy_chk, and the link fails exactly as before the fix.
rm -rf "$REPO/bin" "$REPO/build_temp" "$REPO/go_keccak"
echo "    cleaned bin/, build_temp/, go_keccak/"

# --- 4. build ---------------------------------------------------------------
( cd "$REPO" && ./build_go_asm.sh ) > "$WORK/build.log" 2>&1 || {
  echo "ERROR: build failed. Last 20 lines:" >&2
  tail -20 "$WORK/build.log" >&2
  exit 1
}

OUT4="$REPO/go_keccak/keccak_times4_amd64.syso"
OUT8="$REPO/go_keccak/keccak_times8_amd64.syso"
[ -f "$OUT4" ] && [ -f "$OUT8" ] || { echo "ERROR: blobs not produced" >&2; exit 1; }

# No relocations may remain: that is what lets Go link them without cgo.
for o in "$OUT4" "$OUT8"; do
  if [ "$(readelf -r "$o" 2>/dev/null | grep -c 'Relocation section')" -ne 0 ]; then
    echo "ERROR: $(basename "$o") still contains relocations" >&2; exit 1
  fi
  if nm -u "$o" 2>/dev/null | grep -q .; then
    echo "ERROR: $(basename "$o") has undefined symbols:" >&2
    nm -u "$o" >&2; exit 1
  fi
done
echo "    blobs are relocation-free with no undefined symbols"

# --- 4b. make the artefact path-independent --------------------------------
# `ld -r -b binary` names its symbols after the *absolute path* of the input
# file, so the blob records the directory it happened to be built in:
#     _binary__home_acud_repos_XKCP_build_temp_blob_avx2_bin_start
# Two builds from identical source on the same machine therefore differ, purely
# because the working directories differ, and the shipped blob can never be
# shown to correspond to any particular source — which is the substance of #90.
# Nothing references these symbols (Go calls go_keccak256x4/x8), so rename them
# to a canonical form. After this, the same source and toolchain produce the
# same bytes wherever the build ran.
# Pass the variant explicitly. Deriving it from the filename is a trap: get it
# wrong and both blobs declare the same symbols, which fails at link time with
# a duplicate-symbol error far from the cause.
normalise() {
  o="$1"; variant="$2"
  args=()
  while read -r sym; do
    [ -n "$sym" ] || continue
    suffix="${sym##*_bin_}"
    args+=(--redefine-sym "$sym=_binary_blob_${variant}_bin_${suffix}")
  done < <(nm "$o" 2>/dev/null | awk '{print $NF}' | grep '^_binary_' | sort -u)
  [ ${#args[@]} -eq 0 ] || objcopy "${args[@]}" "$o"
}
normalise "$OUT4" avx2
normalise "$OUT8" avx512
for o in "$OUT4" "$OUT8"; do
  if nm "$o" 2>/dev/null | awk '{print $NF}' | grep '^_binary_' | grep -qv '^_binary_blob_'; then
    echo "ERROR: a build-path symbol survived normalisation in $(basename "$o")" >&2; exit 1
  fi
done
# The two blobs must not declare the same symbols, or the Go link fails.
if [ "$(nm "$OUT4" | awk '{print $NF}' | grep -c '_binary_blob_avx2_')" -eq 0 ] ||
   [ "$(nm "$OUT8" | awk '{print $NF}' | grep -c '_binary_blob_avx512_')" -eq 0 ]; then
  echo "ERROR: blob symbols were normalised to the wrong variant" >&2; exit 1
fi
echo "    embedded build-path symbols normalised"

# --- 5. warn about the generated assembly ----------------------------------
# The generator emits a Go assembly stub declaring a frame far smaller than the
# code it calls actually needs (issue #89). Do NOT copy it over the stubs in
# pkg/keccak: those deliberately use a pooled scratch stack instead of a Go
# frame, which is the fix for #92. Copying the generated stub reintroduces the
# original crash.
GEN_FRAME=$(grep -oE 'TEXT ·keccak256x4\(SB\), \$[0-9]+' "$REPO/go_keccak/keccak_times4_amd64.s" 2>/dev/null | grep -oE '[0-9]+$' || echo "?")
echo ""
echo "    NOTE: the generator emitted a \$$GEN_FRAME-byte frame in its .s glue."
echo "          The blob's measured static ceiling is 11484 bytes (times4) and"
echo "          12229 (times8). Do NOT copy the generated .s files into"
echo "          pkg/keccak — see issues #89 and #92. Only the .syso files are"
echo "          taken from this build."

echo ""
echo "==> built successfully"
printf '    %s\n' "$(sha256sum "$OUT4")" "$(sha256sum "$OUT8")"
echo ""
echo "To adopt them:"
echo "  cp $OUT4 $KECCAK/keccak_times4_linux_amd64.syso"
echo "  cp $OUT8 $KECCAK/keccak_times8_linux_amd64.syso"
echo "  (cd $KECCAK && sha256sum keccak_times4_linux_amd64.syso keccak_times8_linux_amd64.syso > CHECKSUM)"
echo "  go test -race ./pkg/keccak/... ./pkg/bmt/..."
