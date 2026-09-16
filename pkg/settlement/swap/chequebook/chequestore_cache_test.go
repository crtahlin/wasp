// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package chequebook_test

import (
	"context"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethersphere/bee/v2/pkg/settlement/swap/chequebook"
	storemock "github.com/ethersphere/bee/v2/pkg/statestore/mock"
	transactionmock "github.com/ethersphere/bee/v2/pkg/transaction/mock"
)

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

// TestReceiveChequeStoredIssuerZero checks that an issuer entry of all zeroes,
// which a corrupt state store can return without an error, counts as absent. A
// node that took it at face value would reject every later cheque from that
// chequebook.
func TestReceiveChequeStoredIssuerZero(t *testing.T) {
	t.Parallel()

	store := storemock.NewStateStore()
	beneficiary := common.HexToAddress("0xffff")
	issuer := common.HexToAddress("0xbeee")
	chequebookAddress := common.HexToAddress("0xeeee")
	cumulativePayout := big.NewInt(101)
	chainID := int64(1)

	if err := store.Put(chequebook.IssuerKey(chequebookAddress), common.Address{}); err != nil {
		t.Fatal(err)
	}

	chequestore := chequebook.NewChequeStore(
		store,
		acceptingFactory(),
		chainID,
		beneficiary,
		transactionmock.New(
			transactionmock.WithABICallSequence(
				transactionmock.ABICall(&chequebookABI, chequebookAddress, common.BytesToHash(issuer.Bytes()).Bytes(), "issuer"),
				transactionmock.ABICall(&chequebookABI, chequebookAddress, cumulativePayout.FillBytes(make([]byte, 32)), "balance"),
				transactionmock.ABICall(&chequebookABI, chequebookAddress, big.NewInt(0).FillBytes(make([]byte, 32)), "paidOut", beneficiary),
			),
		),
		func(c *chequebook.SignedCheque, cid int64) (common.Address, error) {
			return issuer, nil
		})

	cheque := newTestCheque(beneficiary, chequebookAddress, cumulativePayout)
	if _, err := chequestore.ReceiveCheque(context.Background(), cheque, big.NewInt(10), big.NewInt(1)); err != nil {
		t.Fatal(err)
	}

	var stored common.Address
	if err := store.Get(chequebook.IssuerKey(chequebookAddress), &stored); err != nil {
		t.Fatal(err)
	}
	if stored != issuer {
		t.Fatalf("stored wrong issuer. wanted %x, got %x", issuer, stored)
	}
}

// TestReceiveChequeLiquidityRead checks that the balance and paid out total are
// read again once their period has passed, and not before.
func TestReceiveChequeLiquidityRead(t *testing.T) {
	t.Parallel()

	store := storemock.NewStateStore()
	beneficiary := common.HexToAddress("0xffff")
	issuer := common.HexToAddress("0xbeee")
	chequebookAddress := common.HexToAddress("0xeeee")
	payout1 := big.NewInt(101)
	payout2 := big.NewInt(201)
	payout3 := big.NewInt(301)
	balance := big.NewInt(1000)
	chainID := int64(1)

	chequestore := chequebook.NewChequeStore(
		store,
		acceptingFactory(),
		chainID,
		beneficiary,
		transactionmock.New(
			transactionmock.WithABICallSequence(
				// first cheque: issuer, then the liquidity values
				transactionmock.ABICall(&chequebookABI, chequebookAddress, common.BytesToHash(issuer.Bytes()).Bytes(), "issuer"),
				transactionmock.ABICall(&chequebookABI, chequebookAddress, balance.FillBytes(make([]byte, 32)), "balance"),
				transactionmock.ABICall(&chequebookABI, chequebookAddress, big.NewInt(0).FillBytes(make([]byte, 32)), "paidOut", beneficiary),
				// third cheque, after the period: the liquidity values again,
				// but not the issuer
				transactionmock.ABICall(&chequebookABI, chequebookAddress, balance.FillBytes(make([]byte, 32)), "balance"),
				transactionmock.ABICall(&chequebookABI, chequebookAddress, big.NewInt(0).FillBytes(make([]byte, 32)), "paidOut", beneficiary),
			),
		),
		func(c *chequebook.SignedCheque, cid int64) (common.Address, error) {
			return issuer, nil
		})

	now := time.Now()
	chequebook.SetChequeStoreTimeNow(chequestore, func() time.Time { return now })

	ctx := context.Background()
	if _, err := chequestore.ReceiveCheque(ctx, newTestCheque(beneficiary, chequebookAddress, payout1), big.NewInt(10), big.NewInt(1)); err != nil {
		t.Fatal(err)
	}

	// still inside the period: no chain call, so the sequence must not advance
	if _, err := chequestore.ReceiveCheque(ctx, newTestCheque(beneficiary, chequebookAddress, payout2), big.NewInt(10), big.NewInt(1)); err != nil {
		t.Fatal(err)
	}

	now = now.Add(chequebook.ChequeLiquidityValidity + time.Second)

	if _, err := chequestore.ReceiveCheque(ctx, newTestCheque(beneficiary, chequebookAddress, payout3), big.NewInt(10), big.NewInt(1)); err != nil {
		t.Fatal(err)
	}
}

