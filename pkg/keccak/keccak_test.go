// Copyright 2026 The Swarm Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux && amd64 && !purego

package keccak

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"testing"

	"golang.org/x/crypto/sha3"
)

// referenceHash returns the legacy Keccak-256 digest using golang.org/x/crypto,
// which is the same function the BMT hasher uses in its goroutine path.
func referenceHash(data []byte) []byte {
	h := sha3.NewLegacyKeccak256()
	h.Write(data)
	return h.Sum(nil)
}

// testInputLengths covers a wide spread: sub-rate (<136), one-byte-before the
// rate (135 — where Keccak padding forces the 0x81 collision), exact rate,
// just over, and multi-block sizes out to 4096 bytes. The BMT caller only
// uses 64/96-byte inputs; the larger sizes exercise the lockstep multi-block
// absorption in the C wrapper.
var testInputLengths = []int{0, 1, 31, 32, 63, 64, 65, 95, 96, 97, 128, 134, 135, 136, 137, 200, 272, 1024, 4096}

func TestSum256x4(t *testing.T) {
	if !HasSIMD() {
		t.Skip("AVX2 not available on this CPU")
	}

	for _, length := range testInputLengths {
		t.Run(fmt.Sprintf("len_%d", length), func(t *testing.T) {
			var inputs [4][]byte
			var expected [4][]byte
			for i := range inputs {
				buf := make([]byte, length)
				_, _ = rand.Read(buf)
				inputs[i] = buf
				expected[i] = referenceHash(buf)
			}

			got := Sum256x4(inputs)
			for i := range got {
				if !bytes.Equal(got[i][:], expected[i]) {
					t.Errorf("lane %d mismatch:\n  got  %x\n  want %x", i, got[i], expected[i])
				}
			}
		})
	}
}

// TestSum256x4_PartialBatch mirrors the BMT caller's usage when the last batch
// is partial: some lanes carry real data, the rest are nil. The wrapper
// produces a (meaningless) digest for the nil lanes and correct digests for
// the real lanes — only the latter are asserted.
// These cover the shape that made the C wrapper write out of bounds: a partial
// batch whose ACTIVE lanes are at least one full 136-byte rate block long.
//
// The wrapper computes each lane's final-block length as len - max_full*136,
// where max_full is the maximum block count across ALL lanes. For a lane
// shorter than the longest — which every nil filler lane is, at length 0 —
// that goes negative, and the padding byte is written at a negative index,
// before a 136-byte stack array. See issue #91.
//
// TestSum256x4_PartialBatch below cannot reach it: its lengths are 64, 96 and
// 128, all under the rate, so max_full is always 0 and the value never goes
// negative. Nothing covered this shape before.
//
// What these tests can and cannot assert is worth being precise about. The
// out-of-bounds write itself is not observable from Go — the stub runs the blob
// on a 64 KiB pooled scratch stack, so a write 136 bytes below a buffer deep
// inside that stack lands harmlessly within it. Catching the write requires
// AddressSanitizer at the C level; `scripts/rebuild-keccak-syso.sh` documents
// how, and the fix is carried as a patch in pkg/keccak/patches.
//
// Nor can the filler lanes' digests be asserted. A lane that absorbs nothing is
// still permuted by the shared PermuteAll in the full-block loop, so its output
// is not the empty-message digest and is not any other well-defined value
// either. (The wrapper's own comment claims skipping AddBytes leaves a lane's
// state unchanged; it does not — the permutation runs regardless.) Filler
// output is undefined whenever an active lane reaches the rate, which is
// stronger than the API's "must be ignored", and is why Sum256x4 documents it
// that way.
//
// What is asserted is the part that matters: every real lane hashes correctly
// in a shape that previously invoked undefined behaviour on every call.
func TestSum256x4_PartialBatchLongLanes(t *testing.T) {
	if !HasSIMD() {
		t.Skip("AVX2 not available on this CPU")
	}

	for _, realLen := range []int{136, 137, 200, 272, 4096} {
		for realLanes := 1; realLanes < 4; realLanes++ {
			t.Run(fmt.Sprintf("real_%d_of_4_len_%d", realLanes, realLen), func(t *testing.T) {
				var inputs [4][]byte
				var expected [4][]byte
				for i := range realLanes {
					buf := make([]byte, realLen)
					_, _ = rand.Read(buf)
					inputs[i] = buf
					expected[i] = referenceHash(buf)
				}

				got := Sum256x4(inputs)
				for i := range realLanes {
					if !bytes.Equal(got[i][:], expected[i]) {
						t.Errorf("real lane %d mismatch:\n  got  %x\n  want %x", i, got[i], expected[i])
					}
				}
			})
		}
	}
}

