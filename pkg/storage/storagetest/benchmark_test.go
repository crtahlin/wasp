// Copyright 2022 The Swarm Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package storagetest

import (
	"bytes"
	"fmt"
	"math/rand"
	"testing"
	"time"
)

const (
	cr = 0.5
	vs = 100
)

// expectedKey renders the key a generator should produce for the i-th
// position. It goes through storedPosition for the same reason the generators
// do: stored keys take the even positions so that missing keys can sit between
// them. See issue #162.
func expectedKey(i int) string {
	return fmt.Sprintf(keyFormat, storedPosition(i))
}

func TestCompressibleBytes(t *testing.T) {
	t.Parallel()

	rng := rand.New(rand.NewSource(time.Now().Unix()))

	bts := compressibleBytes(rng, cr, vs)
	if !bytes.Equal(bts[:50], bts[50:]) {
		t.Errorf("expected \n%s to equal \n%s", string(bts[:50]), string(bts[50:]))
	}

	bts = compressibleBytes(rng, 0.25, vs)
	if !bytes.Equal(bts[:25], bts[25:50]) || !bytes.Equal(bts[50:75], bts[75:]) {
		t.Errorf("expected \n%s to equal \n%s", string(bts[:50]), string(bts[50:]))
	}
}

func TestRandomValueGenerator(t *testing.T) {
	t.Parallel()

	rng := rand.New(rand.NewSource(time.Now().Unix()))

	t.Run("generates random values", func(t *testing.T) {
		gen := makeRandomValueGenerator(rng, cr, vs)
		if bytes.Equal(gen.Value(1), gen.Value(2)) {
			t.Fatal("expected values to be random, got same value")
		}
	})

	t.Run("respects value size", func(t *testing.T) {
		gen := makeRandomValueGenerator(rng, cr, 10)
		if len(gen.Value(1)) != 10 {
			t.Fatal("expected values size to be 10, got", len(gen.Value(1)))
		}
	})
}

func TestFullRandomEntryGenerator(t *testing.T) {
	t.Parallel()

	t.Run("startAt is respected", func(t *testing.T) {
		startAt, size := 10, 100
		gen := newFullRandomEntryGenerator(startAt, size)
		minVal := []byte(expectedKey(startAt))
		for i := 0; i < gen.NKey(); i++ {
			if bytes.Compare(minVal, gen.Key(i)) > 0 {
				t.Fatalf("%s should not be lower than %s", gen.Key(i), minVal)
			}
		}
	})
}

func TestSequentialEntryGenerator(t *testing.T) {
	t.Parallel()

	t.Run("generated values are consecutive ascending", func(t *testing.T) {
		gen := newSequentialEntryGenerator(0, 10)
		for i := 0; i < gen.NKey(); i++ {
			expected := expectedKey(i)
			if expected != string(gen.Key(i)) {
				t.Fatalf("%s expected to equal %s", expected, string(gen.Key(i)))
			}
		}
	})
}

func TestReverseGenerator(t *testing.T) {
	t.Parallel()

	t.Run("generated values are consecutive descending", func(t *testing.T) {
		gen := newReversedKeyGenerator(newSequentialKeyGenerator(10))
		for i := 0; i < gen.NKey(); i++ {
			expected := expectedKey(9 - i)
			if expected != string(gen.Key(i)) {
				t.Fatalf("%s expected to equal %s", expected, string(gen.Key(i)))
			}
		}
	})
}

func TestStartAtEntryGenerator(t *testing.T) {
	t.Parallel()

	t.Run("generated values are consecutive ascending", func(t *testing.T) {
		startAt := 5
		gen := newStartAtEntryGenerator(startAt, newSequentialEntryGenerator(0, 10))
		for i := 0; i < gen.NKey(); i++ {
			expected := expectedKey(i + startAt)
			if expected != string(gen.Key(i)) {
				t.Fatalf("%s expected to equal %s", expected, string(gen.Key(i)))
			}
		}
	})
}

func TestRoundKeyGenerator(t *testing.T) {
	t.Parallel()

	t.Run("repeating values are generated", func(t *testing.T) {
		gen := newRoundKeyGenerator(newRandomKeyGenerator(100))
		v := string(gen.Key(50))
		var found bool
		for i := 50; i < gen.NKey()+50; i++ {
			if v == string(gen.Key(i)) {
				found = true
				break
			}
		}
		if !found {
			t.Fatal("repeating values not found")
		}
	})
}

// TestMissingKeysLieInsideTheStoredRange is the regression test for issue
// #162.
//
// Missing keys used to be formatted "0%015d" while stored keys were formatted
// "1%015d", so every missing key sorted below the entire stored key space and
// a table rejected it on its key range before reading a bloom filter, an index
// block or a data block. BenchmarkReadRandomMissing was therefore measuring
// range rejection, not the lookup a node performs when it is asked for a chunk
// it does not hold.
//
// The property is that a missing key must be absent but not out of range.
// Nothing here depends on how that is arranged.
func TestMissingKeysLieInsideTheStoredRange(t *testing.T) {
	t.Parallel()

	const size = 100

	stored := newSequentialKeyGenerator(size)
	lowest, highest := stored.Key(0), stored.Key(size-1)

	present := make(map[string]struct{}, size)
	for i := range size {
		present[string(stored.Key(i))] = struct{}{}
	}

	missing := newRandomMissingKeyGenerator(size)
	for i := range missing.NKey() {
		key := missing.Key(i)

		if _, ok := present[string(key)]; ok {
			t.Fatalf("missing key %s was written by the setup, so this "+
				"benchmark measures a hit", key)
		}
		if bytes.Compare(key, lowest) < 0 || bytes.Compare(key, highest) > 0 {
			t.Fatalf("missing key %s is outside the stored range [%s, %s], so "+
				"it is rejected on the key range and the benchmark measures "+
				"that instead of a lookup (issue #162)", key, lowest, highest)
		}
	}
}

// TestOutOfRangeKeysAreBelowTheStoredRange pins the other half of the split.
//
// Rejecting an out-of-range key is a real and much cheaper path, kept as its
// own benchmark. It is only worth keeping if it stays genuinely out of range.
func TestOutOfRangeKeysAreBelowTheStoredRange(t *testing.T) {
	t.Parallel()

	const size = 100

	lowest := newSequentialKeyGenerator(size).Key(0)

	g := newRandomOutOfRangeKeyGenerator(size)
	for i := range g.NKey() {
		if key := g.Key(i); bytes.Compare(key, lowest) >= 0 {
			t.Fatalf("out-of-range key %s is not below the lowest stored key "+
				"%s, so it no longer measures range rejection", key, lowest)
		}
	}
}
