// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package transaction

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethersphere/bee/v2/pkg/storage"
)

// ErrCancelledForReveal is returned to the caller of an operation whose
// blocked transaction the node cancelled to protect a reveal (#819). It
// wraps ErrTransactionCancelled.
var ErrCancelledForReveal = fmt.Errorf("cancelled to protect a storage-lottery reveal: %w", ErrTransactionCancelled)

// putPending writes a pending key.
func (t *transactionService) putPending(txHash common.Hash, p pendingTransaction) error {
	t.pendingMu.Lock()
	defer t.pendingMu.Unlock()
	return t.store.Put(pendingTransactionKey(txHash), p)
}

// deletePending removes a pending key.
func (t *transactionService) deletePending(txHash common.Hash) error {
	t.pendingMu.Lock()
	defer t.pendingMu.Unlock()
	return t.store.Delete(pendingTransactionKey(txHash))
}

// getPending reads a pending key.
func (t *transactionService) getPending(txHash common.Hash) (pendingTransaction, error) {
	t.pendingMu.Lock()
	defer t.pendingMu.Unlock()
	var p pendingTransaction
	err := t.store.Get(pendingTransactionKey(txHash), &p)
	return p, err
}

// updatePending changes a pending key that exists; storage.ErrNotFound when
// it does not.
func (t *transactionService) updatePending(txHash common.Hash, change func(*pendingTransaction)) error {
	t.pendingMu.Lock()
	defer t.pendingMu.Unlock()
	var p pendingTransaction
	if err := t.store.Get(pendingTransactionKey(txHash), &p); err != nil {
		return err
	}
	change(&p)
	return t.store.Put(pendingTransactionKey(txHash), p)
}

// keptPending applies the nonce rule (#819): it reads the chain's account
// nonce once, removes every pending entry whose nonce is below it, and
// returns the others with their nonces, which still count for the next
// nonce. An entry the active endpoint does not know is kept: that endpoint
// is not the only one, and a transaction it never saw may be in another's
// pool. When the chain nonce cannot be read, every entry is kept.
//
// Nothing else removes a pending entry: no timer, no "not found" answer.
// See docs/experiments/send-everywhere/spec.md, part 3.
func (t *transactionService) keptPending(ctx context.Context) (map[common.Hash]uint64, error) {
	hashes, err := t.PendingTransactions()
	if err != nil {
		return nil, err
	}
	kept := make(map[common.Hash]uint64, len(hashes))
	if len(hashes) == 0 {
		return kept, nil
	}
	chainNonce, nonceErr := t.backend.NonceAt(ctx, t.sender, nil)
	if nonceErr != nil {
		t.logger.Debug("chain nonce unavailable; keeping every pending transaction", "error", nonceErr)
	}
	for _, txHash := range hashes {
		stored, err := t.StoredTransaction(txHash)
		if err != nil {
			t.logger.Warning("pending transaction data unavailable, relying on onchain nonce", "tx", txHash, "error", err)
			continue
		}
		if nonceErr == nil && stored.Nonce < chainNonce {
			if err := t.deletePending(txHash); err != nil {
				t.logger.Error(err, "error while unregistering transaction as pending", "tx", txHash)
			}
			t.watchdog.settled(t, txHash)
			continue
		}
		kept[txHash] = stored.Nonce
	}
	return kept, nil
}

// pendingEntry is a pending transaction with what is stored for it.
type pendingEntry struct {
	hash    common.Hash
	stored  *StoredTransaction
	pending pendingTransaction
}

// pendingEntries lists the pending entries, each with its stored
// transaction, in ascending nonce order; entries without a stored
// transaction are left out.
func (t *transactionService) pendingEntries() ([]pendingEntry, error) {
	hashes, err := t.PendingTransactions()
	if err != nil {
		return nil, err
	}
	entries := make([]pendingEntry, 0, len(hashes))
	for _, h := range hashes {
		stored, err := t.StoredTransaction(h)
		if err != nil {
			continue
		}
		p, err := t.getPending(h)
		if err != nil {
			continue
		}
		entries = append(entries, pendingEntry{hash: h, stored: stored, pending: p})
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].stored.Nonce < entries[j].stored.Nonce })
	return entries, nil
}

// newestPerNonce keeps, for each nonce, only the newest stored transaction:
// the one no other entry at the nonce replaces, or, when there are several,
// the newest by newerThan. A replaced or cancelled transaction is so never
// sent again over its replacement.
func newestPerNonce(entries []pendingEntry) []pendingEntry {
	replaced := make(map[common.Hash]bool)
	for _, e := range entries {
		if e.stored.Replaces != (common.Hash{}) {
			replaced[e.stored.Replaces] = true
		}
	}
	byNonce := make(map[uint64]pendingEntry)
	var nonces []uint64
	for _, e := range entries {
		if replaced[e.hash] {
			continue
		}
		cur, ok := byNonce[e.stored.Nonce]
		if !ok {
			nonces = append(nonces, e.stored.Nonce)
			byNonce[e.stored.Nonce] = e
			continue
		}
		if newerThan(e.stored, cur.stored) {
			byNonce[e.stored.Nonce] = e
		}
	}
	sort.Slice(nonces, func(i, j int) bool { return nonces[i] < nonces[j] })
	out := make([]pendingEntry, 0, len(nonces))
	for _, n := range nonces {
		out = append(out, byNonce[n])
	}
	return out
}

