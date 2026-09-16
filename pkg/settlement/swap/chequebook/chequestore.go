// Copyright 2020 The Swarm Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package chequebook

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethersphere/bee/v2/pkg/crypto"
	"github.com/ethersphere/bee/v2/pkg/storage"
	"github.com/ethersphere/bee/v2/pkg/transaction"
)

const (
	// prefix for the persistence key
	lastReceivedChequePrefix = "swap_chequebook_last_received_cheque_"
	// prefix for the persistence key of a chequebook's issuer. It must not begin
	// with lastReceivedChequePrefix, which LastCheques iterates over, reading an
	// address out of every key it finds.
	issuerPrefix = "swap_chequebook_issuer_"
	// chequeLiquidityValidity is how long a chequebook's balance and paid out
	// total are reused before they are read from the chain again. Reading them
	// for every cheque puts three chain calls on the path that accepts one,
	// which is what limits how fast debt clears with a peer.
	chequeLiquidityValidity = 30 * time.Second
)

var (
	// ErrNoCheque is the error returned if there is no prior cheque for a chequebook or beneficiary.
	ErrNoCheque = errors.New("no cheque")
	// ErrChequeNotIncreasing is the error returned if the cheque amount is the same or lower.
	ErrChequeNotIncreasing = errors.New("cheque cumulativePayout is not increasing")
	// ErrChequeInvalid is the error returned if the cheque itself is invalid.
	ErrChequeInvalid = errors.New("invalid cheque")
	// ErrWrongBeneficiary is the error returned if the cheque has the wrong beneficiary.
	ErrWrongBeneficiary = errors.New("wrong beneficiary")
	// ErrBouncingCheque is the error returned if the chequebook is demonstrably illiquid.
	ErrBouncingCheque = errors.New("bouncing cheque")
	// ErrChequeValueTooLow is the error returned if the after deduction value of a cheque did not cover 1 accounting credit
	ErrChequeValueTooLow = errors.New("cheque value lower than acceptable")
)

// ChequeStore handles the verification and storage of received cheques
type ChequeStore interface {
	// ReceiveCheque verifies and stores a cheque. It returns the total amount earned.
	ReceiveCheque(ctx context.Context, cheque *SignedCheque, exchangeRate, deduction *big.Int) (*big.Int, error)
	// LastCheque returns the last cheque we received from a specific chequebook.
	LastCheque(chequebook common.Address) (*SignedCheque, error)
	// LastCheques returns the last received cheques from every known chequebook.
	LastCheques() (map[common.Address]*SignedCheque, error)
}

type chequeStore struct {
	lock               sync.Mutex
	store              storage.StateStorer
	factory            Factory
	chaindID           int64
	transactionService transaction.Service
	beneficiary        common.Address // the beneficiary we expect in cheques sent to us
	recoverChequeFunc  RecoverChequeFunc
	metrics            metrics
	timeNow            func() time.Time
	// liquidity is what each chequebook's contract last reported. It is a cache,
	// so it is empty again after a restart, and is read under lock.
	liquidity map[common.Address]liquidity
}

// liquidity is what a chequebook could pay this beneficiary when it was last
// read from the chain, and when that was.
type liquidity struct {
	balance        *big.Int
	alreadyPaidOut *big.Int
	readAt         time.Time
}

type RecoverChequeFunc func(cheque *SignedCheque, chainID int64) (common.Address, error)

// NewChequeStore creates new ChequeStore
func NewChequeStore(
	store storage.StateStorer,
	factory Factory,
	chainID int64,
	beneficiary common.Address,
	transactionService transaction.Service,
	recoverChequeFunc RecoverChequeFunc,
) ChequeStore {
	return &chequeStore{
		store:              store,
		factory:            factory,
		chaindID:           chainID,
		transactionService: transactionService,
		beneficiary:        beneficiary,
		recoverChequeFunc:  recoverChequeFunc,
		metrics:            newMetrics(),
		timeNow:            time.Now,
		liquidity:          make(map[common.Address]liquidity),
	}
}

// lastReceivedChequeKey computes the key where to store the last cheque received from a chequebook.
func lastReceivedChequeKey(chequebook common.Address) string {
	return fmt.Sprintf("%s_%x", lastReceivedChequePrefix, chequebook)
}

