// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package postagecontract_test

import (
	"bytes"
	"context"
	"errors"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/ethersphere/bee/v2/pkg/crypto"
	"github.com/ethersphere/bee/v2/pkg/log"
	"github.com/ethersphere/bee/v2/pkg/postage"
	postagestoreMock "github.com/ethersphere/bee/v2/pkg/postage/batchstore/mock"
	postageMock "github.com/ethersphere/bee/v2/pkg/postage/mock"
	"github.com/ethersphere/bee/v2/pkg/postage/postagecontract"
	storemock "github.com/ethersphere/bee/v2/pkg/statestore/mock"
	"github.com/ethersphere/bee/v2/pkg/transaction"
	"github.com/ethersphere/bee/v2/pkg/transaction/backendmock"
	"github.com/ethersphere/bee/v2/pkg/transaction/monitormock"
	"github.com/ethersphere/bee/v2/pkg/util/testutil"
)

// http429 is what both rate-limited endpoints answered to the approve's
// eth_sendRawTransaction on a Sepolia test node (#802).
var http429 = rpc.HTTPError{StatusCode: http.StatusTooManyRequests, Status: "429 Too Many Requests"}

// refusalError is a JSON-RPC error answer: the endpoint read the transaction and
// refused it.
type refusalError struct{ msg string }

func (r refusalError) Error() string  { return r.msg }
func (r refusalError) ErrorCode() int { return -32000 }

var _ rpc.Error = refusalError{}

// chainFixture runs CreateBatch on a real transaction service over a fake
// chain: sends, calls and receipts are answered by the fixture, so the
// service's own handling of a failed send (stored, nonce kept, re-broadcast)
// is what the contract code sees.
type chainFixture struct {
	postageStamp, bzzToken common.Address
	batchID                common.Hash

	approveSendErr error  // returned for the first approve send
	createSendErr  error  // returned for the first createBatch send
	approveStatus  uint64 // receipt status of the approve
	approveMined   bool   // whether the approve gets a receipt at all

	mu   sync.Mutex
	sent []*types.Transaction
	log  bytes.Buffer
}

// logger writes the transaction service's warnings to f.log.
func (f *chainFixture) logger() log.Logger {
	return log.NewLogger("test", log.WithSink(&lockedWriter{mu: &f.mu, w: &f.log}), log.WithVerbosity(log.VerbosityWarning))
}

func (f *chainFixture) logged() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.log.String()
}

