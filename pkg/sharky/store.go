// Copyright 2021 The Swarm Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sharky

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strconv"
	"sync"

	"github.com/hashicorp/go-multierror"
)

var (
	// ErrTooLong returned by Write if the blob length exceeds the max blobsize.
	ErrTooLong = errors.New("data too long")
	// ErrQuitting returned by Write when the store is Closed before the write completes.
	ErrQuitting = errors.New("quitting")
	// ErrInvalidLocation returned when a Location cannot be decoded from a buffer.
	ErrInvalidLocation = errors.New("invalid location")
)

// Store models the sharded fix-length blobstore
// Design provides lockless sharding:
// - shard choice responding to backpressure by running operation
// - read prioritisation over writing
// - free slots allow write
type Store struct {
	maxDataSize int             // max length of blobs
	writes      chan write      // shared write operations channel
	shards      []*shard        // shards
	wg          *sync.WaitGroup // count started operations
	quit        chan struct{}   // quit channel
	metrics     metrics
}

// New constructs a sharded blobstore
// arguments:
// - base directory string
// - shard count - positive integer < 256 - cannot be zero or expect panic
// - shard size - positive integer multiple of 8 - for others expect undefined behaviour
// - maxDataSize - positive integer representing the maximum blob size to be stored
func New(basedir fs.FS, shardCnt int, maxDataSize int) (*Store, error) {
	store := &Store{
		maxDataSize: maxDataSize,
		writes:      make(chan write),
		shards:      make([]*shard, shardCnt),
		wg:          &sync.WaitGroup{},
		quit:        make(chan struct{}),
		metrics:     newMetrics(),
	}
	for i := range store.shards {
		s, err := store.create(uint8(i), maxDataSize, basedir)
		if err != nil {
			return nil, err
		}
		store.shards[i] = s
	}
	store.metrics.ShardCount.Set(float64(len(store.shards)))

	return store, nil
}

// Close closes each shard and return incidental errors from each shard
func (s *Store) Close() error {
	close(s.quit)
	err := new(multierror.Error)
	for _, sh := range s.shards {
		err = multierror.Append(err, sh.close())
	}

	return err.ErrorOrNil()
}

// create creates a new shard with index, max capacity limit, file within base directory
func (s *Store) create(index uint8, maxDataSize int, basedir fs.FS) (*shard, error) {
	file, err := basedir.Open(fmt.Sprintf("shard_%03d", index))
	if err != nil {
		return nil, err
	}
	ffile, err := basedir.Open(fmt.Sprintf("free_%03d", index))
	if err != nil {
		return nil, err
	}
	sl := newSlots(ffile.(sharkyFile), s.wg)
	err = sl.load()
	if err != nil {
		return nil, err
	}
	sh := &shard{
		writes:      s.writes,
		index:       index,
		maxDataSize: maxDataSize,
		file:        file.(sharkyFile),
		slots:       sl,
		quit:        s.quit,
	}
	terminated := make(chan struct{})
	s.wg.Go(func() {
		sh.process()
		close(terminated)
	})
	s.wg.Go(func() {
		sl.process(terminated)
	})
	return sh, nil
}

// Read reads the content of the blob found at location into the byte buffer given
// The location is assumed to be obtained by an earlier Write call storing the blob
//
// The read runs directly in the calling goroutine rather than being handed to the
// shard's write actor, so several reads on the same shard proceed concurrently
// instead of queueing behind it. ReadAt compiles to pread, which does not use or
// mutate the shared file offset and is safe for concurrent use on one descriptor.
// The shard's lifecycle guard makes this safe against Close: the read holds the
// read lock for the duration of its ReadAt, and close takes the write lock before
// closing the file, so the descriptor cannot be closed and reused by the OS while a
// ReadAt is in flight. This is the correctness the first attempt lacked (#8, #66).
//
// The caller owns buf and Read is synchronous, so the buffer is only touched for
// the duration of the call; no buffer outlives its read.
func (s *Store) Read(ctx context.Context, loc Location, buf []byte) (err error) {
	if int(loc.Shard) >= len(s.shards) {
		return ErrShardNotFound
	}
	sh := s.shards[loc.Shard]

	sh.closeMu.RLock()
	defer sh.closeMu.RUnlock()

	// On shutdown, return ErrQuitting without touching the file, matching the
	// previous behaviour that callers distinguish. Checked under the read lock so
	// the result is consistent with close, which sets closed under the write lock.
	select {
	case <-s.quit:
		return ErrQuitting
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if sh.closed {
		return ErrQuitting
	}

	s.metrics.TotalReadCalls.Inc()
	err = sh.read(read{buf: buf[:loc.Length], slot: loc.Slot})
	if err != nil {
		s.metrics.TotalReadCallsErr.Inc()
	}
	return err
}

// Write stores a new blob and returns its location to be used as a reference
// It can be given to a Read call to return the stored blob.
func (s *Store) Write(ctx context.Context, data []byte) (loc Location, err error) {
	if len(data) > s.maxDataSize {
		return loc, ErrTooLong
	}
	s.wg.Add(1)
	defer s.wg.Done()

	c := make(chan entry, 1) // buffer the channel to avoid blocking in shard.process on quit or context done

	select {
	case s.writes <- write{data, c}:
		s.metrics.TotalWriteCalls.Inc()
	case <-s.quit:
		return loc, ErrQuitting
	case <-ctx.Done():
		return loc, ctx.Err()
	}

	select {
	case e := <-c:
		if e.err == nil {
			shard := strconv.Itoa(int(e.loc.Shard))
			s.metrics.CurrentShardSize.WithLabelValues(shard).Inc()
			s.metrics.ShardFragmentation.WithLabelValues(shard).Add(float64(s.maxDataSize - int(e.loc.Length)))
			s.metrics.LastAllocatedShardSlot.WithLabelValues(shard).Set(float64(e.loc.Slot))
		} else {
			s.metrics.TotalWriteCallsErr.Inc()
		}
		return e.loc, e.err
	case <-s.quit:
		return loc, ErrQuitting
	case <-ctx.Done():
		return loc, ctx.Err()
	}
}

// Release gives back the slot to the shard
// From here on the slot can be reused and overwritten
// Release is meant to be called when an entry in the upstream db is removed
// Note that releasing is not safe for obfuscating earlier content, since
// even after reuse, the slot may be used by a very short blob and leaves the
// rest of the old blob bytes untouched
func (s *Store) Release(ctx context.Context, loc Location) error {
	if int(loc.Shard) >= len(s.shards) {
		return ErrShardNotFound
	}
	sh := s.shards[loc.Shard]
	err := sh.release(ctx, loc.Slot)
	s.metrics.TotalReleaseCalls.Inc()
	if err == nil {
		shard := strconv.Itoa(int(sh.index))
		s.metrics.CurrentShardSize.WithLabelValues(shard).Dec()
		s.metrics.ShardFragmentation.WithLabelValues(shard).Sub(float64(s.maxDataSize - int(loc.Length)))
		s.metrics.LastReleasedShardSlot.WithLabelValues(shard).Set(float64(loc.Slot))
	} else {
		s.metrics.TotalReleaseCallsErr.Inc()
	}
	return err
}
