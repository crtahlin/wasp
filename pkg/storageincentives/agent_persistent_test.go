// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package storageincentives_test

import (
	"context"
	"math/big"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethersphere/bee/v2/pkg/storageincentives/redistribution"
	"github.com/ethersphere/bee/v2/pkg/storer"
	"github.com/ethersphere/bee/v2/pkg/swarm"
	"github.com/ethersphere/bee/v2/pkg/transaction"
	"github.com/ethersphere/bee/v2/pkg/util/testutil"
)

// markRecorder records, per contract call, whether every call it saw carried
// the persistent-calls mark (#783).
type markRecorder struct {
	*mockContract
	mu     sync.Mutex
	seen   map[string]bool
	marked map[string]bool
}

func (r *markRecorder) note(name string, ctx context.Context) {
	_, ok := transaction.PersistentCalls(ctx)
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.seen[name] {
		r.seen[name], r.marked[name] = true, ok
		return
	}
	r.marked[name] = r.marked[name] && ok
}

func (r *markRecorder) result(name string) (seen, marked bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.seen[name], r.marked[name]
}

func (r *markRecorder) IsPlaying(ctx context.Context, depth uint8) (bool, error) {
	r.note("isPlaying", ctx)
	return r.mockContract.IsPlaying(ctx, depth)
}

func (r *markRecorder) IsWinner(ctx context.Context) (bool, error) {
	r.note("isWinner", ctx)
	return r.mockContract.IsWinner(ctx)
}

func (r *markRecorder) Commit(ctx context.Context, obfusHash []byte, round uint64, f transaction.BeforeBroadcastFunc) (common.Hash, error) {
	r.note("commit", ctx)
	return r.mockContract.Commit(ctx, obfusHash, round, f)
}

func (r *markRecorder) Reveal(ctx context.Context, depth uint8, hash, nonce []byte, f transaction.BeforeBroadcastFunc) (common.Hash, error) {
	r.note("reveal", ctx)
	return r.mockContract.Reveal(ctx, depth, hash, nonce, f)
}

var _ redistribution.Contract = (*markRecorder)(nil)

// TestAgentMarksLotteryCallsPersistent checks that the commit, reveal and
// claim phases make their chain calls with the persistent-calls mark, so the
// failover backend bounds each attempt and goes round its endpoints (#783),
// and that the sample phase does not: its context also bounds the reserve
// sample.
func TestAgentMarksLotteryCallsPersistent(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		wait := make(chan struct{}, 1)
		backend := &mockchainBackend{
			limit:         108,
			limitCallback: func() { wait <- struct{}{} },
			incrementBy:   1,
			block:         9,
			balance:       big.NewInt(4_000_000_000),
		}
		var radius uint8 = 8
		contract := &markRecorder{
			mockContract: &mockContract{t: t, expectedRadius: radius},
			seen:         map[string]bool{},
			marked:       map[string]bool{},
		}

		service, _ := createService(t, swarm.RandAddress(t), backend, contract, 9, 3, radius, 0, storer.ReserveProofModeClassic)
		testutil.CleanupCloser(t, service)

		<-wait
		synctest.Wait()

		for _, name := range []string{"commit", "reveal", "isWinner"} {
			seen, marked := contract.result(name)
			if !seen {
				t.Fatalf("%s was not called", name)
			}
			if !marked {
				t.Fatalf("%s was called without the persistent-calls mark", name)
			}
		}
		if seen, marked := contract.result("isPlaying"); !seen || marked {
			t.Fatalf("isPlaying: seen %v, marked %v; want seen and not marked", seen, marked)
		}
	})
}
