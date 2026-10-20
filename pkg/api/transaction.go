// Copyright 2020 The Swarm Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package api

import (
	"encoding/hex"
	"errors"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethersphere/bee/v2/pkg/bigint"
	"github.com/ethersphere/bee/v2/pkg/jsonhttp"
	"github.com/ethersphere/bee/v2/pkg/sctx"
	"github.com/ethersphere/bee/v2/pkg/transaction"
	"github.com/gorilla/mux"
)

const (
	errCantGetTransaction    = "cannot get transaction"
	errUnknownTransaction    = "unknown transaction"
	errCantResendTransaction = "can't resend transaction"
	errNonceUsedByOther      = "the transaction's nonce was used by another transaction"
	errNonceUsedByUnknown    = "the transaction's nonce was used by a transaction this node did not store"
)

type transactionInfo struct {
	TransactionHash common.Hash     `json:"transactionHash"`
	To              *common.Address `json:"to"`
	Nonce           uint64          `json:"nonce"`
	GasPrice        *bigint.BigInt  `json:"gasPrice"`
	GasLimit        uint64          `json:"gasLimit"`
	GasTipBoost     int             `json:"gasTipBoost"`
	GasTipCap       *bigint.BigInt  `json:"gasTipCap"`
	GasFeeCap       *bigint.BigInt  `json:"gasFeeCap"`
	Data            string          `json:"data"`
	Created         time.Time       `json:"created"`
	Description     string          `json:"description"`
	Value           *bigint.BigInt  `json:"value"`
	// State is pending, overdue, blocked (with BlockedReason), mined or
	// cancelled_for_reveal; empty for a transaction that is no longer
	// pending (#819).
	State         string `json:"state,omitempty"`
	BlockedReason string `json:"blockedReason,omitempty"`
}

// newTransactionInfo describes a stored transaction for the API.
func (s *Service) newTransactionInfo(txHash common.Hash, storedTransaction *transaction.StoredTransaction) transactionInfo {
	info := transactionInfo{
		TransactionHash: txHash,
		To:              storedTransaction.To,
		Nonce:           storedTransaction.Nonce,
		GasPrice:        bigint.Wrap(storedTransaction.GasPrice),
		GasLimit:        storedTransaction.GasLimit,
		GasFeeCap:       bigint.Wrap(storedTransaction.GasFeeCap),
		GasTipCap:       bigint.Wrap(storedTransaction.GasTipCap),
		GasTipBoost:     storedTransaction.GasTipBoost,
		Data:            hexutil.Encode(storedTransaction.Data),
		Created:         time.Unix(storedTransaction.Created, 0),
		Description:     storedTransaction.Description,
		Value:           bigint.Wrap(storedTransaction.Value),
	}
	if state, err := s.transaction.PendingState(txHash); err == nil {
		info.State, info.BlockedReason = state.State, state.BlockedReason
	}
	return info
}

type transactionPendingList struct {
	PendingTransactions []transactionInfo `json:"pendingTransactions"`
}

func (s *Service) transactionListHandler(w http.ResponseWriter, _ *http.Request) {
	logger := s.logger.WithName("get_transactions").Build()

	txHashes, err := s.transaction.PendingTransactions()
	if err != nil {
		logger.Debug("get pending transactions failed", "error", err)
		logger.Error(nil, "get pending transactions failed")
		jsonhttp.InternalServerError(w, errCantGetTransaction)
		return
	}

	transactionInfos := make([]transactionInfo, 0, len(txHashes))
	for _, txHash := range txHashes {
		storedTransaction, err := s.transaction.StoredTransaction(txHash)
		if err != nil {
			logger.Debug("get stored transaction failed", "tx_hash", txHash, "error", err)
			logger.Error(nil, "get stored transaction failed", "tx_hash", txHash)
			jsonhttp.InternalServerError(w, errCantGetTransaction)
			return
		}

		transactionInfos = append(transactionInfos, s.newTransactionInfo(txHash, storedTransaction))
	}

	jsonhttp.OK(w, transactionPendingList{
		PendingTransactions: transactionInfos,
	})
}

func (s *Service) transactionDetailHandler(w http.ResponseWriter, r *http.Request) {
	logger := s.logger.WithName("get_transaction").Build()

	paths := struct {
		Hash common.Hash `map:"hash"`
	}{}
	if response := s.mapStructure(mux.Vars(r), &paths); response != nil {
		response("invalid path params", logger, w)
		return
	}

	storedTransaction, err := s.transaction.StoredTransaction(paths.Hash)
	if err != nil {
		logger.Debug("get stored transaction failed", "tx_hash", paths.Hash, "error", err)
		logger.Error(nil, "get stored transaction failed", "tx_hash", paths.Hash)
		if errors.Is(err, transaction.ErrUnknownTransaction) {
			jsonhttp.NotFound(w, errUnknownTransaction)
		} else {
			jsonhttp.InternalServerError(w, errCantGetTransaction)
		}
		return
	}

	jsonhttp.OK(w, s.newTransactionInfo(paths.Hash, storedTransaction))
}

type transactionHashResponse struct {
	TransactionHash common.Hash `json:"transactionHash"`
}

// endpointAnswer is one endpoint's answer to a resent transaction: accepted,
// already known, or refused with the endpoint's text.
type endpointAnswer struct {
	Endpoint string `json:"endpoint"`
	Result   string `json:"result"`
	Error    string `json:"error,omitempty"`
}

