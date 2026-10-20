// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package backendsimulation

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/ethersphere/bee/v2/pkg/transaction"
)

// RPCError is a JSON-RPC error answer, as the rpc client returns it.
type RPCError struct{ Msg string }

func (e RPCError) Error() string  { return e.Msg }
func (e RPCError) ErrorCode() int { return -32000 }

var _ rpc.Error = RPCError{}

// SendFunc decides an endpoint's answer to a sent transaction before the
// chain sees it. A non-nil error is returned and the transaction is not
// taken.
type SendFunc func(endpoint string, tx *types.Transaction) error

// Chain is a simulated chain that mines what its endpoints receive. Mine
// adds a block. Each endpoint (Endpoint) shares the chain's pool and state;
// a black hole (Blackhole) keeps what it receives to itself (#819).
type Chain struct {
	mu      sync.Mutex
	chainID *big.Int
	signer  types.Signer

	block   uint64
	baseFee *big.Int
	nonces  map[common.Address]uint64
	// history holds each account's nonce after each block
	history map[uint64]map[common.Address]uint64
	funds   map[common.Address]*big.Int // nil: unlimited

	pool     map[common.Hash]*types.Transaction
	mined    map[common.Hash]*types.Receipt
	minedTxs map[common.Hash]*types.Transaction
	sends    []Sent
	send     SendFunc
}

// Sent records one send the chain's endpoints received.
type Sent struct {
	Endpoint string
	Tx       *types.Transaction
	Err      error
}

// NewChain returns a chain at block 1 with the given base fee.
func NewChain(chainID, baseFee *big.Int) *Chain {
	return &Chain{
		chainID:  chainID,
		signer:   types.LatestSignerForChainID(chainID),
		block:    1,
		baseFee:  new(big.Int).Set(baseFee),
		nonces:   make(map[common.Address]uint64),
		history:  make(map[uint64]map[common.Address]uint64),
		pool:     make(map[common.Hash]*types.Transaction),
		mined:    make(map[common.Hash]*types.Receipt),
		minedTxs: make(map[common.Hash]*types.Transaction),
	}
}

// SetSendFunc sets the answer function for every endpoint's sends.
func (c *Chain) SetSendFunc(f SendFunc) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.send = f
}

// SetBaseFee sets the base fee of the next blocks.
func (c *Chain) SetBaseFee(v *big.Int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.baseFee = new(big.Int).Set(v)
}

// SetFunds limits an account's funds; without a limit an account can pay
// for anything. A transaction costs gas limit times fee cap plus value.
func (c *Chain) SetFunds(account common.Address, v *big.Int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.funds == nil {
		c.funds = make(map[common.Address]*big.Int)
	}
	c.funds[account] = new(big.Int).Set(v)
}

// Sends returns the sends the endpoints received, in order.
func (c *Chain) Sends() []Sent {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Sent(nil), c.sends...)
}

// Nonce returns the account's nonce at the latest block.
func (c *Chain) Nonce(account common.Address) uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.nonces[account]
}

// Block returns the latest block number.
func (c *Chain) Block() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.block
}

// MinedAt returns the transaction mined at the account's nonce, or nil.
func (c *Chain) MinedAt(account common.Address, nonce uint64) *types.Transaction {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, tx := range c.minedTxs {
		if from, err := types.Sender(c.signer, tx); err == nil && from == account && tx.Nonce() == nonce {
			return tx
		}
	}
	return nil
}

// Receipt returns the receipt of a mined transaction, or nil.
func (c *Chain) Receipt(h common.Hash) *types.Receipt {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.mined[h]
}

// Mine adds one block: for each sender, the pooled transactions from its
// nonce on, in nonce order, while one is there whose fee cap covers the
// base fee and whose cost the sender's funds cover.
func (c *Chain) Mine() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.block++
	bySender := make(map[common.Address][]*types.Transaction)
	for _, tx := range c.pool {
		from, err := types.Sender(c.signer, tx)
		if err != nil {
			continue
		}
		bySender[from] = append(bySender[from], tx)
	}
	for from, txs := range bySender {
		sort.Slice(txs, func(i, j int) bool { return txs[i].Nonce() < txs[j].Nonce() })
		for {
			next := c.nonces[from]
			var pick *types.Transaction
			for _, tx := range txs {
				if tx.Nonce() == next && tx.GasFeeCap().Cmp(c.baseFee) >= 0 && (pick == nil || tx.GasFeeCap().Cmp(pick.GasFeeCap()) > 0) {
					pick = tx
				}
			}
			if pick == nil || !c.covers(from, pick) {
				break
			}
			if c.funds != nil {
				if f, ok := c.funds[from]; ok {
					f.Sub(f, cost(pick))
				}
			}
			c.nonces[from] = next + 1
			c.minedTxs[pick.Hash()] = pick
			c.mined[pick.Hash()] = &types.Receipt{
				TxHash:      pick.Hash(),
				Status:      types.ReceiptStatusSuccessful,
				BlockNumber: new(big.Int).SetUint64(c.block),
				GasUsed:     pick.Gas(),
			}
			for h, tx := range c.pool {
				if f, err := types.Sender(c.signer, tx); err == nil && f == from && tx.Nonce() == next {
					delete(c.pool, h)
				}
			}
		}
	}
	snapshot := make(map[common.Address]uint64, len(c.nonces))
	for a, n := range c.nonces {
		snapshot[a] = n
	}
	c.history[c.block] = snapshot
}

