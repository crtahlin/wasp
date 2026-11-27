// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package storageincentives

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethersphere/bee/v2/pkg/log"
	"github.com/ethersphere/bee/v2/pkg/transaction"
)

// errNotMinedInPhase: a commit or claim was sent but none of its
// transactions was mined before its phase ended.
var errNotMinedInPhase = errors.New("not mined in its phase")

// stepBackend is the part of the chain backend the phase steps read.
type stepBackend interface {
	BlockNumber(context.Context) (uint64, error)
	HeaderByNumber(context.Context, *big.Int) (*types.Header, error)
}

// phaseSteps runs a lottery transaction's steps within its phase, the
// blocks [start, end). Each step, receipt wait included, ends after
// revealStepBlocks or at end, whichever is first, so a pending transaction
// is looked at again, and replaced with raised fees when needed, while its
// phase is open (#793, #796). The reveal, the commit and the claim all run
// on it.
type phaseSteps struct {
	backend    stepBackend
	blockTime  func() time.Duration
	logger     log.Logger
	name       string // "reveal", "commit" or "claim", for the logs
	round      uint64
	start, end uint64
	// stop, if not nil, is checked before every step; true ends the
	// steps.
	stop func() bool
	// step takes one step at the given block; done ends the steps.
	step func(ctx context.Context, block uint64) (done bool, err error)
	// after, if not nil, is called after every step that is not done.
	after func(ctx context.Context)
}

// run takes steps until one is done, stop answers true, the phase ends or
// ctx is done.
func (p phaseSteps) run(ctx context.Context) {
	for {
		if p.stop != nil && p.stop() {
			return
		}

		block, err := p.backend.BlockNumber(ctx)
		switch {
		case err != nil:
			p.logger.Debug(p.name+": reading block number", "error", err)
		case block >= p.end:
			return
		case block >= p.start:
			until := min(p.end, block+revealStepBlocks)
			stepCtx, cancel := context.WithTimeout(ctx, time.Duration(until-block)*p.blockTime())
			done, err := p.step(stepCtx, block)
			bounded := until < p.end && ctx.Err() == nil && errors.Is(stepCtx.Err(), context.DeadlineExceeded)
			cancel()
			switch {
			case err != nil && bounded && errors.Is(err, context.DeadlineExceeded):
				p.logger.Debug(p.name+" not mined within the step; checking it again", "round", p.round)
			case err != nil:
				p.logger.Error(err, p.name+" failed, retrying at the next block", "round", p.round)
			}
			if done {
				return
			}
			if p.after != nil {
				p.after(ctx)
			}
			if bounded {
				// the step used its blocks waiting; look again at once
				continue
			}
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(p.blockTime()):
		}
	}
}

// feeAtRisk reports whether the stored fee cap of the transaction is below
// the base fee that revealFeeHeadroomBlocks full blocks can reach from the
// latest block's: the latest base fee times (9/8)^revealFeeHeadroomBlocks,
// rounded up. Such a transaction can be overtaken by the base fee before
// the next step looks at it, so it is replaced now (#793).
func feeAtRisk(ctx context.Context, txService transaction.Service, backend stepBackend, txHash common.Hash) bool {
	stored, err := txService.StoredTransaction(txHash)
	if err != nil || stored.GasFeeCap == nil {
		return false
	}
	header, err := backend.HeaderByNumber(ctx, nil)
	if err != nil || header.BaseFee == nil {
		return false
	}
	return stored.GasFeeCap.Cmp(baseFeeAfterFullBlocks(header.BaseFee, revealFeeHeadroomBlocks)) < 0
}

// phaseTx is a commit or a claim within its phase: sent once, then
// replaced with raised fees like the reveal (#796). Its transactions are
// kept in memory only: a commit is not sent again after a restart, and a
// claim is sent again from the start.
type phaseTx struct {
	name      string
	txService transaction.Service
	backend   stepBackend
	logger    log.Logger
	// send sends the transaction, calling hook before the broadcast, and
	// waits for its receipt until ctx ends.
	send func(ctx context.Context, hook transaction.BeforeBroadcastFunc) (common.Hash, error)
	// beforeBroadcast, if not nil, is called with the hash of the first
	// send and of every replacement before it is broadcast; an error
	// stops that broadcast.
	beforeBroadcast func(txHash common.Hash) error

	block     uint64        // the block of the current step
	txs       []common.Hash // sent, oldest first
	sentBlock uint64        // the block the newest was sent at

	done  bool
	mined common.Hash
	err   error // the outcome of a done transaction not mined
}

// listSent records a hash before its broadcast.
func (p *phaseTx) listSent(txHash common.Hash) error {
	if p.beforeBroadcast != nil {
		if err := p.beforeBroadcast(txHash); err != nil {
			return err
		}
	}
	p.txs = append(p.txs, txHash)
	p.sentBlock = p.block
	return nil
}

// finish ends the transaction: mined as txHash when err is nil.
func (p *phaseTx) finish(txHash common.Hash, err error) {
	p.done = true
	p.err = err
	if err == nil {
		p.mined = txHash
	}
}