// TestReceiveChequeBouncingFromStoredLiquidity checks that a cheque above what
// the stored values say the chequebook can pay is still refused.
func TestReceiveChequeBouncingFromStoredLiquidity(t *testing.T) {
	t.Parallel()

	store := storemock.NewStateStore()
	beneficiary := common.HexToAddress("0xffff")
	issuer := common.HexToAddress("0xbeee")
	chequebookAddress := common.HexToAddress("0xeeee")
	payout1 := big.NewInt(101)
	tooMuch := big.NewInt(1001)
	balance := big.NewInt(500)
	chainID := int64(1)

	chequestore := chequebook.NewChequeStore(
		store,
		acceptingFactory(),
		chainID,
		beneficiary,
		transactionmock.New(
			transactionmock.WithABICallSequence(
				transactionmock.ABICall(&chequebookABI, chequebookAddress, common.BytesToHash(issuer.Bytes()).Bytes(), "issuer"),
				transactionmock.ABICall(&chequebookABI, chequebookAddress, balance.FillBytes(make([]byte, 32)), "balance"),
				transactionmock.ABICall(&chequebookABI, chequebookAddress, big.NewInt(0).FillBytes(make([]byte, 32)), "paidOut", beneficiary),
			),
		),
		func(c *chequebook.SignedCheque, cid int64) (common.Address, error) {
			return issuer, nil
		})

	ctx := context.Background()
	if _, err := chequestore.ReceiveCheque(ctx, newTestCheque(beneficiary, chequebookAddress, payout1), big.NewInt(10), big.NewInt(1)); err != nil {
		t.Fatal(err)
	}

	_, err := chequestore.ReceiveCheque(ctx, newTestCheque(beneficiary, chequebookAddress, tooMuch), big.NewInt(10), big.NewInt(1))
	if err == nil {
		t.Fatal("accepted a cheque above the chequebook's balance")
	}
}

// TestIssuerKeyNotACheque checks that the issuer entry is not picked up by
// LastCheques, which reads an address out of every key under its own prefix.
func TestIssuerKeyNotACheque(t *testing.T) {
	t.Parallel()

	store := storemock.NewStateStore()
	beneficiary := common.HexToAddress("0xffff")
	issuer := common.HexToAddress("0xbeee")
	chequebookAddress := common.HexToAddress("0xeeee")
	cumulativePayout := big.NewInt(101)
	chainID := int64(1)

	chequestore := chequebook.NewChequeStore(
		store,
		acceptingFactory(),
		chainID,
		beneficiary,
		transactionmock.New(
			transactionmock.WithABICallSequence(
				transactionmock.ABICall(&chequebookABI, chequebookAddress, common.BytesToHash(issuer.Bytes()).Bytes(), "issuer"),
				transactionmock.ABICall(&chequebookABI, chequebookAddress, cumulativePayout.FillBytes(make([]byte, 32)), "balance"),
				transactionmock.ABICall(&chequebookABI, chequebookAddress, big.NewInt(0).FillBytes(make([]byte, 32)), "paidOut", beneficiary),
			),
		),
		func(c *chequebook.SignedCheque, cid int64) (common.Address, error) {
			return issuer, nil
		})

	cheque := newTestCheque(beneficiary, chequebookAddress, cumulativePayout)
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
