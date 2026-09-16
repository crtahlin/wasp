// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package chequebook_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethersphere/bee/v2/pkg/settlement/swap/chequebook"
	storemock "github.com/ethersphere/bee/v2/pkg/statestore/mock"
	"github.com/ethersphere/bee/v2/pkg/storage"
	"github.com/ethersphere/bee/v2/pkg/transaction"
	transactionmock "github.com/ethersphere/bee/v2/pkg/transaction/mock"
)

// expectedCall is one chain call a test allows, and what it answers.
type expectedCall struct {
	to     common.Address
	method string
	params []any
	result []byte
}

// callCounter reports how many chain calls a cheque store has made.
type callCounter struct{ n int }

// countingCalls answers the given chain calls in order and counts them. The
// mock the rest of this package uses does not say how many of its expected
// calls were made, so a test could not tell a missing call from a cached one.
func countingCalls(t *testing.T, counter *callCounter, calls ...expectedCall) transactionmock.Option {
	t.Helper()

	return transactionmock.WithCallFunc(func(ctx context.Context, request *transaction.TxRequest) ([]byte, error) {
		if counter.n >= len(calls) {
			return nil, fmt.Errorf("chain call %d was not expected", counter.n+1)
		}

		call := calls[counter.n]

		data, err := chequebookABI.Pack(call.method, call.params...)
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(data, request.Data) {
			return nil, fmt.Errorf("chain call %d is not %s", counter.n+1, call.method)
		}
		if request.To == nil {
			return nil, errors.New("chain call with no recipient")
		}
		if *request.To != call.to {
			return nil, fmt.Errorf("chain call %d to the wrong contract", counter.n+1)
		}

		counter.n++
		return call.result, nil
	})
}

func padded(v *big.Int) []byte {
	return v.FillBytes(make([]byte, 32))
}

func issuerResult(issuer common.Address) []byte {
	return common.BytesToHash(issuer.Bytes()).Bytes()
}

// newTestCheque returns a cheque from chequebookAddress to beneficiary.
func newTestCheque(beneficiary, chequebookAddress common.Address, cumulativePayout *big.Int) *chequebook.SignedCheque {
	return &chequebook.SignedCheque{
		Cheque: chequebook.Cheque{
			Beneficiary:      beneficiary,
			CumulativePayout: cumulativePayout,
			Chequebook:       chequebookAddress,
		},
		Signature: make([]byte, 65),
	}
}

// acceptingFactory verifies every chequebook it is asked about.
func acceptingFactory() *factoryMock {
	return &factoryMock{
		verifyChequebook: func(ctx context.Context, address common.Address) error {
			return nil
		},
	}
}

// TestReceiveChequeStoredIssuer checks that the issuer is read from the chain
// once, and that an entry this node cannot use counts as absent: all zeroes,
// which a corrupt store can return without an error, and a value that is not an
// address at all. Taking either at face value would reject every later cheque
// from that chequebook, since nothing would write the entry again.
func TestReceiveChequeStoredIssuer(t *testing.T) {
	t.Parallel()

	beneficiary := common.HexToAddress("0xffff")
	issuer := common.HexToAddress("0xbeee")
	chequebookAddress := common.HexToAddress("0xeeee")
	payout := big.NewInt(101)
	balance := big.NewInt(1000)

	for _, tc := range []struct {
		name  string
		store func(t *testing.T) storage.StateStorer
	}{
		{
			name:  "no entry",
			store: func(t *testing.T) storage.StateStorer { t.Helper(); return storemock.NewStateStore() },
		},
		{
			name: "zero entry",
			store: func(t *testing.T) storage.StateStorer {
				t.Helper()
				s := storemock.NewStateStore()
				if err := s.Put(chequebook.IssuerKey(chequebookAddress), common.Address{}); err != nil {
					t.Fatal(err)
				}
				return s
			},
		},
		{
			name: "entry that is not an address",
			store: func(t *testing.T) storage.StateStorer {
				t.Helper()
				s := storemock.NewStateStore()
				if err := s.Put(chequebook.IssuerKey(chequebookAddress), "not an address"); err != nil {
					t.Fatal(err)
				}
				return s
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			store := tc.store(t)
			counter := &callCounter{}

			chequestore := chequebook.NewChequeStore(
				store,
				acceptingFactory(),
				int64(1),
				beneficiary,
				transactionmock.New(countingCalls(t, counter,
					expectedCall{chequebookAddress, "issuer", nil, issuerResult(issuer)},
					expectedCall{chequebookAddress, "balance", nil, padded(balance)},
					expectedCall{chequebookAddress, "paidOut", []any{beneficiary}, padded(big.NewInt(0))},
				)),
				func(c *chequebook.SignedCheque, cid int64) (common.Address, error) {
					return issuer, nil
				})

			cheque := newTestCheque(beneficiary, chequebookAddress, payout)
			if _, err := chequestore.ReceiveCheque(context.Background(), cheque, big.NewInt(10), big.NewInt(1)); err != nil {
				t.Fatal(err)
			}

			if counter.n != 3 {
				t.Fatalf("wrong number of chain calls. wanted 3, got %d", counter.n)
			}

			var stored common.Address
			if err := store.Get(chequebook.IssuerKey(chequebookAddress), &stored); err != nil {
				t.Fatal(err)
			}
			if stored != issuer {
				t.Fatalf("stored wrong issuer. wanted %x, got %x", issuer, stored)
			}
		})
	}
}

