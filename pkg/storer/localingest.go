// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package storer

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"

	"github.com/ethersphere/bee/v2/pkg/encryption"
	storage "github.com/ethersphere/bee/v2/pkg/storage"
	"github.com/ethersphere/bee/v2/pkg/storage/storageutil"
	"github.com/ethersphere/bee/v2/pkg/storer/internal"
	pinstore "github.com/ethersphere/bee/v2/pkg/storer/internal/pinning"
	"github.com/ethersphere/bee/v2/pkg/storer/internal/transaction"
	"github.com/ethersphere/bee/v2/pkg/swarm"
	"github.com/prometheus/client_golang/prometheus"
)

// ErrLocalIngestLimit is returned when accepting another chunk would take this
// node past its configured local ingest limit. See issue #326.
var ErrLocalIngestLimit = errors.New("storer: local ingest limit reached")

// ErrLocalIngestDuplicate is returned by LocalIngestSession.Done when this node
// already holds the resulting reference, so there is nothing to store and the
// caller should clean up and report success.
//
// It stands for pinning.ErrDuplicatePinCollection, which lives in an internal
// package that pkg/api cannot import.
var ErrLocalIngestDuplicate = errors.New("storer: content already held")

// ErrLocalIngestSessionClosed is returned when a session is used after it has
// been finished by Done or Cleanup. Claiming room on a finished session would
// take it from the node-wide limit with nothing left to give it back.
var ErrLocalIngestSessionClosed = errors.New("storer: local ingest session is closed")

var (
	errInvalidLocalIngestAddr = errors.New("storer: invalid local ingest address")
	errInvalidLocalIngestSize = errors.New("storer: invalid local ingest item size")
)

// LocalIngestSession is a pinning session whose Done carries the number of
// distinct chunks stored, so the count and the pinning collection commit in a
// single transaction.
//
// It exists because PutterSession.Done takes only an address. Writing the count
// after Done returns would let a crash in between leave a committed collection
// with no record, so usage would undercount for good and that much of the limit
// would be bypassed permanently. Writing it before would leave an orphaned
// record when Close fails. Neither is repairable from disk.
type LocalIngestSession interface {
	storage.Putter

	// Reserve claims room for n more distinct chunks against the node-wide
	// limit, returning ErrLocalIngestLimit when the committed total plus
	// every claim in flight would cross it.
	//
	// A claim rather than a check, because two ingests interleave: checking
	// a committed total lets both pass and together exceed the limit, and
	// incrementing the committed total as chunks arrive inflates it for good
	// on every refusal, every abandoned request and every duplicate.
	Reserve(n uint64) error

	// Done commits the collection and the local ingest record together.
	// chunks is the number of distinct chunk addresses stored, which is what
	// the claim is converted into.
	Done(root swarm.Address, chunks uint64) error

	// Cleanup removes what the session wrote and releases its claim.
	Cleanup() error
}

// localIngestState is the node-wide accounting for locally ingested content.
//
// The total lives in memory and is rebuilt at startup from the index. A
// singleton total item on disk would be a second transactional write, and so a
// second way for two records to disagree.
//
// Lock order: this mutex is taken after uploadsLock and never before, because
// DB.DeletePin holds uploadsLock for its whole body and takes this mutex inside
// it. Equivalently, and this is the rule the ingest path follows: this mutex is
// never held across a call into the storer.
type localIngestState struct {
	mu        sync.Mutex
	committed uint64
	reserved  uint64
	limit     uint64
	// gauge mirrors committed. Every method that changes committed publishes
	// it, because a gauge written on only one of the paths that move a number
	// reports a figure the operator cannot act on.
	gauge prometheus.Gauge
	// published is the last value handed to the gauge. Reading a prometheus
	// gauge back needs a dependency this module does not carry, so tests
	// assert on this instead; it is written in the same place and so still
	// catches a path that changes the total without publishing it.
	published uint64
}

// publish reports the committed total. Called with mu held.
func (s *localIngestState) publish() {
	s.published = s.committed
	if s.gauge != nil {
		s.gauge.Set(float64(s.committed))
	}
}

