// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build replay

package storageincentives

import (
	"context"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethersphere/bee/v2/pkg/log"
	"github.com/ethersphere/bee/v2/pkg/storageincentives/redistribution"
	"github.com/ethersphere/bee/v2/pkg/swarm"
	"github.com/ethersphere/bee/v2/pkg/transaction"
)

// This file is built only with the replay build tag. It lets the
// code-level replay of docs/experiments/lottery-fee-pricing/replay drive
// the node's own commit, reveal and claim code, phase loop included,
// against a simulated chain (#796, #846). No node build includes it.

// replaySample is the sample a replayed commit and reveal carry; the
// simulated chain does not look at it.
var replaySample = SampleData{ReserveSampleHash: swarm.MustParseHexAddress("aa"), StorageRadius: 8}

// NewReplayAgent builds an agent that commits and claims through contract
// and revealer, with nothing else set: only ReplayCommit and ReplayClaim
// may be called on it.
func NewReplayAgent(contract redistribution.Contract, revealer *Revealer, overlay swarm.Address) *Agent {
	return &Agent{
		logger:   log.Noop,
		metrics:  newMetrics(),
		contract: contract,
		revealer: revealer,
		state:    revealer.state,
		overlay:  overlay,
	}
}

// ReplayCommit runs the agent's commit of round: the key stored first, the
// send, and the replacements within the commit phase.
func (a *Agent) ReplayCommit(ctx context.Context, round uint64) error {
	return a.commit(ctx, replaySample, round)
}

// ReplayClaim runs the agent's claim of round, with empty proofs, and its
// replacements within the claim phase.
func (a *Agent) ReplayClaim(ctx context.Context, round uint64) (common.Hash, error) {
	return a.claim(ctx, round, redistribution.ChunkInclusionProofs{})
}

// ReplayReveal stores a commit key for round and the sample of the round
// before, as a node that committed has them, and runs the revealer's
// reveal phase loop. It reports whether the round is revealed.
func (r *Revealer) ReplayReveal(ctx context.Context, round uint64) (bool, error) {
	if err := r.state.SetCommitKey(round, make([]byte, swarm.HashSize)); err != nil {
		return false, err
	}
	r.state.SetSampleData(round-1, replaySample, time.Second)
	return r.revealUntil(ctx, round), nil
}

// ReplaySendInPhase runs the phase loop of a commit or a claim for any
// send: the send at once, then the replacements until one of its
// transactions is mined or block end.
func (r *Revealer) ReplaySendInPhase(ctx context.Context, name string, round, end uint64,
	send func(ctx context.Context, hook transaction.BeforeBroadcastFunc) (common.Hash, error),
) (common.Hash, error) {
	return r.sendInPhase(ctx, name, round, end, send, nil)
}
