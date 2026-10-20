// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package transaction

import (
	"context"
	"errors"
	"strings"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rpc"
)

// ErrEndpointUnreachable marks an endpoint's answer as no answer at all: a
// transport failure, or a dial that failed.
var ErrEndpointUnreachable = errors.New("endpoint unreachable")

// AnswerKind is what an endpoint's answer to eth_sendRawTransaction means
// for the transaction it was sent. See
// docs/experiments/send-everywhere/spec.md, part 2.
type AnswerKind int

const (
	// AnswerAccepted: the endpoint holds the bytes.
	AnswerAccepted AnswerKind = iota + 1
	// AnswerNonceUsed: the chain has used the nonce.
	AnswerNonceUsed
	// AnswerHeldOther: the endpoint holds another transaction at this nonce.
	AnswerHeldOther
	// AnswerFeeTooLow: the fee is the problem.
	AnswerFeeTooLow
	// AnswerDefinite: the bytes can never be mined as they are.
	AnswerDefinite
	// AnswerOther: any other answer from the endpoint.
	AnswerOther
	// AnswerUnreachable: the endpoint did not answer.
	AnswerUnreachable
)

// String names the kind as the watchdog's counter labels it.
func (k AnswerKind) String() string {
	switch k {
	case AnswerAccepted:
		return "accepted"
	case AnswerNonceUsed:
		return "nonce_used"
	case AnswerHeldOther:
		return "held_other"
	case AnswerFeeTooLow:
		return "fee_too_low"
	case AnswerDefinite:
		return "definite"
	case AnswerOther:
		return "other"
	case AnswerUnreachable:
		return "unreachable"
	}
	return "unknown"
}

// SendAnswer is one endpoint's answer to a sent transaction.
type SendAnswer struct {
	Endpoint string
	Err      error
}

// Kind classifies the answer.
func (a SendAnswer) Kind() AnswerKind { return ClassifySendAnswer(a.Err) }

// Broadcaster is implemented by a backend that can send one transaction to
// every endpoint it has and report each endpoint's answer. The failover
// backend does; the plain single-endpoint backend does not, and the service
// then sends through SendTransaction and takes its error as the only answer.
type Broadcaster interface {
	Broadcast(ctx context.Context, tx *types.Transaction) []SendAnswer
}

// answerTexts are matched, in order, against an answer's text without
// regard to case. The go-ethereum messages are the ones #772 verified
// against go-ethereum v1.17.3, the Nethermind names are its result names.
// "replacement transaction underpriced" comes before "transaction
// underpriced", which it contains.
var answerTexts = []struct {
	kind  AnswerKind
	texts []string
}{
	{AnswerAccepted, []string{"already known", "known transaction", "alreadyknown"}},
	{AnswerNonceUsed, []string{"nonce too low", "oldnonce"}},
	{AnswerHeldOther, []string{"replacement transaction underpriced", "replacementnotallowed"}},
	{AnswerFeeTooLow, []string{
		"transaction underpriced", "feetoolow",
		"max fee per gas less than block base fee", "fee cap less than block base fee",
	}},
	{AnswerDefinite, []string{
		"insufficient funds", "intrinsic gas too low", "gas limit reached",
		"exceeds block gas limit", "insufficientfunds", "gaslimittoolow",
	}},
}

// ClassifySendAnswer reads an endpoint's answer to a sent transaction. nil
// is accepted. An error marked with ErrEndpointUnreachable is unreachable.
// Otherwise the text decides; an answer with no known text is AnswerOther
// when it is a JSON-RPC error or an HTTP status that every endpoint would
// give (a 4xx other than 429), and AnswerUnreachable otherwise, as a
// transport failure.
func ClassifySendAnswer(err error) AnswerKind {
	if err == nil {
		return AnswerAccepted
	}
	if errors.Is(err, ErrEndpointUnreachable) {
		return AnswerUnreachable
	}
	msg := strings.ToLower(err.Error())
	for _, a := range answerTexts {
		for _, text := range a.texts {
			if strings.Contains(msg, text) {
				return a.kind
			}
		}
	}
	var rpcErr rpc.Error
	if errors.As(err, &rpcErr) {
		return AnswerOther
	}
	var httpErr rpc.HTTPError
	if errors.As(err, &httpErr) && httpErr.StatusCode < 500 && httpErr.StatusCode != 429 {
		return AnswerOther
	}
	return AnswerUnreachable
}
