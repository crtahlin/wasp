// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pebblestore

import (
	"context"
	"fmt"
	"sync"

	"github.com/cockroachdb/pebble"
	"github.com/ethersphere/bee/v2/pkg/storage"
)

// Batch implements storage.BatchStore.
func (s *Store) Batch(ctx context.Context) storage.Batch {
	return &Batch{
		ctx:   ctx,
		batch: s.db.NewBatch(),
		store: s,
	}
}

type Batch struct {
	ctx context.Context

	mu    sync.Mutex // mu guards batch and done.
	batch *pebble.Batch
	store *Store
	done  bool
}

func (b *Batch) Put(item storage.Item) error {
	if err := b.ctx.Err(); err != nil {
		return err
	}

	val, err := item.Marshal()
	if err != nil {
		return fmt.Errorf("unable to marshal item: %w", err)
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	return b.batch.Set(key(item), val, nil)
}

func (b *Batch) Delete(item storage.Item) error {
	if err := b.ctx.Err(); err != nil {
		return err
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	return b.batch.Delete(key(item), nil)
}

func (b *Batch) Commit() error {
	if err := b.ctx.Err(); err != nil {
		return err
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if b.done {
		return storage.ErrBatchCommitted
	}

	if err := b.batch.Commit(pebble.NoSync); err != nil {
		return fmt.Errorf("unable to commit batch: %w", err)
	}
	// A pebble batch holds a buffer that is only returned to the pool on Close.
	// goleveldb's has nothing to release, so this has no counterpart there and
	// is easy to leave out; leaving it out leaks a buffer per batch.
	if err := b.batch.Close(); err != nil {
		return fmt.Errorf("unable to close batch: %w", err)
	}

	b.done = true
	return nil
}
