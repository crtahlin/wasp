// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package redistribution

import (
	"bytes"
	"context"
	"errors"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rpc"
)

// wrongPhaseSelector is the 4-byte selector of the contract's WrongPhase()
// error, which isParticipatingInUpcomingRound reverts with when it is called
// outside the claim phase.
var wrongPhaseSelector = crypto.Keccak256([]byte("WrongPhase()"))[:4]

// PlayCheckRetryable reports whether a failed IsPlaying call may pass if it is
// asked again a block later (#773).
//
// A WrongPhase() revert is retryable: the node's estimated block number can
// run a block ahead of the chain, or the answering endpoint can be a block
// behind, so the call lands on the last block of the reveal phase. A revert
// with any other selector, such as NotStaked() or MustStake2Rounds(), holds
// for the whole round and is not. An error without revert data, including a
// transport error, is retryable. A cancelled or expired context is not.
func PlayCheckRetryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var derr rpc.DataError
	if !errors.As(err, &derr) {
		return true
	}
	hexData, ok := derr.ErrorData().(string)
	if !ok {
		return true
	}
	data := common.FromHex(hexData)
	if len(data) < len(wrongPhaseSelector) {
		return true
	}
	return bytes.Equal(data[:len(wrongPhaseSelector)], wrongPhaseSelector)
}