// lockedWriter serialises writes to w with mu.
type lockedWriter struct {
	mu *sync.Mutex
	w  *bytes.Buffer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func newChainFixture() *chainFixture {
	return &chainFixture{
		postageStamp:  common.HexToAddress("ffff"),
		bzzToken:      common.HexToAddress("eeee"),
		batchID:       common.HexToHash("dddd"),
		approveStatus: types.ReceiptStatusSuccessful,
		approveMined:  true,
	}
}

func (f *chainFixture) sentTo(to common.Address) []*types.Transaction {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*types.Transaction
	for _, tx := range f.sent {
		if *tx.To() == to {
			out = append(out, tx)
		}
	}
	return out
}

// distinctSentTo counts the different transactions sent to to; a
// re-broadcast of the same signed bytes counts once.
func (f *chainFixture) distinctSentTo(to common.Address) int {
	seen := map[common.Hash]bool{}
	for _, tx := range f.sentTo(to) {
		seen[tx.Hash()] = true
	}
	return len(seen)
}

func (f *chainFixture) byHash(h common.Hash) *types.Transaction {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, tx := range f.sent {
		if tx.Hash() == h {
			return tx
		}
	}
	return nil
}

// receiptFor is the receipt a mined transaction gets, or nil while it has
// none.
func (f *chainFixture) receiptFor(h common.Hash) *types.Receipt {
	tx := f.byHash(h)
	if tx == nil {
		return nil
	}
	switch *tx.To() {
	case f.bzzToken:
		if !f.approveMined {
			return nil
		}
		return &types.Receipt{TxHash: h, Status: f.approveStatus}
	case f.postageStamp:
		return &types.Receipt{
			TxHash: h, Status: types.ReceiptStatusSuccessful,
			Logs: []*types.Log{newCreateEvent(f.postageStamp, f.batchID)},
		}
	}
	return nil
}

func (f *chainFixture) contract(t *testing.T) (postagecontract.Interface, postage.Service) {
	t.Helper()
	b := postagecontract.BucketDepth
	postagecontract.BucketDepth = 9
	t.Cleanup(func() { postagecontract.BucketDepth = b })

	key, err := crypto.GenerateSecp256k1Key()
	if err != nil {
		t.Fatal(err)
	}
	signer := crypto.NewDefaultSigner(key)
	owner, err := signer.EthereumAddress()
	if err != nil {
		t.Fatal(err)
	}

	lastPrice, err := postageStampContractABI.Pack("lastPrice")
	if err != nil {
		t.Fatal(err)
	}
	minValidity, err := postageStampContractABI.Pack("minimumValidityBlocks")
	if err != nil {
		t.Fatal(err)
	}
	expiredExist, err := postageStampContractABI.Pack("expiredBatchesExist")
	if err != nil {
		t.Fatal(err)
	}

	backend := backendmock.New(
		backendmock.WithSendTransactionFunc(func(_ context.Context, tx *types.Transaction) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			first := true
			for _, s := range f.sent {
				if s.Hash() == tx.Hash() {
					first = false
				}
			}
			f.sent = append(f.sent, tx)
			switch {
			case *tx.To() == f.bzzToken && first:
				return f.approveSendErr
			case *tx.To() == f.postageStamp && first:
				return f.createSendErr
			}
			return nil
		}),
		backendmock.WithCallContractFunc(func(_ context.Context, msg ethereum.CallMsg, _ *big.Int) ([]byte, error) {
			switch {
			case *msg.To == f.bzzToken:
				return big.NewInt(1_000_000_000).FillBytes(make([]byte, 32)), nil
			case bytes.Equal(msg.Data, lastPrice):
				return big.NewInt(2).FillBytes(make([]byte, 32)), nil
			case bytes.Equal(msg.Data, minValidity):
				return big.NewInt(25).FillBytes(make([]byte, 32)), nil
			case bytes.Equal(msg.Data, expiredExist):
				return big.NewInt(0).FillBytes(make([]byte, 32)), nil
			}
			return nil, errors.New("unexpected call")
		}),
		backendmock.WithEstimateGasFunc(func(context.Context, ethereum.CallMsg) (uint64, error) {
			return 50_000, nil
		}),
		backendmock.WithPendingNonceAtFunc(func(context.Context, common.Address) (uint64, error) {
			return 0, nil
		}),
		backendmock.WithSuggestedFeeAndTipFunc(func(context.Context, *big.Int, int) (*big.Int, *big.Int, error) {
			return big.NewInt(10_000_000_000), big.NewInt(1_000_000_000), nil
		}),
		backendmock.WithTransactionByHashFunc(func(context.Context, common.Hash) (*types.Transaction, bool, error) {
			return nil, false, ethereum.NotFound
		}),
	)

	// A transaction is mined a little after it is watched, so a receipt for
	// an approve whose send failed arrives after the send has returned, as
	// on the test node, where it was mined 30 to 60 s later.
	monitor := monitormock.New(
		monitormock.WithWatchTransactionFunc(func(txHash common.Hash, _ uint64) (<-chan types.Receipt, <-chan error, error) {
			receiptC := make(chan types.Receipt, 1)
			time.AfterFunc(50*time.Millisecond, func() {
				if r := f.receiptFor(txHash); r != nil {
					receiptC <- *r
				}
			})
			return receiptC, make(chan error, 1), nil
		}),
	)

	service, err := transaction.NewService(f.logger(), owner, backend, signer, storemock.NewStateStore(), big.NewInt(5), monitor, 0)
	if err != nil {
		t.Fatal(err)
	}
	testutil.CleanupCloser(t, service)

	postages := postageMock.New()
	return postagecontract.New(owner, f.postageStamp, postageStampContractABI, f.bzzToken,
		service, postages, postagestoreMock.New(), true, 0), postages
}

// createBatch runs CreateBatch with its own deadline, so that a wait which
// should not happen fails the test with a message instead of hanging it.
func createBatch(t *testing.T, c postagecontract.Interface) ([]byte, error) {
	t.Helper()
	id, err := createBatchWithin(c, 5*time.Second)
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("CreateBatch did not return within its 5 s deadline: %v", err)
	}
	return id, err
}

func createBatchWithin(c postagecontract.Interface, d time.Duration) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	_, id, err := c.CreateBatch(ctx, big.NewInt(100), 10, false, "label")
	return id, err
}