// issuerKey computes the key where to store the issuer of a chequebook.
func issuerKey(chequebook common.Address) string {
	return fmt.Sprintf("%s_%x", issuerPrefix, chequebook)
}

// LastCheque returns the last cheque we received from a specific chequebook.
func (s *chequeStore) LastCheque(chequebook common.Address) (*SignedCheque, error) {
	var cheque *SignedCheque
	err := s.store.Get(lastReceivedChequeKey(chequebook), &cheque)
	if err != nil {
		if !errors.Is(err, storage.ErrNotFound) {
			return nil, err
		}
		return nil, ErrNoCheque
	}

	// A corrupt statestore entry (e.g. a literal JSON null) unmarshals into a
	// nil pointer without an error; guard against returning it for later deref.
	if cheque == nil {
		return nil, fmt.Errorf("nil cheque loaded from statestore for chequebook %x: %w", chequebook, ErrNoCheque)
	}

	return cheque, nil
}

// ReceiveCheque verifies and stores a cheque. It returns the totam amount earned.
func (s *chequeStore) ReceiveCheque(ctx context.Context, cheque *SignedCheque, exchangeRate, deduction *big.Int) (*big.Int, error) {
	// verify we are the beneficiary
	if cheque.Beneficiary != s.beneficiary {
		return nil, ErrWrongBeneficiary
	}

	// A peer-supplied cheque omitting cumulativePayout unmarshals into a nil
	// *big.Int without an error; guard against dereferencing it below.
	if cheque.CumulativePayout == nil {
		return nil, ErrChequeInvalid
	}

	// don't allow concurrent processing of cheques
	// this would be sufficient on a per chequebook basis
	s.lock.Lock()
	defer s.lock.Unlock()

	// load the lastCumulativePayout for the cheques chequebook
	var lastCumulativePayout *big.Int
	var lastReceivedCheque *SignedCheque
	err := s.store.Get(lastReceivedChequeKey(cheque.Chequebook), &lastReceivedCheque)
	if err != nil {
		if !errors.Is(err, storage.ErrNotFound) {
			return nil, err
		}

		// if this is the first cheque from this chequebook, verify with the factory.
		err = s.factory.VerifyChequebook(ctx, cheque.Chequebook)
		if err != nil {
			return nil, err
		}

		lastCumulativePayout = big.NewInt(0)
	} else if lastReceivedCheque == nil {
		// A corrupt statestore entry (e.g. a literal JSON null) unmarshals into a
		// nil pointer without an error; guard against dereferencing it below.
		return nil, fmt.Errorf("nil cheque loaded from statestore for chequebook %x: %w", cheque.Chequebook, ErrNoCheque)
	} else {
		lastCumulativePayout = lastReceivedCheque.CumulativePayout
	}

	// check this cheque is actually increasing in value
	amount := big.NewInt(0).Sub(cheque.CumulativePayout, lastCumulativePayout)

	if amount.Cmp(big.NewInt(0)) <= 0 {
		return nil, ErrChequeNotIncreasing
	}

	deducedAmount := new(big.Int).Sub(amount, deduction)

	if deducedAmount.Cmp(exchangeRate) < 0 {
		return nil, ErrChequeValueTooLow
	}

	// the issuer is read from the chain only the first time, see issuerOf
	expectedIssuer, err := s.issuerOf(ctx, cheque.Chequebook)
	if err != nil {
		return nil, err
	}

	// verify the cheque signature
	issuer, err := s.recoverChequeFunc(cheque, s.chaindID)
	if err != nil {
		return nil, err
	}

	if issuer != expectedIssuer {
		return nil, ErrChequeInvalid
	}

	// basic liquidity check, from values read at most once per
	// chequeLiquidityValidity, see liquidityOf
	balance, alreadyPaidOut, err := s.liquidityOf(ctx, cheque.Chequebook)
	if err != nil {
		return nil, err
	}

	if balance.Cmp(big.NewInt(0).Sub(cheque.CumulativePayout, alreadyPaidOut)) < 0 {
		return nil, ErrBouncingCheque
	}

	// store the accepted cheque
	err = s.store.Put(lastReceivedChequeKey(cheque.Chequebook), cheque)
	if err != nil {
		return nil, err
	}

	return amount, nil
}