func (s *localIngestState) reserve(n uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.limit > 0 && s.committed+s.reserved+n > s.limit {
		return ErrLocalIngestLimit
	}
	s.reserved += n
	return nil
}

func (s *localIngestState) release(n uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if n > s.reserved {
		n = s.reserved
	}
	s.reserved -= n
}

// commit turns a session's claim into usage. The two numbers are equal in
// normal operation; taking both keeps a mismatch from corrupting the total.
func (s *localIngestState) commit(claimed, chunks uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if claimed > s.reserved {
		claimed = s.reserved
	}
	s.reserved -= claimed
	s.committed += chunks
	s.publish()
}

func (s *localIngestState) subtract(n uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if n > s.committed {
		n = s.committed
	}
	s.committed -= n
	s.publish()
}

func (s *localIngestState) setTotal(n uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.committed = n
	s.publish()
}

func (s *localIngestState) usage() (committed, reserved, limit uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.committed, s.reserved, s.limit
}

// LocalIngestUsage reports the committed chunk count, the amount claimed by
// ingests in flight, and the configured limit. A limit of 0 means no limit.
//
// The committed count is an upper bound on disk rather than a measurement of
// it: chunkstore.Put increments a refcount for a chunk this node already holds,
// so a counted chunk may have cost no new disk at all.
func (db *DB) LocalIngestUsage() (committed, reserved, limit uint64) {
	return db.localIngest.usage()
}

type localIngestSession struct {
	storage.Putter

	state   *localIngestState
	done    func(root swarm.Address, chunks uint64) error
	cleanup func() error

	mu       sync.Mutex
	reserved uint64
	closed   bool
}

var _ LocalIngestSession = (*localIngestSession)(nil)

// Reserve holds the session mutex across the node-wide claim. Splitting the two
// would let a Done or Cleanup land in between: the claim would be committed
// node-wide while the session it belongs to had already given up everything it
// was holding, so those units would never be released and the limit would
// shrink for the life of the process.
func (s *localIngestSession) Reserve(n uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return ErrLocalIngestSessionClosed
	}
	if err := s.state.reserve(n); err != nil {
		return err
	}
	s.reserved += n
	return nil
}

// take removes and returns the session's outstanding claim and reports whether
// the session had already been finished. Done and Cleanup both go through it,
// so a claim is released once however the two are ordered.
func (s *localIngestSession) take() (claimed uint64, alreadyClosed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return 0, true
	}
	claimed = s.reserved
	s.reserved = 0
	s.closed = true
	return claimed, false
}

func (s *localIngestSession) Done(root swarm.Address, chunks uint64) error {
	claimed, alreadyClosed := s.take()
	if alreadyClosed {
		// Running the commit twice would write the record a second time and
		// take Close down a path it has already been through.
		return ErrLocalIngestSessionClosed
	}

	if err := s.done(root, chunks); err != nil {
		// The transaction did not commit, so the claim never became usage.
		// The caller cleans up, which finds nothing left to release.
		s.state.release(claimed)
		return err
	}

	s.state.commit(claimed, chunks)
	return nil
}

func (s *localIngestSession) Cleanup() error {
	// The claim goes back before the storer is touched: the state mutex must
	// not be held across a call that takes uploadsLock.
	if claimed, alreadyClosed := s.take(); !alreadyClosed {
		s.state.release(claimed)
	}
	return s.cleanup()
}

