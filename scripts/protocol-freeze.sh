#!/usr/bin/env bash
#
# Fingerprint the wire-protocol surface this fork freezes.
#
# These are the values that decide whether a stock Bee node will complete a
# handshake with us, route to us, or accept our chunks. This fork exists to
# change Bee's behaviour while remaining a normal participant on the real Swarm
# network, so every one of them is frozen and any movement must be deliberate.
#
# This is a FINGERPRINT, not a diff. It extracts the frozen declarations,
# normalises them, sorts them, and hashes the result. A refactor that moves code
# around inside pkg/hive/hive.go passes silently; a one-character change to
# protocolVersion fails loudly. Line numbers are deliberately excluded so that
# unrelated edits to the same file do not fire the gate.
#
#   scripts/protocol-freeze.sh            print hash + surface (regenerate the lock)
#   scripts/protocol-freeze.sh --check    compare against .github/protocol-freeze.lock
#
# See docs/agent-playbooks/protocol-compatibility.md.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

LOCK=".github/protocol-freeze.lock"

# sha256sum on Linux, shasum on macOS.
sha256() { if command -v sha256sum >/dev/null 2>&1; then sha256sum | cut -d' ' -f1; else shasum -a 256 | cut -d' ' -f1; fi; }

# extract <file> <extended-regexp> -- prints "<file> <matching line>" per hit,
# or a MISSING marker so a deleted or renamed file fails the check rather than
# silently shrinking the surface.
extract() {
  if [ ! -f "$1" ]; then echo "MISSING-FILE $1"; return; fi
  local hits
  hits=$(grep -hE "$2" "$1" || true)
  if [ -z "$hits" ]; then echo "MISSING-MATCH $1 $2"; return; fi
  echo "$hits" | sed "s|^|$1 |"
}

surface() {
  # The stream-name prefix every protocol stream is built from.
  extract pkg/p2p/p2p.go '"/swarm/"'

  # Per-protocol name and version. The semver matcher requires an equal major
  # and local-minor >= peer-minor, so raising a minor silently stops older
  # peers from dialling us for that protocol.
  for f in pkg/hive/hive.go \
           pkg/pushsync/pushsync.go \
           pkg/pullsync/pullsync.go \
           pkg/retrieval/retrieval.go \
           pkg/status/status.go \
           pkg/pricing/pricing.go \
           pkg/pingpong/pingpong.go \
           pkg/settlement/pseudosettle/pseudosettle.go \
           pkg/settlement/swap/swapprotocol/swapprotocol.go; do
    extract "$f" '^[[:space:]]*protocol(Name|Version)[[:space:]]*='
  done

  # The handshake itself: a mismatch here means no connection at all.
  extract pkg/p2p/libp2p/internal/handshake/handshake.go '^[[:space:]]*ProtocolVersion[[:space:]]*='
  # Protobuf field numbers are permanent; reusing one misreads peer data.
  extract pkg/p2p/libp2p/internal/handshake/pb/handshake.proto '=[[:space:]]*[0-9]+[[:space:]]*;'

  # Chunk geometry determines chunk ADDRESSES. Changing any of it makes our
  # hashes disagree with every other client, including the JavaScript ones.
  extract pkg/swarm/swarm.go '^[[:space:]]*(StampIndexSize|StampTimestampSize|SpanSize|SectionSize|Branches|EncryptedBranches|BmtBranches|ChunkSize|HashSize|MaxPO|ExtendedPO|MaxBins|ChunkWithSpanSize|SocSignatureSize|SocMinChunkSize|SocMaxChunkSize)[[:space:]]'

  # Chain identity. NetworkID is checked in the handshake AND mixed into the
  # overlay address and the signed BzzAddress, so it is the switch that would
  # deliberately create a separate network.
  extract pkg/config/chain.go '^[[:space:]]*(ChainID|NetworkID)[[:space:]]*:'
  # Append-never-remove: dropping a hash rejects chequebooks from that factory
  # generation. Freezing the entries catches a removal, not just an edit.
  extract pkg/config/chain.go '^[[:space:]]*mustHash\('
  # ChainID/NetworkID above resolve through this module, so freezing the file
  # line alone would pin the reference and not the value. Pin the version too.
  extract go.mod 'go-storage-incentives-abi'
}

TMP="$(mktemp)"
trap 'rm -f "$TMP"' EXIT
# Normalise WITHIN each line (never across them): squeeze runs of whitespace,
# drop trailing line comments so that a comment edit cannot raise a false alarm,
# and trim. Then sort, so that reordering a declaration is not a change either.
surface \
  | sed -e 's|[[:space:]]\{1,\}| |g' -e 's| //.*$||' -e 's|^ ||' -e 's| $||' \
  | grep -v '^$' \
  | LC_ALL=C sort > "$TMP"

if [ "${1:-}" = "--check" ]; then
  if [ ! -f "$LOCK" ]; then echo "protocol-freeze: $LOCK is missing" >&2; exit 1; fi
  have=$(sha256 < "$TMP")
  want=$(head -1 "$LOCK")
  if [ "$have" = "$want" ]; then
    echo "protocol-freeze: wire surface unchanged ($have)"
    exit 0
  fi
  echo "protocol-freeze: WIRE SURFACE CHANGED" >&2
  echo "  expected $want" >&2
  echo "  actual   $have" >&2
  echo >&2
  echo "Difference (-committed +working tree):" >&2
  diff <(tail -n +2 "$LOCK") "$TMP" >&2 || true
  echo >&2
  echo "If this change is intentional: document it in the spec's Protocol impact" >&2
  echo "section, apply the 'protocol-change' label, and regenerate the lock with" >&2
  echo "  scripts/protocol-freeze.sh > $LOCK" >&2
  echo "See docs/agent-playbooks/protocol-compatibility.md." >&2
  exit 1
fi

sha256 < "$TMP"
cat "$TMP"