// issuerOf returns the chequebook's issuer, reading it from the chain only the
// first time. A chequebook cannot change its issuer, so the value is kept for
// good. An entry that is missing, zero or unreadable counts as absent, so that
// one bad entry cannot reject every later cheque from that chequebook.
func (s *chequeStore) issuerOf(ctx context.Context, chequebook common.Address) (common.Address, error) {
	// An entry this node cannot read counts as absent, whatever went wrong with
	// it: a store that has lost the key, and one that returns something that is
	// not an address, both mean the same thing here. Returning the error instead
	// would reject every later cheque from this chequebook, because nothing
	// would ever write the entry again. A store that is broken for writing still
	// reports it, through the Put below.
	var stored common.Address
	if err := s.store.Get(issuerKey(chequebook), &stored); err == nil && stored != (common.Address{}) {
		s.metrics.ChainReadsAvoided.Inc()
		return stored, nil
	}

	s.metrics.ChainReads.Inc()
	issuer, err := newChequebookContract(chequebook, s.transactionService).Issuer(ctx)
	if err != nil {
		return common.Address{}, err
	}

	if err := s.store.Put(issuerKey(chequebook), issuer); err != nil {
		return common.Address{}, err
	}

	return issuer, nil
}

// liquidityOf returns what the chequebook could pay this beneficiary, reading
// the chain at most once per chequeLiquidityValidity. Both values come from the
// same reading, so a cash-out by this node moves them together and cannot make
// the check answer wrongly.
func (s *chequeStore) liquidityOf(ctx context.Context, chequebook common.Address) (*big.Int, *big.Int, error) {
	if l, ok := s.liquidity[chequebook]; ok && s.timeNow().Sub(l.readAt) < chequeLiquidityValidity {
		s.metrics.ChainReadsAvoided.Add(2)
		// copies, so that a caller changing them cannot reach what is kept here
		return new(big.Int).Set(l.balance), new(big.Int).Set(l.alreadyPaidOut), nil
	}

	contract := newChequebookContract(chequebook, s.transactionService)

	// counted before the call, so that a call which fails still shows what it
	// cost the node
	s.metrics.ChainReads.Inc()
	balance, err := contract.Balance(ctx)
	if err != nil {
		return nil, nil, err
	}

	s.metrics.ChainReads.Inc()
	alreadyPaidOut, err := contract.PaidOut(ctx, s.beneficiary)
	if err != nil {
		return nil, nil, err
	}

	s.liquidity[chequebook] = liquidity{
		balance:        balance,
		alreadyPaidOut: alreadyPaidOut,
		readAt:         s.timeNow(),
	}

	return new(big.Int).Set(balance), new(big.Int).Set(alreadyPaidOut), nil
}

// RecoverCheque recovers the issuer ethereum address from a signed cheque
func RecoverCheque(cheque *SignedCheque, chaindID int64) (common.Address, error) {
	eip712Data := eip712DataForCheque(&cheque.Cheque, chaindID)

	pubkey, err := crypto.RecoverEIP712(cheque.Signature, eip712Data)
	if err != nil {
		return common.Address{}, err
	}

	ethAddr, err := crypto.NewEthereumAddress(*pubkey)
	if err != nil {
		return common.Address{}, err
	}

	var issuer common.Address
	copy(issuer[:], ethAddr)
	return issuer, nil
}

// keyChequebook computes the chequebook a store entry is for.
func keyChequebook(key []byte, prefix string) (chequebook common.Address, err error) {
	k := string(key)

	split := strings.SplitAfter(k, prefix)
	if len(split) != 2 {
		return common.Address{}, errors.New("no peer in key")
	}
	return common.HexToAddress(split[1]), nil
}

// LastCheques returns the last received cheques from every known chequebook.
func (s *chequeStore) LastCheques() (map[common.Address]*SignedCheque, error) {
	result := make(map[common.Address]*SignedCheque)
	err := s.store.Iterate(lastReceivedChequePrefix, func(key, val []byte) (stop bool, err error) {
		addr, err := keyChequebook(key, lastReceivedChequePrefix+"_")
		if err != nil {
			return false, fmt.Errorf("parse address from key: %s: %w", string(key), err)
		}

		if _, ok := result[addr]; !ok {
			lastCheque, err := s.LastCheque(addr)
			if err != nil && !errors.Is(err, ErrNoCheque) {
				return false, err
			} else if errors.Is(err, ErrNoCheque) {
				return false, nil
			}

			result[addr] = lastCheque
		}
		return false, nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