// NewLocalIngestCollection is NewCollection with two additions: the session
// records how many distinct chunks it stored, in the same transaction that
// commits the collection, and it claims that room against the node-wide limit.
//
// Everything else about the collection is ordinary. The chunks carry no stamp,
// nothing reaches the pusher, and the result is a pinning collection that the
// existing unpin route removes.
func (db *DB) NewLocalIngestCollection(ctx context.Context) (LocalIngestSession, error) {
	var (
		pinningPutter internal.PutterCloserWithReference
		err           error
	)
	err = db.storage.Run(ctx, func(store transaction.Store) error {
		pinningPutter, err = pinstore.NewCollection(store.IndexStore())
		if err != nil {
			return fmt.Errorf("pinstore.NewCollection: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return &localIngestSession{
		Putter: putterWithMetrics{
			storage.PutterFunc(
				func(ctx context.Context, chunk swarm.Chunk) error {
					unlock := db.Lock(uploadsLock)
					defer unlock()
					return db.storage.Run(ctx, func(s transaction.Store) error {
						return pinningPutter.Put(ctx, s, chunk)
					})
				},
			),
			db.metrics,
			"localingest",
		},
		state: &db.localIngest,
		done: func(root swarm.Address, chunks uint64) error {
			unlock := db.Lock(uploadsLock)
			defer unlock()
			err := db.storage.Run(ctx, func(s transaction.Store) error {
				// The record is written BEFORE Close, and the order is not
				// cosmetic. Close marks the collection putter closed in
				// memory before it writes, and a later Cleanup on a closed
				// putter returns nil without deleting anything. Writing the
				// record first means a failure here leaves Close unrun, so
				// Cleanup still removes the chunks. Both writes are in one
				// transaction, so durability does not depend on the order.
				if err := s.IndexStore().Put(&localIngestItem{Addr: root, Chunks: chunks}); err != nil {
					return err
				}
				return pinningPutter.Close(s.IndexStore(), root)
			})
			// Close refuses a root this node already holds, and refuses it
			// before writing anything, so the transaction rolls back whole.
			if errors.Is(err, pinstore.ErrDuplicatePinCollection) {
				return ErrLocalIngestDuplicate
			}
			return err
		},
		cleanup: func() error {
			unlock := db.Lock(uploadsLock)
			defer unlock()
			return pinningPutter.Cleanup(db.storage)
		},
	}, nil
}

// rebuildLocalIngestTotal recomputes the usage total from the index and drops
// any record whose root is no longer pinned.
//
// It runs in New, before the API is built and before the listener opens, so
// there is no window in which the route serves against an unbuilt total.
//
// **It never fails startup.** What it rebuilds is an in-memory accounting
// figure for a feature that is off by default, and the worst consequence of
// getting it wrong is a limit that binds early or late. Refusing to start a
// node over that would be out of proportion, so a record that cannot be read
// is logged and skipped and the node comes up with a total that is short by
// that record. Contrast pinstore.CleanupDirty beside it, which is fatal
// because it repairs real on-disk state.
//
// The dropping repairs one case and one only: a crash after DB.DeletePin
// removed the collection but before it removed the record. A crash inside
// pinstore.DeletePin is not covered, because that deletes the collection chunks
// in many independent transactions and the root last, so the root still answers
// HasPin while its chunks are gone. That is upstream behaviour and CleanupDirty
// does not see it either, since DeletePin writes no dirty marker.
func (db *DB) rebuildLocalIngestTotal(ctx context.Context) {
	var (
		total    uint64
		skipped  int
		orphaned []*localIngestItem
	)

	err := db.storage.IndexStore().Iterate(
		storage.Query{Factory: func() storage.Item { return new(localIngestItem) }},
		func(r storage.Result) (bool, error) {
			item, ok := r.Entry.(*localIngestItem)
			if !ok {
				skipped++
				return false, nil
			}

			has, err := pinstore.HasPin(db.storage.IndexStore(), item.Addr)
			if err != nil {
				// Skip this record rather than stopping: one unreadable
				// pin must not cost the whole total.
				db.logger.Error(err, "local ingest: checking whether a recorded reference is still pinned", "reference", item.Addr)
				skipped++
				return false, nil
			}
			if !has {
				orphaned = append(orphaned, item.Clone().(*localIngestItem))
				return false, nil
			}

			total += item.Chunks
			return false, nil
		},
	)
	if err != nil {
		// The total is short by whatever the iteration did not reach. Say so
		// plainly rather than reporting a figure as though it were complete.
		db.logger.Error(err, "local ingest: rebuilding the usage total, the figure may be short and the limit may bind late")
	}

	for _, item := range orphaned {
		if err := db.storage.Run(ctx, func(s transaction.Store) error {
			return s.IndexStore().Delete(item)
		}); err != nil {
			db.logger.Error(err, "local ingest: dropping the record of already unpinned content", "reference", item.Addr)
		}
	}

	db.localIngest.setTotal(total)

	if len(orphaned) > 0 || skipped > 0 {
		db.logger.Info("local ingest: usage total rebuilt", "chunks", total, "dropped", len(orphaned), "skipped", skipped)
	}
}

// dropLocalIngestRecord removes the record for a root that has just been
// unpinned and takes its count off the total. A root with no record is an
// ordinary pin, which is the common case and not an error.
//
// It is called after pinstore.DeletePin has returned, and the order is not
// interchangeable: removing the record first would leave a root that still
// answers HasPin with nothing left to detect it.
//
// It reports its failures rather than returning them, because by the time it
// runs the unpin has already succeeded. Failing the request would tell the
// caller the pin is still there when it is gone, and a retry cannot succeed
// because the collection no longer exists. A record left behind is repaired by
// the startup pass.
func (db *DB) dropLocalIngestRecord(ctx context.Context, root swarm.Address) {
	item := &localIngestItem{Addr: root}

	if err := db.storage.IndexStore().Get(item); err != nil {
		if !errors.Is(err, storage.ErrNotFound) {
			db.logger.Error(err, "local ingest: reading the record of unpinned content", "reference", root)
		}
		return
	}

	if err := db.storage.Run(ctx, func(s transaction.Store) error {
		return s.IndexStore().Delete(item)
	}); err != nil {
		db.logger.Error(err, "local ingest: deleting the record of unpinned content, the usage figure stays high until the next restart", "reference", root)
		return
	}

	db.localIngest.subtract(item.Chunks)
}

// localIngestItemSize is sized for a 64-byte encrypted reference, as
// pinCollectionItem is, because the route honours Swarm-Encrypt and a 32-byte
// assumption would truncate every encrypted reference silently.
const localIngestItemSize = encryption.ReferenceSize + 8

var emptyLocalIngestKey = make([]byte, encryption.KeyLength)

var _ storage.Item = (*localIngestItem)(nil)

// localIngestItem records that a pinning collection was created by a local
// ingest, and how many distinct chunks it holds. Its absence means "not
// locally ingested", which is true of every collection that existed before
// this feature, so there is no migration.
type localIngestItem struct {
	Addr   swarm.Address
	Chunks uint64
}

func (i *localIngestItem) ID() string { return i.Addr.ByteString() }

func (localIngestItem) Namespace() string { return "localIngestItem" }

func (i *localIngestItem) Marshal() ([]byte, error) {
	if i.Addr.IsZero() {
		return nil, errInvalidLocalIngestAddr
	}

	buf := make([]byte, localIngestItemSize)
	copy(buf[:encryption.ReferenceSize], i.Addr.Bytes())
	binary.LittleEndian.PutUint64(buf[encryption.ReferenceSize:], i.Chunks)
	return buf, nil
}

func (i *localIngestItem) Unmarshal(buf []byte) error {
	if len(buf) != localIngestItemSize {
		return errInvalidLocalIngestSize
	}

	ni := new(localIngestItem)
	if bytes.Equal(buf[swarm.HashSize:encryption.ReferenceSize], emptyLocalIngestKey) {
		ni.Addr = swarm.NewAddress(buf[:swarm.HashSize]).Clone()
	} else {
		ni.Addr = swarm.NewAddress(buf[:encryption.ReferenceSize]).Clone()
	}
	ni.Chunks = binary.LittleEndian.Uint64(buf[encryption.ReferenceSize:])
	*i = *ni
	return nil
}

func (i *localIngestItem) Clone() storage.Item {
	if i == nil {
		return nil
	}
	return &localIngestItem{
		Addr:   i.Addr.Clone(),
		Chunks: i.Chunks,
	}
}

func (i localIngestItem) String() string {
	return storageutil.JoinFields(i.Namespace(), i.ID())
}