// step takes one step at block: the first send, or a look at the sent
// transactions with a replacement or a rebroadcast when needed. An error
// with done false is retried at the next step; a definite outcome is kept
// for result.
func (p *phaseTx) step(ctx context.Context, block uint64) (bool, error) {
	p.block = block

	if len(p.txs) == 0 {
		txHash, err := p.send(ctx, p.listSent)
		switch {
		case err == nil:
			p.finish(txHash, nil)
			return true, nil
		case errors.Is(err, transaction.ErrTransactionReverted):
			p.finish(txHash, err)
			return true, nil
		case len(p.txs) == 0:
			// nothing was broadcast, for example because the reads
			// before the signing outlasted the step: send again at the
			// next step while the phase is open, as the reveal does
			return false, err
		case errors.Is(err, transaction.ErrNotBroadcast):
			p.finish(txHash, err)
			return true, nil
		}
		// sent, and not mined within the step, or the send's answer was
		// ambiguous: the next step looks at it
		return false, err
	}

	// any sent transaction mined: done, whichever it is
	if mined, _, _ := p.scan(ctx); mined != (common.Hash{}) {
		p.finish(mined, nil)
		return true, nil
	}

	newest := p.txs[len(p.txs)-1]
	st, err := p.txService.TransactionStatus(ctx, newest)
	if err != nil {
		return false, err
	}
	aged := block >= p.sentBlock+revealReplaceAfterBlocks
	switch st {
	case transaction.TxMined:
		p.finish(newest, nil)
		return true, nil
	case transaction.TxReverted:
		p.finish(newest, fmt.Errorf("%w: %s", transaction.ErrTransactionReverted, newest))
		return true, nil
	case transaction.TxPending:
		if !aged && !feeAtRisk(ctx, p.txService, p.backend, newest) {
			return false, nil
		}
		p.logger.Info(p.name+": replacing a pending transaction with raised fees", "tx", newest)
		_, err := p.txService.ReplaceTransaction(ctx, newest, p.listSent)
		return false, err
	case transaction.TxNotFound:
		if !p.synced(ctx) {
			return false, nil
		}
		if aged || feeAtRisk(ctx, p.txService, p.backend, newest) {
			p.logger.Info(p.name+": replacing a lost transaction with raised fees", "tx", newest)
			_, err := p.txService.ReplaceTransaction(ctx, newest, p.listSent)
			return false, err
		}
		p.logger.Info(p.name+": rebroadcasting a lost transaction", "tx", newest)
		return false, p.txService.RebroadcastTransaction(ctx, newest)
	case transaction.TxCancelled:
		if !p.synced(ctx) {
			return false, nil
		}
		// every send shares one nonce: look at the others again, as one
		// can have been mined since the scan above, and decide only when
		// every lookup answered
		mined, reverted, failed := p.scan(ctx)
		switch {
		case mined != (common.Hash{}):
			p.finish(mined, nil)
			return true, nil
		case failed:
			return false, nil
		}
		// a reverted one of them used the nonce
		if reverted != (common.Hash{}) {
			p.finish(reverted, fmt.Errorf("%w: %s", transaction.ErrTransactionReverted, reverted))
			return true, nil
		}
		p.finish(newest, fmt.Errorf("%w: %s", transaction.ErrTransactionCancelled, newest))
		return true, nil
	}
	return false, nil
}

// scan looks every sent transaction up and returns one that is mined and
// one that reverted, each zero when there is none, and whether a lookup
// failed.
func (p *phaseTx) scan(ctx context.Context) (mined, reverted common.Hash, failed bool) {
	for _, txHash := range p.txs {
		st, err := p.txService.TransactionStatus(ctx, txHash)
		if err != nil {
			failed = true
			continue
		}
		switch st {
		case transaction.TxMined:
			return txHash, reverted, failed
		case transaction.TxReverted:
			reverted = txHash
		}
	}
	return common.Hash{}, reverted, failed
}

func (p *phaseTx) synced(ctx context.Context) bool {
	header, err := p.backend.HeaderByNumber(ctx, nil)
	if err != nil {
		return false
	}
	return headerFresh(header)
}

// result is the mined hash, or the newest sent hash with the reason none
// was mined; an error wrapping transaction.ErrNotBroadcast when nothing was
// sent.
func (p *phaseTx) result(ctx context.Context) (common.Hash, error) {
	if p.done {
		if p.err == nil {
			return p.mined, nil
		}
		return p.newest(), p.err
	}
	cause := errNotMinedInPhase
	if err := ctx.Err(); err != nil {
		cause = err
	}
	if len(p.txs) == 0 {
		// the phase or ctx ended before the first send
		return common.Hash{}, fmt.Errorf("%s: %w: %w", p.name, transaction.ErrNotBroadcast, cause)
	}
	return p.newest(), fmt.Errorf("%s: %w", p.name, cause)
}

func (p *phaseTx) newest() common.Hash {
	if len(p.txs) == 0 {
		return common.Hash{}
	}
	return p.txs[len(p.txs)-1]
}

// sendInPhase sends a commit or a claim of round at once, as before, and
// replaces it until one of its transactions is mined or block end, where
// its phase ends (#796). It returns the mined hash, or the newest sent hash
// and why none was mined.
func (r *Revealer) sendInPhase(ctx context.Context, name string, round, end uint64,
	send func(ctx context.Context, hook transaction.BeforeBroadcastFunc) (common.Hash, error),
	beforeBroadcast func(txHash common.Hash) error,
) (common.Hash, error) {
	tx := &phaseTx{
		name:            name,
		txService:       r.txService,
		backend:         r.backend,
		logger:          r.logger,
		send:            send,
		beforeBroadcast: beforeBroadcast,
	}
	phaseSteps{
		backend:   r.backend,
		blockTime: r.blockTime,
		logger:    r.logger,
		name:      name,
		round:     round,
		end:       end,
		step:      tx.step,
	}.run(ctx)
	if !tx.done && len(tx.txs) > 0 && ctx.Err() == nil {
		// mined in the phase's last blocks, after the last step looked
		if mined, _, _ := tx.scan(ctx); mined != (common.Hash{}) {
			tx.finish(mined, nil)
		}
	}
	return tx.result(ctx)
}
