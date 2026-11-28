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

// notClaimPhaseSelector is the 4-byte selector of the contract's
// NotClaimPhase() error, which isWinner reverts with when it is called
// outside the claim phase.
var notClaimPhaseSelector = crypto.Keccak256([]byte("NotClaimPhase()"))[:4]

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
	return revertRetryable(err, wrongPhaseSelector)
}

// WinnerCheckRetryable reports whether a failed IsWinner call may pass if it
// is asked again a block later (#855).
//
// A NotClaimPhase() revert is retryable: the node enters the claim phase on
// its estimated block number, which can run a block ahead of the chain, so
// the call lands on the last block of the reveal phase. A revert with any
// other selector, such as AlreadyClaimed(), is not. An error without revert
// data, including a transport error, is retryable. A cancelled or expired
// context is not.
func WinnerCheckRetryable(err error) bool {
	return revertRetryable(err, notClaimPhaseSelector)
}

// revertRetryable reports whether a failed contract call may pass a block
// later: its revert data starts with selector, or it carries no revert data
// at all. A cancelled or expired context is never retryable.
func revertRetryable(err error, selector []byte) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	data, ok := revertData(err)
	if !ok || len(data) < len(selector) {
		return true
	}
	return bytes.Equal(data[:len(selector)], selector)
}

// revertData returns the revert data an rpc.DataError in err's chain
// carries, and false when there is none or it is not a hex string.
func revertData(err error) ([]byte, bool) {
	var derr rpc.DataError
	if !errors.As(err, &derr) {
		return nil, false
	}
	hexData, ok := derr.ErrorData().(string)
	if !ok {
		return nil, false
	}
	return common.FromHex(hexData), true
}