// nonceAt returns the account's nonce after the given block. Called with
// c.mu held.
func (c *Chain) nonceAt(a common.Address, block uint64) uint64 {
	if block >= c.block {
		return c.nonces[a]
	}
	for b := block; b > 0; b-- {
		if snap, ok := c.history[b]; ok {
			return snap[a]
		}
	}
	return 0
}

func cost(tx *types.Transaction) *big.Int {
	v := new(big.Int).Mul(new(big.Int).SetUint64(tx.Gas()), tx.GasFeeCap())
	if tx.Value() != nil {
		v.Add(v, tx.Value())
	}
	return v
}

// covers reports whether from's funds pay for tx. Called with c.mu held.
func (c *Chain) covers(from common.Address, tx *types.Transaction) bool {
	if c.funds == nil {
		return true
	}
	f, ok := c.funds[from]
	return !ok || f.Cmp(cost(tx)) >= 0
}

// add takes a sent transaction into the shared pool, answering as
// go-ethereum's pool does. Called with c.mu held.
func (c *Chain) add(endpoint string, tx *types.Transaction) error {
	if c.send != nil {
		if err := c.send(endpoint, tx); err != nil {
			return err
		}
	}
	from, err := types.Sender(c.signer, tx)
	if err != nil {
		return RPCError{"invalid sender"}
	}
	if _, ok := c.pool[tx.Hash()]; ok {
		return RPCError{"already known"}
	}
	if _, ok := c.mined[tx.Hash()]; ok {
		return RPCError{"already known"}
	}
	if tx.Nonce() < c.nonces[from] {
		return RPCError{"nonce too low"}
	}
	if !c.covers(from, tx) {
		return RPCError{"insufficient funds for gas * price + value"}
	}
	for h, other := range c.pool {
		if f, err := types.Sender(c.signer, other); err != nil || f != from || other.Nonce() != tx.Nonce() {
			continue
		}
		if !bumpedEnough(tx.GasFeeCap(), other.GasFeeCap()) || !bumpedEnough(tx.GasTipCap(), other.GasTipCap()) {
			return RPCError{"replacement transaction underpriced"}
		}
		delete(c.pool, h)
	}
	c.pool[tx.Hash()] = tx
	return nil
}

func bumpedEnough(v, old *big.Int) bool {
	need := new(big.Int).Mul(old, big.NewInt(110))
	return new(big.Int).Mul(v, big.NewInt(100)).Cmp(need) >= 0
}

// Endpoint returns a view of the chain as one RPC endpoint with the given
// name.
func (c *Chain) Endpoint(name string) transaction.Backend {
	return &chainEndpoint{chain: c, name: name}
}

// Blackhole returns an endpoint that takes every sent transaction into its
// own pool, answers TransactionByHash with it, never passes it on, and
// answers every other call from the chain, as the endpoint of the incident
// did (#819).
func (c *Chain) Blackhole(name string) *BlackholeEndpoint {
	return &BlackholeEndpoint{chainEndpoint: chainEndpoint{chain: c, name: name}, held: make(map[common.Hash]*types.Transaction)}
}

type chainEndpoint struct {
	chain *Chain
	name  string
}

var errSimulated = errors.New("simulated chain: not implemented")

func (e *chainEndpoint) SendTransaction(_ context.Context, tx *types.Transaction) error {
	c := e.chain
	c.mu.Lock()
	defer c.mu.Unlock()
	err := c.add(e.name, tx)
	c.sends = append(c.sends, Sent{Endpoint: e.name, Tx: tx, Err: err})
	return err
}

func (e *chainEndpoint) TransactionByHash(_ context.Context, h common.Hash) (*types.Transaction, bool, error) {
	c := e.chain
	c.mu.Lock()
	defer c.mu.Unlock()
	if tx, ok := c.pool[h]; ok {
		return tx, true, nil
	}
	if tx, ok := c.minedTxs[h]; ok {
		return tx, false, nil
	}
	return nil, false, ethereum.NotFound
}

func (e *chainEndpoint) TransactionReceipt(_ context.Context, h common.Hash) (*types.Receipt, error) {
	c := e.chain
	c.mu.Lock()
	defer c.mu.Unlock()
	if r, ok := c.mined[h]; ok {
		return r, nil
	}
	return nil, ethereum.NotFound
}

func (e *chainEndpoint) BlockNumber(context.Context) (uint64, error) {
	return e.chain.Block(), nil
}

func (e *chainEndpoint) HeaderByNumber(_ context.Context, n *big.Int) (*types.Header, error) {
	c := e.chain
	c.mu.Lock()
	defer c.mu.Unlock()
	number := c.block
	if n != nil {
		number = n.Uint64()
	}
	return &types.Header{Number: new(big.Int).SetUint64(number), BaseFee: new(big.Int).Set(c.baseFee), Time: uint64(time.Now().Unix())}, nil
}

