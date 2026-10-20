// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pullsync_test

import (
	"testing"

	"github.com/ethersphere/bee/v2/pkg/log"
	"github.com/ethersphere/bee/v2/pkg/pullsync"
	"github.com/ethersphere/bee/v2/pkg/soc"
	chunktesting "github.com/ethersphere/bee/v2/pkg/storage/testing"
	mock "github.com/ethersphere/bee/v2/pkg/storer/mock"
	"github.com/ethersphere/bee/v2/pkg/swarm"
)

// TestUnwrapHeldValidatesSOCAddress checks that a held single owner chunk
// reaches the gsoc handler only under its own address. A peer can send a
// correctly signed SOC under any address; Bee v2.8.3 rejects that in its
// push-sync and pull-sync handlers, and wasp's held-chunk path must do the
// same.
func TestUnwrapHeldValidatesSOCAddress(t *testing.T) {
	t.Parallel()

	var handled []swarm.Address
	s := pullsync.New(nil, mock.NewReserve(), func(swarm.Chunk) {},
		func(sc *soc.SOC) {
			addr, err := sc.Address()
			if err != nil {
				t.Error(err)
			}
			handled = append(handled, addr)
		},
		func(ch swarm.Chunk) (swarm.Chunk, error) { return ch, nil }, log.Noop, 5, pullsync.DefaultMaxChunksPerSecond)
	t.Cleanup(func() { _ = s.Close() })

	sch := chunktesting.GenerateTestRandomSoChunk(t, chunktesting.GenerateTestRandomChunk())

	s.UnwrapHeld(swarm.NewChunk(swarm.RandAddress(t), sch.Data()))
	if len(handled) != 0 {
		t.Fatalf("a SOC under another address reached the gsoc handler: %v", handled)
	}

	s.UnwrapHeld(sch)
	if len(handled) != 1 || !handled[0].Equal(sch.Address()) {
		t.Fatalf("the SOC under its own address was not handled once: %v", handled)
	}
}
