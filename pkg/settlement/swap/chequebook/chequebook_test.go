// Copyright 2020 The Swarm Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package chequebook_test

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethersphere/bee/v2/pkg/settlement/swap/chequebook"
	erc20mock "github.com/ethersphere/bee/v2/pkg/settlement/swap/erc20/mock"
	storemock "github.com/ethersphere/bee/v2/pkg/statestore/mock"
	"github.com/ethersphere/bee/v2/pkg/storage"
	"github.com/ethersphere/bee/v2/pkg/transaction"
	transactionmock "github.com/ethersphere/bee/v2/pkg/transaction/mock"
)

func TestChequebookAddress(t *testing.T) {
	t.Parallel()

	address := common.HexToAddress("0xabcd")
	ownerAdress := common.HexToAddress("0xfff")
	chequebookService, err := chequebook.New(
		transactionmock.New(),
		address,
		ownerAdress,
		nil,
		&chequeSignerMock{},
		erc20mock.New(),
	)
	if err != nil {
		t.Fatal(err)
	}

	if chequebookService.Address() != address {
		t.Fatalf("returned wrong address. wanted %x, got %x", address, chequebookService.Address())
	}
}

func TestChequebookBalance(t *testing.T) {
	t.Parallel()

	address := common.HexToAddress("0xabcd")
	ownerAdress := common.HexToAddress("0xfff")
	balance := big.NewInt(10)
	chequebookService, err := chequebook.New(
		transactionmock.New(
			transactionmock.WithABICall(&chequebookABI, address, balance.FillBytes(make([]byte, 32)), "balance"),
		),
		address,
		ownerAdress,
		nil,
		&chequeSignerMock{},
		erc20mock.New(),
	)
	if err != nil {
		t.Fatal(err)
	}

	returnedBalance, err := chequebookService.Balance(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if returnedBalance.Cmp(balance) != 0 {
		t.Fatalf("returned wrong balance. wanted %d, got %d", balance, returnedBalance)
	}
}

func TestChequebookDeposit(t *testing.T) {
	t.Parallel()

	address := common.HexToAddress("0xabcd")
	ownerAdress := common.HexToAddress("0xfff")
	balance := big.NewInt(30)
	depositAmount := big.NewInt(20)
	txHash := common.HexToHash("0xdddd")
	chequebookService, err := chequebook.New(
		transactionmock.New(),
		address,
		ownerAdress,
		nil,
		&chequeSignerMock{},
		erc20mock.New(
			erc20mock.WithBalanceOfFunc(func(ctx context.Context, address common.Address) (*big.Int, error) {
				if address != ownerAdress {
					return nil, errors.New("getting balance of wrong address")
				}
				return balance, nil
			}),
			erc20mock.WithTransferFunc(func(ctx context.Context, to common.Address, value *big.Int) (common.Hash, error) {
				if to != address {
					return common.Hash{}, fmt.Errorf("sending to wrong address. wanted %x, got %x", address, to)
				}
				if depositAmount.Cmp(value) != 0 {
					return common.Hash{}, fmt.Errorf("sending wrong value. wanted %d, got %d", depositAmount, value)
				}
				return txHash, nil
			}),
		),
	)
	if err != nil {
		t.Fatal(err)
	}

	returnedTxHash, err := chequebookService.Deposit(context.Background(), depositAmount)
	if err != nil {
		t.Fatal(err)
	}

	if txHash != returnedTxHash {
		t.Fatalf("returned wrong transaction hash. wanted %v, got %v", txHash, returnedTxHash)
	}
}

func TestChequebookWaitForDeposit(t *testing.T) {
	t.Parallel()

	address := common.HexToAddress("0xabcd")
	ownerAdress := common.HexToAddress("0xfff")
	txHash := common.HexToHash("0xdddd")
	chequebookService, err := chequebook.New(
		transactionmock.New(
			transactionmock.WithWaitForReceiptFunc(func(ctx context.Context, tx common.Hash) (*types.Receipt, error) {
				if tx != txHash {
					t.Fatalf("waiting for wrong transaction. wanted %x, got %x", txHash, tx)
				}
				return &types.Receipt{
					Status: 1,
				}, nil
			}),
		),
		address,
		ownerAdress,
		nil,
		&chequeSignerMock{},
		erc20mock.New(),
	)
	if err != nil {
		t.Fatal(err)
	}

	err = chequebookService.WaitForDeposit(context.Background(), txHash)
	if err != nil {
		t.Fatal(err)
	}
}

func TestChequebookWaitForDepositReverted(t *testing.T) {
	t.Parallel()

	address := common.HexToAddress("0xabcd")
	ownerAdress := common.HexToAddress("0xfff")
	txHash := common.HexToHash("0xdddd")
	chequebookService, err := chequebook.New(
		transactionmock.New(
			transactionmock.WithWaitForReceiptFunc(func(ctx context.Context, tx common.Hash) (*types.Receipt, error) {
				if tx != txHash {
					t.Fatalf("waiting for wrong transaction. wanted %x, got %x", txHash, tx)
				}
				return &types.Receipt{
					Status: 0,
				}, nil
			}),
		),
		address,
		ownerAdress,
		nil,
		&chequeSignerMock{},
		erc20mock.New(),
	)
	if err != nil {
		t.Fatal(err)
	}

	err = chequebookService.WaitForDeposit(context.Background(), txHash)
	if err == nil {
		t.Fatal("expected reverted error")
	}
	if !errors.Is(err, transaction.ErrTransactionReverted) {
		t.Fatalf("wrong error. wanted %v, got %v", transaction.ErrTransactionReverted, err)
	}
}

func TestChequebookIssue(t *testing.T) {
	t.Parallel()

	address := common.HexToAddress("0xabcd")
	beneficiary := common.HexToAddress("0xdddd")
	ownerAdress := common.HexToAddress("0xfff")
	store := storemock.NewStateStore()
	amount := big.NewInt(20)
	amount2 := big.NewInt(30)
	expectedCumulative := big.NewInt(50)
	sig := common.Hex2Bytes("0xffff")
	chequeSigner := &chequeSignerMock{}

	chequebookService, err := chequebook.New(
		transactionmock.New(
			transactionmock.WithABICallSequence(
				transactionmock.ABICall(&chequebookABI, address, big.NewInt(100).FillBytes(make([]byte, 32)), "balance"),
				transactionmock.ABICall(&chequebookABI, address, big.NewInt(0).FillBytes(make([]byte, 32)), "totalPaidOut"),
				transactionmock.ABICall(&chequebookABI, address, big.NewInt(100).FillBytes(make([]byte, 32)), "balance"),
				transactionmock.ABICall(&chequebookABI, address, big.NewInt(0).FillBytes(make([]byte, 32)), "totalPaidOut"),
				transactionmock.ABICall(&chequebookABI, address, big.NewInt(100).FillBytes(make([]byte, 32)), "balance"),
				transactionmock.ABICall(&chequebookABI, address, big.NewInt(0).FillBytes(make([]byte, 32)), "totalPaidOut"),
			),
		),
		address,
		ownerAdress,
		store,
		chequeSigner,
		erc20mock.New(),
	)
	if err != nil {
		t.Fatal(err)
	}

	// issue a cheque
	expectedCheque := &chequebook.SignedCheque{
		Cheque: chequebook.Cheque{
			Beneficiary:      beneficiary,
			CumulativePayout: amount,
			Chequebook:       address,
		},
		Signature: sig,
	}

	chequeSigner.sign = func(cheque *chequebook.Cheque) ([]byte, error) {
		if !cheque.Equal(&expectedCheque.Cheque) {
			t.Fatalf("wrong cheque. wanted %v got %v", expectedCheque.Cheque, cheque)
		}
		return sig, nil
	}

	_, err = chequebookService.Issue(context.Background(), beneficiary, amount, func(cheque *chequebook.SignedCheque) error {
		if !cheque.Equal(expectedCheque) {
			t.Fatalf("wrong cheque. wanted %v got %v", expectedCheque, cheque)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	lastCheque, err := chequebookService.LastCheque(beneficiary)
	if err != nil {
		t.Fatal(err)
	}

	if !lastCheque.Equal(expectedCheque) {
		t.Fatalf("wrong cheque stored. wanted %v got %v", expectedCheque, lastCheque)
	}

	// issue another cheque for the same beneficiary
	expectedCheque = &chequebook.SignedCheque{
		Cheque: chequebook.Cheque{
			Beneficiary:      beneficiary,
			CumulativePayout: expectedCumulative,
			Chequebook:       address,
		},
		Signature: sig,
	}

	chequeSigner.sign = func(cheque *chequebook.Cheque) ([]byte, error) {
		if !cheque.Equal(&expectedCheque.Cheque) {
			t.Fatalf("wrong cheque. wanted %v got %v", expectedCheque, cheque)
		}
		return sig, nil
	}

	_, err = chequebookService.Issue(context.Background(), beneficiary, amount2, func(cheque *chequebook.SignedCheque) error {
		if !cheque.Equal(expectedCheque) {
			t.Fatalf("wrong cheque. wanted %v got %v", expectedCheque, cheque)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	lastCheque, err = chequebookService.LastCheque(beneficiary)
	if err != nil {
		t.Fatal(err)
	}

	if !lastCheque.Equal(expectedCheque) {
		t.Fatalf("wrong cheque stored. wanted %v got %v", expectedCheque, lastCheque)
	}

	// issue another cheque for the different beneficiary
	expectedChequeOwner := &chequebook.SignedCheque{
		Cheque: chequebook.Cheque{
			Beneficiary:      ownerAdress,
			CumulativePayout: amount,
			Chequebook:       address,
		},
		Signature: sig,
	}

	chequeSigner.sign = func(cheque *chequebook.Cheque) ([]byte, error) {
		if !cheque.Equal(&expectedChequeOwner.Cheque) {
			t.Fatalf("wrong cheque. wanted %v got %v", expectedCheque, cheque)
		}
		return sig, nil
	}

	_, err = chequebookService.Issue(context.Background(), ownerAdress, amount, func(cheque *chequebook.SignedCheque) error {
		if !cheque.Equal(expectedChequeOwner) {
			t.Fatalf("wrong cheque. wanted %v got %v", expectedChequeOwner, cheque)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	lastCheque, err = chequebookService.LastCheque(ownerAdress)
	if err != nil {
		t.Fatal(err)
	}

	if !lastCheque.Equal(expectedChequeOwner) {
		t.Fatalf("wrong cheque stored. wanted %v got %v", expectedChequeOwner, lastCheque)
	}

	// finally check this did not interfere with the beneficiary cheque
	lastCheque, err = chequebookService.LastCheque(beneficiary)
	if err != nil {
		t.Fatal(err)
	}

	if !lastCheque.Equal(expectedCheque) {
		t.Fatalf("wrong cheque stored. wanted %v got %v", expectedCheque, lastCheque)
	}
}

func TestChequebookIssueErrorSend(t *testing.T) {
	t.Parallel()

	address := common.HexToAddress("0xabcd")
	beneficiary := common.HexToAddress("0xdddd")
	ownerAdress := common.HexToAddress("0xfff")
	store := storemock.NewStateStore()
	amount := big.NewInt(20)
	sig := common.Hex2Bytes("0xffff")
	chequeSigner := &chequeSignerMock{}

	chequebookService, err := chequebook.New(
		transactionmock.New(),
		address,
		ownerAdress,
		store,
		chequeSigner,
		erc20mock.New(),
	)
	if err != nil {
		t.Fatal(err)
	}

	chequeSigner.sign = func(cheque *chequebook.Cheque) ([]byte, error) {
		return sig, nil
	}

	_, err = chequebookService.Issue(context.Background(), beneficiary, amount, func(cheque *chequebook.SignedCheque) error {
		return errors.New("err")
	})
	if err == nil {
		t.Fatal("expected error")
	}

	// verify the cheque was not saved
	_, err = chequebookService.LastCheque(beneficiary)
	if !errors.Is(err, chequebook.ErrNoCheque) {
		t.Fatalf("wrong error. wanted %v, got %v", chequebook.ErrNoCheque, err)
	}
}

func TestChequebookIssueOutOfFunds(t *testing.T) {
	t.Parallel()

	address := common.HexToAddress("0xabcd")
	beneficiary := common.HexToAddress("0xdddd")
	ownerAdress := common.HexToAddress("0xfff")
	store := storemock.NewStateStore()
	amount := big.NewInt(20)

	chequebookService, err := chequebook.New(
		transactionmock.New(
			transactionmock.WithABICallSequence(
				transactionmock.ABICall(&chequebookABI, address, big.NewInt(0).FillBytes(make([]byte, 32)), "balance"),
				transactionmock.ABICall(&chequebookABI, address, big.NewInt(0).FillBytes(make([]byte, 32)), "totalPaidOut"),
			),
		),
		address,
		ownerAdress,
		store,
		&chequeSignerMock{},
		erc20mock.New(),
	)
	if err != nil {
		t.Fatal(err)
	}

	_, err = chequebookService.Issue(context.Background(), beneficiary, amount, func(cheque *chequebook.SignedCheque) error {
		return nil
	})
	if !errors.Is(err, chequebook.ErrOutOfFunds) {
		t.Fatalf("wrong error. wanted %v, got %v", chequebook.ErrOutOfFunds, err)
	}

	// verify the cheque was not saved
	_, err = chequebookService.LastCheque(beneficiary)

	if !errors.Is(err, chequebook.ErrNoCheque) {
		t.Fatalf("wrong error. wanted %v, got %v", chequebook.ErrNoCheque, err)
	}
}

func TestChequebookWithdraw(t *testing.T) {
	t.Parallel()

	address := common.HexToAddress("0xabcd")
	ownerAdress := common.HexToAddress("0xfff")
	balance := big.NewInt(30)
	withdrawAmount := big.NewInt(20)
	txHash := common.HexToHash("0xdddd")
	store := storemock.NewStateStore()
	chequebookService, err := chequebook.New(
		transactionmock.New(
			transactionmock.WithABICallSequence(
				transactionmock.ABICall(&chequebookABI, address, balance.FillBytes(make([]byte, 32)), "balance"),
				transactionmock.ABICall(&chequebookABI, address, big.NewInt(0).FillBytes(make([]byte, 32)), "totalPaidOut"),
			),
			transactionmock.WithABISend(&chequebookABI, txHash, address, big.NewInt(0), "withdraw", withdrawAmount),
		),
		address,
		ownerAdress,
		store,
		&chequeSignerMock{},
		erc20mock.New(),
	)
	if err != nil {
		t.Fatal(err)
	}

	returnedTxHash, err := chequebookService.Withdraw(context.Background(), withdrawAmount)
	if err != nil {
		t.Fatal(err)
	}

	if txHash != returnedTxHash {
		t.Fatalf("returned wrong transaction hash. wanted %v, got %v", txHash, returnedTxHash)
	}
}

func TestChequebookWithdrawInsufficientFunds(t *testing.T) {
	t.Parallel()

	address := common.HexToAddress("0xabcd")
	ownerAdress := common.HexToAddress("0xfff")
	withdrawAmount := big.NewInt(20)
	txHash := common.HexToHash("0xdddd")
	store := storemock.NewStateStore()
	chequebookService, err := chequebook.New(
		transactionmock.New(
			transactionmock.WithABISend(&chequebookABI, txHash, address, big.NewInt(0), "withdraw", withdrawAmount),
			transactionmock.WithABICallSequence(
				transactionmock.ABICall(&chequebookABI, address, big.NewInt(0).FillBytes(make([]byte, 32)), "balance"),
				transactionmock.ABICall(&chequebookABI, address, big.NewInt(0).FillBytes(make([]byte, 32)), "totalPaidOut"),
			),
		),
		address,
		ownerAdress,
		store,
		&chequeSignerMock{},
		erc20mock.New(),
	)
	if err != nil {
		t.Fatal(err)
	}

	_, err = chequebookService.Withdraw(context.Background(), withdrawAmount)
	if !errors.Is(err, chequebook.ErrInsufficientFunds) {
		t.Fatalf("got wrong error. wanted %v, got %v", chequebook.ErrInsufficientFunds, err)
	}
}

func TestStateStoreKeys(t *testing.T) {
	t.Parallel()

	address := common.HexToAddress("0xabcd")

	expected := "swap_cashout_000000000000000000000000000000000000abcd"
	if chequebook.CashoutActionKey(address) != expected {
		t.Fatalf("wrong cashout action key. wanted %s, got %s", expected, chequebook.CashoutActionKey(address))
	}

	expected = "swap_chequebook_last_issued_cheque_000000000000000000000000000000000000abcd"
	if chequebook.LastIssuedChequeKey(address) != expected {
		t.Fatalf("wrong last issued cheque key. wanted %s, got %s", expected, chequebook.LastIssuedChequeKey(address))
	}

	expected = "swap_chequebook_last_received_cheque__000000000000000000000000000000000000abcd"
	if chequebook.LastReceivedChequeKey(address) != expected {
		t.Fatalf("wrong last received cheque key. wanted %s, got %s", expected, chequebook.LastReceivedChequeKey(address))
	}
}

// newConcurrentChequebook builds a chequebook whose chain calls answer in any
// order and any number of times, which WithABICallSequence cannot do: a
// concurrent test has no fixed call order.
func newConcurrentChequebook(t *testing.T, store storage.StateStorer) chequebook.Service {
	t.Helper()

	address := common.HexToAddress("0xabcd")
	balance := big.NewInt(1_000_000)

	svc, err := chequebook.New(
		transactionmock.New(
			transactionmock.WithCallFunc(func(_ context.Context, req *transaction.TxRequest) ([]byte, error) {
				method, err := chequebookABI.MethodById(req.Data[:4])
				if err != nil {
					return nil, err
				}
				switch method.Name {
				case "balance":
					return balance.FillBytes(make([]byte, 32)), nil
				case "totalPaidOut":
					return big.NewInt(0).FillBytes(make([]byte, 32)), nil
				}
				return nil, fmt.Errorf("unexpected call to %s", method.Name)
			}),
		),
		address,
		common.HexToAddress("0xfff"),
		store,
		&chequeSignerMock{sign: func(c *chequebook.Cheque) ([]byte, error) { return common.Hex2Bytes("0xffff"), nil }},
		erc20mock.New(),
	)
	if err != nil {
		t.Fatal(err)
	}

	return svc
}

// TestChequebookIssueConcurrentOneBeneficiary covers #317. Issue reads the last
// cheque, adds the amount, sends and persists. Without a lock held across that,
// two overlapping calls for one beneficiary read the same cumulative payout and
// send the same next value, and the later write can persist a value lower than
// what was sent. Every later cheque is then computed from it and repeats a
// payout the receiver has already credited.
//
// Not reachable through any caller today: settle gates payments per peer and two
// peers cannot share a beneficiary. This test is what stops that staying true by
// accident.
func TestChequebookIssueConcurrentOneBeneficiary(t *testing.T) {
	t.Parallel()

	const calls = 25

	beneficiary := common.HexToAddress("0xdddd")

	var mu sync.Mutex
	sent := make([]*big.Int, 0, calls)

	svc := newConcurrentChequebook(t, storemock.NewStateStore())

	var wg sync.WaitGroup
	for range calls {
		wg.Go(func() {
			_, err := svc.Issue(context.Background(), beneficiary, big.NewInt(10), func(c *chequebook.SignedCheque) error {
				// The send sits between the read of the cumulative payout
				// and the write of it, so a pause here is what makes the
				// unlocked window wide enough to be entered twice.
				//
				// Without it the test is machine dependent rather than
				// merely weaker: with the lock removed it caught nothing
				// at GOMAXPROCS=1, and failed about 15 times in 25 on a
				// 15-core machine. With the pause it catches the defect
				// 50 times out of 50 in both settings.
				time.Sleep(2 * time.Millisecond)

				mu.Lock()
				defer mu.Unlock()
				sent = append(sent, new(big.Int).Set(c.CumulativePayout))
				return nil
			})
			if err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()

	if len(sent) != calls {
		t.Fatalf("sent %d cheques, want %d", len(sent), calls)
	}

	// Every payout distinct, which is the property a receiver depends on: a
	// cheque that does not increase is not credited.
	seen := make(map[string]struct{}, len(sent))
	for _, p := range sent {
		if _, dup := seen[p.String()]; dup {
			t.Fatalf("cumulative payout %s was sent twice", p)
		}
		seen[p.String()] = struct{}{}
	}

	// The highest sent must be what is persisted. A lower persisted value is
	// the lasting damage: every later cheque would be computed from it.
	highest := big.NewInt(0)
	for _, p := range sent {
		if p.Cmp(highest) > 0 {
			highest = p
		}
	}
	if want := big.NewInt(int64(calls) * 10); highest.Cmp(want) != 0 {
		t.Fatalf("highest payout sent is %s, want %s", highest, want)
	}

	last, err := svc.LastCheque(beneficiary)
	if err != nil {
		t.Fatal(err)
	}
	if last.CumulativePayout.Cmp(highest) != 0 {
		t.Fatalf("persisted payout is %s, but %s was sent", last.CumulativePayout, highest)
	}
}

// TestChequebookIssueDifferentBeneficiariesNotSerialized checks the other half
// of #317: the lock is per beneficiary, not service-wide. A service-wide lock
// would also pass the test above while queueing every cheque this node issues
// behind every other.
//
// It discriminates by construction rather than by timing. Each send blocks
// until all of them have arrived, so the test can only finish if every call is
// inside its send at the same time. Under a service-wide lock the second call
// never reaches its send and the test fails on its own deadline rather than on
// a slow machine's.
func TestChequebookIssueDifferentBeneficiariesNotSerialized(t *testing.T) {
	t.Parallel()

	const beneficiaries = 4

	svc := newConcurrentChequebook(t, storemock.NewStateStore())

	arrived := make(chan struct{}, beneficiaries)
	release := make(chan struct{})

	var wg sync.WaitGroup

	// Released and awaited on every exit, including the t.Fatal below. Without
	// this, a failing run leaves every goroutine blocked on release forever:
	// harmless to the process, which exits anyway, but it leaves the failure
	// looking like a hang and would hide a later t.Error behind a panic for
	// logging after the test completed.
	var once sync.Once
	releaseAll := func() { once.Do(func() { close(release) }) }
	defer func() {
		releaseAll()
		wg.Wait()
	}()
	for i := range beneficiaries {
		wg.Go(func() {
			b := common.BigToAddress(big.NewInt(int64(i) + 1))
			_, err := svc.Issue(context.Background(), b, big.NewInt(10), func(*chequebook.SignedCheque) error {
				arrived <- struct{}{}
				<-release
				return nil
			})
			if err != nil {
				t.Error(err)
			}
		})
	}

	// All four must be inside their send before any is let go.
	for range beneficiaries {
		select {
		case <-arrived:
		case <-time.After(10 * time.Second):
			t.Fatal("not every beneficiary reached its send, so the lock is serializing peers that should be independent")
		}
	}

	releaseAll()
}

// TestChequebookIssueReleasesLockOnFailure covers the mutation the two tests
// above do not: unlocking only on the success path instead of with defer.
//
// That is worse than the defect being fixed. A failed Issue would leave the
// beneficiary's mutex held forever, so every later payment to that peer would
// block, and a send failure is an ordinary event rather than a rare one.
//
// The existing error-path tests miss it because each builds a fresh service and
// issues once, so nothing ever takes the lock a second time.
func TestChequebookIssueReleasesLockOnFailure(t *testing.T) {
	t.Parallel()

	beneficiary := common.HexToAddress("0xdddd")
	svc := newConcurrentChequebook(t, storemock.NewStateStore())

	sendFailed := errors.New("send failed")
	_, err := svc.Issue(context.Background(), beneficiary, big.NewInt(10), func(*chequebook.SignedCheque) error {
		return sendFailed
	})
	if !errors.Is(err, sendFailed) {
		t.Fatalf("first Issue returned %v, want the send failure", err)
	}

	// The second call must not block on a lock the first never gave back.
	done := make(chan error, 1)
	go func() {
		_, err := svc.Issue(context.Background(), beneficiary, big.NewInt(10), func(*chequebook.SignedCheque) error {
			return nil
		})
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("second Issue returned %v, want nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the second Issue for this beneficiary never returned: the lock was not released when the first one failed")
	}
}