// TestReceiveChequeLiquidityRead checks that the balance and paid out total are
// read again once their period has passed, and not before. The call count is
// checked after every cheque, so a cache that never expires fails here.
func TestReceiveChequeLiquidityRead(t *testing.T) {
	t.Parallel()

	beneficiary := common.HexToAddress("0xffff")
	issuer := common.HexToAddress("0xbeee")
	chequebookAddress := common.HexToAddress("0xeeee")
	balance := big.NewInt(1000)
	counter := &callCounter{}

	chequestore := chequebook.NewChequeStore(
		storemock.NewStateStore(),
		acceptingFactory(),
		int64(1),
		beneficiary,
		transactionmock.New(countingCalls(t, counter,
			// first cheque: the issuer, then the liquidity values
			expectedCall{chequebookAddress, "issuer", nil, issuerResult(issuer)},
			expectedCall{chequebookAddress, "balance", nil, padded(balance)},
			expectedCall{chequebookAddress, "paidOut", []any{beneficiary}, padded(big.NewInt(0))},
			// third cheque, after the period: the liquidity values again, and
			// never the issuer
			expectedCall{chequebookAddress, "balance", nil, padded(balance)},
			expectedCall{chequebookAddress, "paidOut", []any{beneficiary}, padded(big.NewInt(0))},
		)),
		func(c *chequebook.SignedCheque, cid int64) (common.Address, error) {
			return issuer, nil
		})

	now := time.Now()
	chequebook.SetChequeStoreTimeNow(chequestore, func() time.Time { return now })

	ctx := context.Background()
	receive := func(payout int64) {
		t.Helper()
		cheque := newTestCheque(beneficiary, chequebookAddress, big.NewInt(payout))
		if _, err := chequestore.ReceiveCheque(ctx, cheque, big.NewInt(10), big.NewInt(1)); err != nil {
			t.Fatal(err)
		}
	}

	receive(101)
	if counter.n != 3 {
		t.Fatalf("first cheque: wanted 3 chain calls, got %d", counter.n)
	}

	receive(201)
	if counter.n != 3 {
		t.Fatalf("second cheque, inside the period: wanted no further chain call, got %d in total", counter.n)
	}

	now = now.Add(chequebook.ChequeLiquidityValidity + time.Second)

	receive(301)
	if counter.n != 5 {
		t.Fatalf("third cheque, after the period: wanted 5 chain calls in total, got %d", counter.n)
	}
}