func TestSum256x8_PartialBatchLongLanes(t *testing.T) {
	if !HasAVX512() {
		t.Skip("AVX-512 not available on this CPU")
	}

	for _, realLen := range []int{136, 200, 4096} {
		for realLanes := 1; realLanes < 8; realLanes++ {
			t.Run(fmt.Sprintf("real_%d_of_8_len_%d", realLanes, realLen), func(t *testing.T) {
				var inputs [8][]byte
				var expected [8][]byte
				for i := range realLanes {
					buf := make([]byte, realLen)
					_, _ = rand.Read(buf)
					inputs[i] = buf
					expected[i] = referenceHash(buf)
				}

				got := Sum256x8(inputs)
				for i := range realLanes {
					if !bytes.Equal(got[i][:], expected[i]) {
						t.Errorf("real lane %d mismatch:\n  got  %x\n  want %x", i, got[i], expected[i])
					}
				}
			})
		}
	}
}

func TestSum256x4_PartialBatch(t *testing.T) {
	if !HasSIMD() {
		t.Skip("AVX2 not available on this CPU")
	}

	for _, realLen := range []int{64, 96, 128} {
		for realLanes := 1; realLanes < 4; realLanes++ {
			t.Run(fmt.Sprintf("real_%d_of_4_len_%d", realLanes, realLen), func(t *testing.T) {
				var inputs [4][]byte
				var expected [4][]byte
				for i := range realLanes {
					buf := make([]byte, realLen)
					_, _ = rand.Read(buf)
					inputs[i] = buf
					expected[i] = referenceHash(buf)
				}
				// inputs[realLanes..3] remain nil

				got := Sum256x4(inputs)
				for i := range realLanes {
					if !bytes.Equal(got[i][:], expected[i]) {
						t.Errorf("real lane %d mismatch:\n  got  %x\n  want %x", i, got[i], expected[i])
					}
				}
			})
		}
	}
}

func TestSum256x8(t *testing.T) {
	if !HasAVX512() {
		t.Skip("AVX-512 not available on this CPU")
	}

	for _, length := range testInputLengths {
		t.Run(fmt.Sprintf("len_%d", length), func(t *testing.T) {
			var inputs [8][]byte
			var expected [8][]byte
			for i := range inputs {
				buf := make([]byte, length)
				_, _ = rand.Read(buf)
				inputs[i] = buf
				expected[i] = referenceHash(buf)
			}

			got := Sum256x8(inputs)
			for i := range got {
				if !bytes.Equal(got[i][:], expected[i]) {
					t.Errorf("lane %d mismatch:\n  got  %x\n  want %x", i, got[i], expected[i])
				}
			}
		})
	}
}

func TestSum256x8_PartialBatch(t *testing.T) {
	if !HasAVX512() {
		t.Skip("AVX-512 not available on this CPU")
	}

	for _, realLen := range []int{64, 96, 128} {
		for realLanes := 1; realLanes < 8; realLanes++ {
			t.Run(fmt.Sprintf("real_%d_of_8_len_%d", realLanes, realLen), func(t *testing.T) {
				var inputs [8][]byte
				var expected [8][]byte
				for i := range realLanes {
					buf := make([]byte, realLen)
					_, _ = rand.Read(buf)
					inputs[i] = buf
					expected[i] = referenceHash(buf)
				}

				got := Sum256x8(inputs)
				for i := range realLanes {
					if !bytes.Equal(got[i][:], expected[i]) {
						t.Errorf("real lane %d mismatch:\n  got  %x\n  want %x", i, got[i], expected[i])
					}
				}
			})
		}
	}
}
