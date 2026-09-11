// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package storer

import (
	"context"
	"math/big"

	"github.com/ethersphere/bee/v2/pkg/swarm"
)

// The windowed proof (#273, validated in #271) restricts the sample to an
// anchor-derived sub-window of the neighbourhood and takes the same k smallest
// transformed addresses inside it. Because the transformed address is a
// content-bound hash the node cannot place, and the window is unpredictable
// before the anchor is revealed, the proof stays sound while reading only about
// N/f chunks instead of N. See docs/experiments/reserve-proof-mode/spec.md.
//
// This is the sampler compute only. It is not yet wired into the redistribution
// agent (that is phase B), and it is experimental: the live contract verifies
// the classic whole-reserve proof, so a windowed sample wins nothing until a
// windowed-aware contract exists.

// Reserve-proof-mode values (#273). classic is the whole-reserve scan the
// network and the live contract expect; windowed is the experimental sublinear
// variant. classic is the default.
const (
	ReserveProofModeClassic  = "classic"
	ReserveProofModeWindowed = "windowed"
)

// ValidReserveProofMode reports whether s is a recognised reserve-proof-mode.
func ValidReserveProofMode(s string) bool {
	return s == ReserveProofModeClassic || s == ReserveProofModeWindowed
}

const (
	// minWindowCount is the smallest number of chunks the window aims to hold.
	// It is a healthy multiple of SampleSize so the order statistic can still
	// separate a full reserve from a partial one; below it the floor from #271
	// is crossed and soundness degrades, so the window is not allowed to shrink
	// past it.
	minWindowCount = 32 * SampleSize

	// maxWindowDepthIncrement caps how many bits deeper than the committed depth
	// the window may reach, keeping the window depth within the proximity range
	// and bounding the largest speedup at 2^maxWindowDepthIncrement.
	maxWindowDepthIncrement = 12

	// windowAnchorSalt domain-separates the window-position hash from any other
	// use of the anchor.
	windowAnchorSalt = "wasp/reserve-proof-window"
)

// WindowedSample generates the experimental windowed sample (#273). It derives
// a sub-window of the node's neighbourhood from the anchor, sized so the window
// holds at least minWindowCount chunks where the reserve allows, and returns the
// k smallest transformed addresses among the chunks inside it. On a small
// reserve the window degenerates to the whole neighbourhood, which is exactly
// the classic sample, so the result is never less sound than classic.
func (db *DB) WindowedSample(
	ctx context.Context,
	anchor []byte,
	committedDepth uint8,
	consensusTime uint64,
	minBatchBalance *big.Int,
) (Sample, error) {
	inc := windowDepthIncrement(db.ReserveSizeWithinRadius())
	if inc == 0 {
		// The reserve is too small to carve a sound window out of, so the window
		// is the whole neighbourhood: identical to the classic sample.
		return db.reserveSample(ctx, anchor, committedDepth, consensusTime, minBatchBalance, nil)
	}

	windowDepth := committedDepth + inc
	if windowDepth > swarm.MaxPO {
		windowDepth = swarm.MaxPO
	}

	windowAnchor := deriveWindowAnchor(anchor, committedDepth)
	inWindow := func(addr []byte) bool {
		return swarm.Proximity(addr, windowAnchor) >= windowDepth
	}

	return db.reserveSample(ctx, anchor, committedDepth, consensusTime, minBatchBalance, inWindow)
}

// windowDepthIncrement chooses how many bits deeper than the committed depth the
// window reaches. It picks the largest increment b for which the window still
// holds at least minWindowCount chunks (the reserve is split roughly in half by
// each extra bit), capped at maxWindowDepthIncrement. It returns 0 when the
// reserve is too small to split, in which case the window is the whole
// neighbourhood.
func windowDepthIncrement(reserveSizeWithinRadius uint64) uint8 {
	if reserveSizeWithinRadius <= minWindowCount {
		return 0
	}
	var b uint8
	for b < maxWindowDepthIncrement && (reserveSizeWithinRadius>>(b+1)) >= minWindowCount {
		b++
	}
	return b
}

// deriveWindowAnchor builds the window's centre address. It keeps the anchor's
// top committedDepth bits, so the window sits inside the node's neighbourhood,
// and replaces every bit below that with bits derived from the anchor. The
// window position is therefore unpredictable before the anchor is revealed but
// deterministic once it is, exactly like the transform salt.
func deriveWindowAnchor(anchor []byte, committedDepth uint8) []byte {
	h := swarm.NewHasher()
	_, _ = h.Write(anchor)
	_, _ = h.Write([]byte(windowAnchorSalt))
	digest := h.Sum(nil)

	windowAnchor := make([]byte, len(anchor))
	copy(windowAnchor, anchor)

	// Replace the bits at and below the committed depth with digest bits,
	// preserving the leading committedDepth bits that place the window in the
	// neighbourhood.
	for bit := int(committedDepth); bit < len(windowAnchor)*8; bit++ {
		byteIdx := bit / 8
		mask := byte(1) << uint(7-(bit%8))
		src := digest[byteIdx%len(digest)]
		if src&mask != 0 {
			windowAnchor[byteIdx] |= mask
		} else {
			windowAnchor[byteIdx] &^= mask
		}
	}
	return windowAnchor
}