// newerThan reports whether a is a later transaction than b for the same
// nonce: created later, or in the same second with a higher fee cap, as a
// replacement has.
func newerThan(a, b *StoredTransaction) bool {
	if a.Created != b.Created {
		return a.Created > b.Created
	}
	return a.GasFeeCap != nil && b.GasFeeCap != nil && a.GasFeeCap.Cmp(b.GasFeeCap) > 0
}

// original follows txHash's Replaces chain to the first transaction at its
// nonce, and returns it with its stored record.
func (t *transactionService) original(txHash common.Hash) (common.Hash, *StoredTransaction, error) {
	stored, err := t.StoredTransaction(txHash)
	if err != nil {
		return common.Hash{}, nil, err
	}
	seen := map[common.Hash]bool{txHash: true}
	for stored.Replaces != (common.Hash{}) && !seen[stored.Replaces] {
		prev := t.storedOrNil(stored.Replaces)
		if prev == nil {
			// the earlier record is gone: the chain starts here
			break
		}
		txHash = stored.Replaces
		seen[txHash] = true
		stored = prev
	}
	return txHash, stored, nil
}

// storedOrNil returns the stored transaction, or nil when it cannot be
// read.
func (t *transactionService) storedOrNil(txHash common.Hash) *StoredTransaction {
	stored, err := t.StoredTransaction(txHash)
	if err != nil {
		return nil
	}
	return stored
}

// sameOperation reports whether two stored transactions do the same thing:
// the same recipient, value and data.
func sameOperation(a, b *StoredTransaction) bool {
	switch {
	case (a.To == nil) != (b.To == nil):
		return false
	case a.To != nil && *a.To != *b.To:
		return false
	case !bytes.Equal(a.Data, b.Data):
		return false
	}
	av, bv := a.Value, b.Value
	if av == nil || bv == nil {
		return (av == nil || av.Sign() == 0) && (bv == nil || bv.Sign() == 0)
	}
	return av.Cmp(bv) == 0
}

// receiptOutcome decides what a receipt mined at txHash's nonce means for a
// caller waiting on txHash: its operation's outcome when the mined
// transaction is txHash or a replacement of the same original with the same
// recipient, value and data; ErrCancelledForReveal for a cancellation that
// protected a reveal; ErrTransactionCancelled for anything else. The check
// runs when the receipt arrives, so a replacement stored after the wait
// started counts too.
func (t *transactionService) receiptOutcome(txHash common.Hash, receipt *types.Receipt) (*types.Receipt, error) {
	if receipt.TxHash == txHash {
		return receipt, nil
	}
	origHash, orig, err := t.original(txHash)
	if err != nil {
		return nil, ErrTransactionCancelled
	}
	mined, err := t.StoredTransaction(receipt.TxHash)
	if err != nil {
		// a transaction this node did not store used the nonce
		return nil, ErrTransactionCancelled
	}
	if mined.CancelReason == CancelReasonReveal {
		return nil, ErrCancelledForReveal
	}
	minedOrig, _, err := t.original(receipt.TxHash)
	if err == nil && minedOrig == origHash && sameOperation(orig, mined) {
		return receipt, nil
	}
	return nil, ErrTransactionCancelled
}

// receiptAtNonce looks for a receipt among the stored pending transactions
// at txHash's nonce, txHash included, for a wait whose monitor watch did
// not see one.
func (t *transactionService) receiptAtNonce(ctx context.Context, txHash common.Hash) *types.Receipt {
	stored, err := t.StoredTransaction(txHash)
	if err != nil {
		return nil
	}
	candidates := []common.Hash{txHash}
	if hashes, err := t.PendingTransactions(); err == nil {
		for _, h := range hashes {
			if h == txHash {
				continue
			}
			if other, err := t.StoredTransaction(h); err == nil && other.Nonce == stored.Nonce {
				candidates = append(candidates, h)
			}
		}
	}
	for _, h := range candidates {
		if r, err := t.backend.TransactionReceipt(ctx, h); err == nil && r != nil {
			return r
		}
	}
	return nil
}

// storedAtNonce lists every stored transaction at nonce except skip,
// replacements and cancellations that are no longer pending included.
func (t *transactionService) storedAtNonce(nonce uint64, skip common.Hash) []common.Hash {
	var out []common.Hash
	_ = t.store.Iterate(storedTransactionPrefix, func(key, value []byte) (bool, error) {
		h := common.HexToHash(string(key[len(storedTransactionPrefix):]))
		if h == skip {
			return false, nil
		}
		var st StoredTransaction
		if err := json.Unmarshal(value, &st); err == nil && st.Nonce == nonce {
			out = append(out, h)
		}
		return false, nil
	})
	return out
}

// isNotFound reports a missing store key.
func isNotFound(err error) bool { return errors.Is(err, storage.ErrNotFound) }