func (e *chainEndpoint) NonceAt(_ context.Context, a common.Address, n *big.Int) (uint64, error) {
	c := e.chain
	c.mu.Lock()
	defer c.mu.Unlock()
	if n == nil {
		return c.nonces[a], nil
	}
	return c.nonceAt(a, n.Uint64()), nil
}

func (e *chainEndpoint) PendingNonceAt(_ context.Context, a common.Address) (uint64, error) {
	c := e.chain
	c.mu.Lock()
	defer c.mu.Unlock()
	next := c.nonces[a]
	for {
		found := false
		for _, tx := range c.pool {
			if f, err := types.Sender(c.signer, tx); err == nil && f == a && tx.Nonce() == next {
				found = true
				break
			}
		}
		if !found {
			return next, nil
		}
		next++
	}
}

func (e *chainEndpoint) BalanceAt(_ context.Context, a common.Address, _ *big.Int) (*big.Int, error) {
	c := e.chain
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.funds != nil {
		if f, ok := c.funds[a]; ok {
			return new(big.Int).Set(f), nil
		}
	}
	return new(big.Int).Lsh(big.NewInt(1), 128), nil
}

func (e *chainEndpoint) SuggestedFeeAndTip(_ context.Context, gasPrice *big.Int, boost int) (*big.Int, *big.Int, error) {
	c := e.chain
	c.mu.Lock()
	defer c.mu.Unlock()
	if gasPrice != nil {
		return new(big.Int).Set(gasPrice), new(big.Int).Set(gasPrice), nil
	}
	tip := big.NewInt(1_000_000_000)
	tip.Mul(tip, big.NewInt(int64(100+boost)))
	tip.Div(tip, big.NewInt(100))
	fee := new(big.Int).Mul(c.baseFee, big.NewInt(2))
	fee.Add(fee, tip)
	return fee, tip, nil
}

func (e *chainEndpoint) EstimateGas(_ context.Context, msg ethereum.CallMsg) (uint64, error) {
	if len(msg.Data) == 0 {
		return 21_000, nil
	}
	return 50_000, nil
}

func (e *chainEndpoint) SuggestGasTipCap(context.Context) (*big.Int, error) {
	return big.NewInt(1_000_000_000), nil
}

func (e *chainEndpoint) ChainID(context.Context) (*big.Int, error) {
	return new(big.Int).Set(e.chain.chainID), nil
}

func (e *chainEndpoint) CallContract(context.Context, ethereum.CallMsg, *big.Int) ([]byte, error) {
	return nil, errSimulated
}

func (e *chainEndpoint) CodeAt(context.Context, common.Address, *big.Int) ([]byte, error) {
	return nil, errSimulated
}

func (e *chainEndpoint) FilterLogs(context.Context, ethereum.FilterQuery) ([]types.Log, error) {
	return nil, errSimulated
}

func (e *chainEndpoint) Close() {}

// BlackholeEndpoint is the endpoint Chain.Blackhole returns.
type BlackholeEndpoint struct {
	chainEndpoint
	mu   sync.Mutex
	held map[common.Hash]*types.Transaction
}

// SendTransaction keeps tx to itself.
func (b *BlackholeEndpoint) SendTransaction(_ context.Context, tx *types.Transaction) error {
	c := b.chain
	c.mu.Lock()
	c.sends = append(c.sends, Sent{Endpoint: b.name, Tx: tx})
	c.mu.Unlock()
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.held[tx.Hash()]; ok {
		return RPCError{"already known"}
	}
	b.held[tx.Hash()] = tx
	return nil
}

// TransactionByHash answers from what it holds, then from the chain.
func (b *BlackholeEndpoint) TransactionByHash(ctx context.Context, h common.Hash) (*types.Transaction, bool, error) {
	b.mu.Lock()
	tx, ok := b.held[h]
	b.mu.Unlock()
	if ok {
		if _, mined := b.chain.minedLocked(h); !mined {
			return tx, true, nil
		}
	}
	return b.chainEndpoint.TransactionByHash(ctx, h)
}

// PendingNonceAt counts what it holds as pending, as a real pool does.
func (b *BlackholeEndpoint) PendingNonceAt(ctx context.Context, a common.Address) (uint64, error) {
	next, err := b.chainEndpoint.PendingNonceAt(ctx, a)
	if err != nil {
		return 0, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for {
		found := false
		for _, tx := range b.held {
			if f, err := types.Sender(b.chain.signer, tx); err == nil && f == a && tx.Nonce() == next {
				found = true
				break
			}
		}
		if !found {
			return next, nil
		}
		next++
	}
}

// Held reports whether the black hole holds the transaction.
func (b *BlackholeEndpoint) Held(h common.Hash) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.held[h]
	return ok
}

func (c *Chain) minedLocked(h common.Hash) (*types.Receipt, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.mined[h]
	return r, ok
}

// String names the chain's state, for test failure messages.
func (c *Chain) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return fmt.Sprintf("block %d, %d pooled, %d mined", c.block, len(c.pool), len(c.mined))
}