// TestReceiveChequeSecondChequebook checks that what is stored for one
// chequebook is not used for another.
func TestReceiveChequeSecondChequebook(t *testing.T) {
	t.Parallel()

	beneficiary := common.HexToAddress("0xffff")
	issuerOne := common.HexToAddress("0xbee1")
	issuerTwo := common.HexToAddress("0xbee2")
	chequebookOne := common.HexToAddress("0xeee1")
	chequebookTwo := common.HexToAddress("0xeee2")
	payout := big.NewInt(101)
	balance := big.NewInt(1000)
	counter := &callCounter{}

	issuerOf := map[common.Address]common.Address{
		chequebookOne: issuerOne,
		chequebookTwo: issuerTwo,
	}

	chequestore := chequebook.NewChequeStore(
		storemock.NewStateStore(),
		acceptingFactory(),
		int64(1),
		beneficiary,
		transactionmock.New(countingCalls(t, counter,
			expectedCall{chequebookOne, "issuer", nil, issuerResult(issuerOne)},
			expectedCall{chequebookOne, "balance", nil, padded(balance)},
			expectedCall{chequebookOne, "paidOut", []any{beneficiary}, padded(big.NewInt(0))},
			expectedCall{chequebookTwo, "issuer", nil, issuerResult(issuerTwo)},
			expectedCall{chequebookTwo, "balance", nil, padded(balance)},
			expectedCall{chequebookTwo, "paidOut", []any{beneficiary}, padded(big.NewInt(0))},
		)),
		func(c *chequebook.SignedCheque, cid int64) (common.Address, error) {
			return issuerOf[c.Chequebook], nil
		})

	ctx := context.Background()
	for _, chequebookAddress := range []common.Address{chequebookOne, chequebookTwo} {
		cheque := newTestCheque(beneficiary, chequebookAddress, payout)
		if _, err := chequestore.ReceiveCheque(ctx, cheque, big.NewInt(10), big.NewInt(1)); err != nil {
			t.Fatal(err)
		}
	}

	if counter.n != 6 {
		t.Fatalf("wrong number of chain calls. wanted 6, got %d", counter.n)
	}
}

// TestReceiveChequeBouncingFromStoredLiquidity checks that a cheque above what
// the stored values say the chequebook can pay is still refused.
func TestReceiveChequeBouncingFromStoredLiquidity(t *testing.T) {
	t.Parallel()

	beneficiary := common.HexToAddress("0xffff")
	issuer := common.HexToAddress("0xbeee")
	chequebookAddress := common.HexToAddress("0xeeee")
	balance := big.NewInt(500)
	counter := &callCounter{}

	chequestore := chequebook.NewChequeStore(
		storemock.NewStateStore(),
		acceptingFactory(),
		int64(1),
		beneficiary,
		transactionmock.New(countingCalls(t, counter,
			expectedCall{chequebookAddress, "issuer", nil, issuerResult(issuer)},
			expectedCall{chequebookAddress, "balance", nil, padded(balance)},
			expectedCall{chequebookAddress, "paidOut", []any{beneficiary}, padded(big.NewInt(0))},
		)),
		func(c *chequebook.SignedCheque, cid int64) (common.Address, error) {
			return issuer, nil
		})

	ctx := context.Background()
	if _, err := chequestore.ReceiveCheque(ctx, newTestCheque(beneficiary, chequebookAddress, big.NewInt(101)), big.NewInt(10), big.NewInt(1)); err != nil {
		t.Fatal(err)
	}

	_, err := chequestore.ReceiveCheque(ctx, newTestCheque(beneficiary, chequebookAddress, big.NewInt(1001)), big.NewInt(10), big.NewInt(1))
	if !errors.Is(err, chequebook.ErrBouncingCheque) {
		t.Fatalf("wanted ErrBouncingCheque, got %v", err)
	}
}

// TestIssuerKeyNotACheque checks that the issuer entry is not picked up by
// LastCheques, which reads an address out of every key under its own prefix.
func TestIssuerKeyNotACheque(t *testing.T) {
	t.Parallel()

	beneficiary := common.HexToAddress("0xffff")
	issuer := common.HexToAddress("0xbeee")
	chequebookAddress := common.HexToAddress("0xeeee")
	payout := big.NewInt(101)
	counter := &callCounter{}

	chequestore := chequebook.NewChequeStore(
		storemock.NewStateStore(),
		acceptingFactory(),
		int64(1),
		beneficiary,
		transactionmock.New(countingCalls(t, counter,
			expectedCall{chequebookAddress, "issuer", nil, issuerResult(issuer)},
			expectedCall{chequebookAddress, "balance", nil, padded(payout)},
			expectedCall{chequebookAddress, "paidOut", []any{beneficiary}, padded(big.NewInt(0))},
		)),
		func(c *chequebook.SignedCheque, cid int64) (common.Address, error) {
			return issuer, nil
		})

	cheque := newTestCheque(beneficiary, chequebookAddress, payout)
	if _, err := chequestore.ReceiveCheque(context.Background(), cheque, big.NewInt(10), big.NewInt(1)); err != nil {
		t.Fatal(err)
	}

	cheques, err := chequestore.LastCheques()
	if err != nil {
		t.Fatal(err)
	}

	if len(cheques) != 1 {
		t.Fatalf("wrong number of cheques. wanted 1, got %d", len(cheques))
	}
	if _, ok := cheques[chequebookAddress]; !ok {
		t.Fatalf("missing the cheque of %x", chequebookAddress)
	}
}