// TestCreateBatchAfterAmbiguousApprove is the failure seen on a Sepolia test
// node (#802): both endpoints answer the approve's send with 429, the
// approve is mined later anyway, and the batch must still be created.
func TestCreateBatchAfterAmbiguousApprove(t *testing.T) {
	f := newChainFixture()
	f.approveSendErr = http429
	c, postages := f.contract(t)

	id, err := createBatch(t, c)
	if err != nil {
		t.Fatalf("CreateBatch: %v, want the batch created once the approve is mined", err)
	}
	if !bytes.Equal(id, f.batchID[:]) {
		t.Fatalf("batch id %x, want %x", id, f.batchID)
	}
	if n := f.distinctSentTo(f.postageStamp); n != 1 {
		t.Fatalf("%d createBatch transactions sent, want 1", n)
	}
	if si, _, err := postages.GetStampIssuer(id); err != nil || si == nil {
		t.Fatalf("stamp issuer for the new batch: %v, %v", si, err)
	}
}

// TestCreateBatchAmbiguousApproveReverted asserts the approve's receipt
// decides: an approve whose send failed and which is then mined with a
// revert still fails the purchase, and no createBatch is sent.
func TestCreateBatchAmbiguousApproveReverted(t *testing.T) {
	f := newChainFixture()
	f.approveSendErr = http429
	f.approveStatus = types.ReceiptStatusFailed
	c, _ := f.contract(t)

	_, err := createBatch(t, c)
	if !errors.Is(err, transaction.ErrTransactionReverted) {
		t.Fatalf("CreateBatch: %v, want %v", err, transaction.ErrTransactionReverted)
	}
	if n := len(f.sentTo(f.postageStamp)); n != 0 {
		t.Fatalf("%d createBatch transactions sent after a reverted approve, want 0", n)
	}
}

// TestCreateBatchAfterAmbiguousCreate is the second transaction's form of
// #802: the approve is sent normally, the createBatch send answers 429 and
// is mined later. The batch must be recorded, and createBatch sent once.
func TestCreateBatchAfterAmbiguousCreate(t *testing.T) {
	f := newChainFixture()
	f.createSendErr = http429
	c, postages := f.contract(t)

	id, err := createBatch(t, c)
	if err != nil {
		t.Fatalf("CreateBatch: %v, want the batch recorded once createBatch is mined", err)
	}
	if !bytes.Equal(id, f.batchID[:]) {
		t.Fatalf("batch id %x, want %x", id, f.batchID)
	}
	if n := f.distinctSentTo(f.postageStamp); n != 1 {
		t.Fatalf("%d createBatch transactions sent, want 1", n)
	}
	if si, _, err := postages.GetStampIssuer(id); err != nil || si == nil {
		t.Fatalf("stamp issuer for the new batch: %v, %v", si, err)
	}
}

// TestCreateBatchAfterRefusedApproveMined covers a temporary refusal: an
// endpoint answers the approve's send with a JSON-RPC error (a full pool),
// the service re-broadcasts it and it is mined. The batch must be created,
// because the service treats the answer as an unknown outcome too.
func TestCreateBatchAfterRefusedApproveMined(t *testing.T) {
	f := newChainFixture()
	f.approveSendErr = refusalError{"txpool is full"}
	c, _ := f.contract(t)

	if _, err := createBatch(t, c); err != nil {
		t.Fatalf("CreateBatch: %v, want the batch created once the approve is mined", err)
	}
	if n := f.distinctSentTo(f.postageStamp); n != 1 {
		t.Fatalf("%d createBatch transactions sent, want 1", n)
	}
}

// TestCreateBatchApproveNeverMined asserts the end of the wait: the
// approve's send fails and the approve gets no receipt, so CreateBatch
// returns when the caller's context ends, with the send error, sends no
// createBatch, and logs the approve's hash at warning level.
func TestCreateBatchApproveNeverMined(t *testing.T) {
	f := newChainFixture()
	f.approveSendErr = http429
	f.approveMined = false
	c, _ := f.contract(t)

	_, err := createBatchWithin(c, 300*time.Millisecond)
	var httpErr rpc.HTTPError
	if !errors.As(err, &httpErr) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("CreateBatch: %v, want the send error and the end of the wait", err)
	}
	if n := f.distinctSentTo(f.postageStamp); n != 0 {
		t.Fatalf("%d createBatch transactions sent without a mined approve, want 0", n)
	}
	approves := f.sentTo(f.bzzToken)
	if len(approves) == 0 {
		t.Fatal("no approve sent")
	}
	out := f.logged()
	if !strings.Contains(out, "may still be mined") || !strings.Contains(out, approves[0].Hash().String()) {
		t.Fatalf("want a warning naming the approve's hash; log: %q", out)
	}
}
