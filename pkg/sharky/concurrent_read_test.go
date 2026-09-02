// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sharky_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ethersphere/bee/v2/pkg/sharky"
)

// TestReadDuringClose exercises the read-and-close interleaving that the shard
// lifecycle guard exists for. Reads now run directly in the caller's goroutine
// instead of through the write actor, so a read can be in flight when Close
// closes the shard file. The first attempt at concurrent reads (#63) crashed a
// real node here with a use-after-close and was reverted (#66); the unit tests
// of the time did not reach the interleaving. This drives many concurrent reads
// while the store is closed underneath them, repeated across reopens the way a
// restarting node would, so -race and goleak have the race to catch.
//
// A read must return correct bytes or ErrQuitting. It must never return wrong
// bytes and never panic. This is the invariant the guard protects.
func TestReadDuringClose(t *testing.T) {
	t.Parallel()

	const (
		shards   = 4
		datasize = 32
		blobs    = 512
		readers  = 32
		rounds   = 40
	)
	dir := t.TempDir()

	type item struct {
		loc  sharky.Location
		want []byte
	}

	// Populate a persistent store once, recording every location and its bytes,
	// so later rounds reopen the same reserve and read known content back.
	items := make([]item, 0, blobs)
	s, err := sharky.New(&dirFS{basedir: dir}, shards, datasize)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < blobs; i++ {
		data := make([]byte, datasize)
		binary.LittleEndian.PutUint32(data, uint32(i))
		loc, err := s.Write(context.Background(), data)
		if err != nil {
			t.Fatal("write", err)
		}
		items = append(items, item{loc: loc, want: data})
	}
	if err := s.Close(); err != nil {
		t.Fatal("close after fill", err)
	}

	for round := 0; round < rounds; round++ {
		s, err := sharky.New(&dirFS{basedir: dir}, shards, datasize)
		if err != nil {
			t.Fatal("reopen", err)
		}

		var wg sync.WaitGroup
		start := make(chan struct{})
		for r := 0; r < readers; r++ {
			wg.Add(1)
			go func(seed int) {
				defer wg.Done()
				buf := make([]byte, datasize)
				<-start
				for n := 0; ; n++ {
					it := items[(seed*7+n)%len(items)]
					err := s.Read(context.Background(), it.loc, buf)
					if errors.Is(err, sharky.ErrQuitting) {
						return // store closed underneath us: the expected way to stop
					}
					if err != nil {
						t.Errorf("read %v: unexpected error %v", it.loc, err)
						return
					}
					if !bytes.Equal(buf[:it.loc.Length], it.want) {
						t.Errorf("corrupt read at %v: got %x want %x", it.loc, buf[:it.loc.Length], it.want)
						return
					}
				}
			}(r)
		}

		close(start)
		// Let the readers get going, then close the store while their reads are
		// in flight. This is the interleaving the first attempt crashed on.
		time.Sleep(time.Millisecond)
		if err := s.Close(); err != nil {
			t.Fatal("close under reads", err)
		}
		wg.Wait()
	}
}