// transactionResendResponse is the answer of POST /transactions/{hash}
// (#819). For a mined transaction State is mined and BlockNumber is set; for
// a pending one State is pending, overdue or blocked and Answers holds each
// endpoint's answer.
type transactionResendResponse struct {
	TransactionHash common.Hash      `json:"transactionHash"`
	State           string           `json:"state"`
	BlockNumber     uint64           `json:"blockNumber,omitempty"`
	Answers         []endpointAnswer `json:"answers,omitempty"`
}

// transactionNonceUsedResponse is the 409 answer of POST
// /transactions/{hash} when another transaction used the nonce.
type transactionNonceUsedResponse struct {
	Code            int          `json:"code"`
	Message         string       `json:"message"`
	TransactionHash common.Hash  `json:"transactionHash"`
	UsedBy          *common.Hash `json:"usedBy,omitempty"`
}

func newEndpointAnswers(answers []transaction.SendAnswer) []endpointAnswer {
	out := make([]endpointAnswer, 0, len(answers))
	for _, a := range answers {
		ea := endpointAnswer{Endpoint: a.Endpoint}
		switch {
		case a.Err == nil:
			ea.Result = "accepted"
		case a.Kind() == transaction.AnswerAccepted:
			ea.Result = "already known"
		default:
			ea.Result = "refused"
			ea.Error = a.Err.Error()
		}
		out = append(out, ea)
	}
	return out
}

func (s *Service) transactionResendHandler(w http.ResponseWriter, r *http.Request) {
	logger := s.logger.WithName("post_transaction").Build()

	paths := struct {
		Hash common.Hash `map:"hash"`
	}{}
	if response := s.mapStructure(mux.Vars(r), &paths); response != nil {
		response("invalid path params", logger, w)
		return
	}

	// common.HexToHash takes any text, so a malformed hash is refused
	// here rather than resent as a zero-padded one.
	if !validTransactionHash(mux.Vars(r)["hash"]) {
		jsonhttp.BadRequest(w, jsonhttp.StatusResponse{
			Code:    http.StatusBadRequest,
			Message: "invalid path params",
			Reasons: []jsonhttp.Reason{{Field: "hash", Error: "not a 32-byte hex transaction hash"}},
		})
		return
	}

	res, err := s.transaction.ResendTransaction(r.Context(), paths.Hash)
	if err != nil {
		logger.Error(nil, "resend transaction failed", "tx_hash", paths.Hash, "error", err)
		if errors.Is(err, transaction.ErrUnknownTransaction) {
			jsonhttp.NotFound(w, errUnknownTransaction)
		} else {
			jsonhttp.InternalServerError(w, errCantResendTransaction)
		}
		return
	}

	switch {
	case res.Mined:
		jsonhttp.OK(w, transactionResendResponse{
			TransactionHash: paths.Hash,
			State:           transaction.StateMined,
			BlockNumber:     res.BlockNumber,
		})
	case res.NonceUsed:
		body := transactionNonceUsedResponse{
			Code:            http.StatusConflict,
			Message:         errNonceUsedByUnknown,
			TransactionHash: paths.Hash,
		}
		if res.NonceUsedBy != (common.Hash{}) {
			usedBy := res.NonceUsedBy
			body.Message, body.UsedBy = errNonceUsedByOther, &usedBy
		}
		jsonhttp.Conflict(w, body)
	default:
		// 200 also when every endpoint refused: the request succeeded, and
		// the answers say what happened.
		jsonhttp.OK(w, transactionResendResponse{
			TransactionHash: paths.Hash,
			State:           res.State,
			Answers:         newEndpointAnswers(res.Answers),
		})
	}
}

// validTransactionHash reports whether v is a 32-byte hex hash, with or
// without the 0x prefix.
func validTransactionHash(v string) bool {
	v = strings.TrimPrefix(strings.TrimPrefix(v, "0x"), "0X")
	if len(v) != 2*common.HashLength {
		return false
	}
	_, err := hex.DecodeString(v)
	return err == nil
}

func (s *Service) transactionCancelHandler(w http.ResponseWriter, r *http.Request) {
	logger := s.logger.WithName("delete_transaction").Build()

	paths := struct {
		Hash common.Hash `map:"hash"`
	}{}
	if response := s.mapStructure(mux.Vars(r), &paths); response != nil {
		response("invalid path params", logger, w)
		return
	}

	headers := struct {
		GasPrice *big.Int `map:"Gas-Price"`
	}{}
	if response := s.mapStructure(r.Header, &headers); response != nil {
		response("invalid header params", logger, w)
		return
	}
	ctx := sctx.SetGasPrice(r.Context(), headers.GasPrice)

	txHash, err := s.transaction.CancelTransaction(ctx, paths.Hash)
	if err != nil {
		logger.Debug("cancel transaction failed", "tx_hash", paths.Hash, "error", err, "canceled_tx_hash", txHash)
		logger.Error(nil, "cancel transaction failed", "tx_hash", paths.Hash)
		if errors.Is(err, transaction.ErrUnknownTransaction) {
			jsonhttp.NotFound(w, errUnknownTransaction)
		} else {
			jsonhttp.InternalServerError(w, errCantResendTransaction)
		}
		return
	}

	jsonhttp.OK(w, transactionHashResponse{
		TransactionHash: txHash,
	})
}
