// Copyright 2023 The Swarm Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package mockstorer

import (
	"context"
	"sync"
	"time"

	"github.com/ethersphere/bee/v2/pkg/pusher"
	"github.com/ethersphere/bee/v2/pkg/storage"
	"github.com/ethersphere/bee/v2/pkg/storage/inmemchunkstore"
	"github.com/ethersphere/bee/v2/pkg/storer"
	"github.com/ethersphere/bee/v2/pkg/swarm"
	"go.uber.org/atomic"
)

// now returns the current time.Time; used in testing.
var now = time.Now

type mockStorer struct {
	chunkStore     storage.ChunkStore
	mu             sync.Mutex
	pins           []swarm.Address
	sessionID      atomic.Uint64
	activeSessions map[uint64]*storer.SessionInfo
	chunkPushC     chan *pusher.Op
	debugInfo      storer.Info

	storageRadius  uint8
	committedDepth uint8

	// Local ingest accounting (issue #326), guarded by mu.
	localIngestLimit     uint64
	localIngestCommitted uint64
	localIngestReserved  uint64
	// localIngestSessions counts sessions created, so a test can tell a
	// refusal that happened before the body was read from one that happened
	// while reading it.
	localIngestSessions uint64
}

type putterSession struct {
	chunkStore storage.Putter
	done       func(swarm.Address) error
}

func (p *putterSession) Put(ctx context.Context, ch swarm.Chunk) error {
	return p.chunkStore.Put(ctx, ch)
}

func (p *putterSession) Done(address swarm.Address) error {
	if p.done != nil {
		return p.done(address)
	}
	return nil
}

func (p *putterSession) Cleanup() error { return nil }

// localIngestSession is the mock's LocalIngestSession (issue #326). It tracks
// claims well enough for the handler tests; the behaviour that needs a real
// database, such as the duplicate and dirty-collection paths, is tested in
// package storer_test instead.
type localIngestSession struct {
	chunkStore storage.Putter
	store      *mockStorer

	mu       sync.Mutex
	reserved uint64
	closed   bool
}

func (p *localIngestSession) Put(ctx context.Context, ch swarm.Chunk) error {
	return p.chunkStore.Put(ctx, ch)
}

func (p *localIngestSession) Reserve(n uint64) error {
	p.store.mu.Lock()
	if p.store.localIngestLimit > 0 &&
		p.store.localIngestCommitted+p.store.localIngestReserved+n > p.store.localIngestLimit {
		p.store.mu.Unlock()
		return storer.ErrLocalIngestLimit
	}
	p.store.localIngestReserved += n
	p.store.mu.Unlock()

	p.mu.Lock()
	p.reserved += n
	p.mu.Unlock()
	return nil
}

func (p *localIngestSession) take() uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()

	claimed := p.reserved
	p.reserved = 0
	p.closed = true
	return claimed
}

func (p *localIngestSession) Done(address swarm.Address, chunks uint64) error {
	claimed := p.take()

	p.store.mu.Lock()
	defer p.store.mu.Unlock()

	for _, pin := range p.store.pins {
		if pin.Equal(address) {
			p.store.localIngestReserved -= claimed
			return storer.ErrLocalIngestDuplicate
		}
	}

	p.store.localIngestReserved -= claimed
	p.store.localIngestCommitted += chunks
	p.store.pins = append(p.store.pins, address)
	return nil
}

func (p *localIngestSession) Cleanup() error {
	claimed := p.take()

	p.store.mu.Lock()
	defer p.store.mu.Unlock()

	if claimed > p.store.localIngestReserved {
		claimed = p.store.localIngestReserved
	}
	p.store.localIngestReserved -= claimed
	return nil
}

func (m *mockStorer) NewLocalIngestCollection(_ context.Context) (storer.LocalIngestSession, error) {
	m.mu.Lock()
	m.localIngestSessions++
	m.mu.Unlock()

	return &localIngestSession{chunkStore: m.chunkStore, store: m}, nil
}

// StoredChunkCount counts the distinct chunks in the mock's chunk store, which
// keys by address and so deduplicates. It gives a test a count arrived at
// independently of whatever the code under test reports.
func (m *mockStorer) StoredChunkCount(ctx context.Context) (uint64, error) {
	var n uint64
	err := m.chunkStore.Iterate(ctx, func(swarm.Chunk) (bool, error) {
		n++
		return false, nil
	})
	return n, err
}

// LocalIngestSessionCount reports how many local ingest sessions were created.
func (m *mockStorer) LocalIngestSessionCount() uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.localIngestSessions
}

func (m *mockStorer) LocalIngestUsage() (committed, reserved, limit uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.localIngestCommitted, m.localIngestReserved, m.localIngestLimit
}

// SetLocalIngestLimit sets the mock's limit in chunks, so a test can exercise
// the refusal path. Zero means no limit.
func (m *mockStorer) SetLocalIngestLimit(limit uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.localIngestLimit = limit
}

// New returns a mock storer implementation that is designed to be used for the
// unit tests.
func New() *mockStorer {
	return &mockStorer{
		chunkStore:     inmemchunkstore.New(),
		chunkPushC:     make(chan *pusher.Op),
		activeSessions: make(map[uint64]*storer.SessionInfo),
	}
}

func NewWithChunkStore(cs storage.ChunkStore) *mockStorer {
	return &mockStorer{
		chunkStore:     cs,
		chunkPushC:     make(chan *pusher.Op),
		activeSessions: make(map[uint64]*storer.SessionInfo),
	}
}

func NewWithDebugInfo(info storer.Info) *mockStorer {
	st := New()
	st.debugInfo = info
	return st
}

func (m *mockStorer) Upload(_ context.Context, pin bool, tagID uint64) (storer.PutterSession, error) {
	return &putterSession{
		chunkStore: m.chunkStore,
		done: func(address swarm.Address) error {
			m.mu.Lock()
			defer m.mu.Unlock()

			if pin {
				m.pins = append(m.pins, address)
			}
			if session, ok := m.activeSessions[tagID]; ok {
				session.Address = address
			}
			return nil
		},
	}, nil
}

func (m *mockStorer) NewSession() (storer.SessionInfo, error) {
	session := &storer.SessionInfo{
		TagID:     m.sessionID.Inc(),
		StartedAt: now().UnixNano(),
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.activeSessions[session.TagID] = session

	return *session, nil
}

func (m *mockStorer) Session(tagID uint64) (storer.SessionInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	session, ok := m.activeSessions[tagID]
	if !ok {
		return storer.SessionInfo{}, storage.ErrNotFound
	}
	return *session, nil
}

func (m *mockStorer) DeleteSession(tagID uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.activeSessions[tagID]; !ok {
		return storage.ErrNotFound
	}
	delete(m.activeSessions, tagID)
	return nil
}

func (m *mockStorer) ListSessions(offset, limit int) ([]storer.SessionInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	sessions := make([]storer.SessionInfo, 0, len(m.activeSessions))
	for _, v := range m.activeSessions {
		sessions = append(sessions, *v)
	}
	return sessions, nil
}

func (m *mockStorer) DeletePin(_ context.Context, address swarm.Address) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for idx, p := range m.pins {
		if p.Equal(address) {
			m.pins = append(m.pins[:idx], m.pins[idx+1:]...)
			break
		}
	}
	return nil
}

func (m *mockStorer) Pins() ([]swarm.Address, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	pins := make([]swarm.Address, 0, len(m.pins))
	for _, p := range m.pins {
		pins = append(pins, p.Clone())
	}
	return pins, nil
}

func (m *mockStorer) HasPin(address swarm.Address) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.pins {
		if p.Equal(address) {
			return true, nil
		}
	}
	return false, nil
}

func (m *mockStorer) NewCollection(ctx context.Context) (storer.PutterSession, error) {
	return &putterSession{
		chunkStore: m.chunkStore,
		done: func(address swarm.Address) error {
			m.mu.Lock()
			defer m.mu.Unlock()

			m.pins = append(m.pins, address)
			return nil
		},
	}, nil
}

func (m *mockStorer) Lookup() storage.Getter {
	return m.chunkStore
}

func (m *mockStorer) Cache() storage.Putter {
	return m.chunkStore
}

func (m *mockStorer) DirectUpload() storer.PutterSession {
	return &putterSession{
		chunkStore: storage.PutterFunc(
			func(ctx context.Context, ch swarm.Chunk) error {
				op := &pusher.Op{Chunk: ch, Err: make(chan error, 1), Direct: true}
				select {
				case <-ctx.Done():
					return ctx.Err()
				case m.chunkPushC <- op:
					return nil
				}
			}),
	}
}

func (m *mockStorer) Download(_ bool) storage.Getter {
	return m.chunkStore
}

func (m *mockStorer) PusherFeed() <-chan *pusher.Op {
	return m.chunkPushC
}

func (m *mockStorer) ChunkStore() storage.ReadOnlyChunkStore {
	return m.chunkStore
}

func (m *mockStorer) StorageRadius() uint8 { return m.storageRadius }

func (m *mockStorer) CommittedDepth() uint8 { return m.committedDepth }

func (m *mockStorer) CapacityDoubling() uint8 {
	return m.committedDepth - m.storageRadius
}

func (m *mockStorer) IsWithinStorageRadius(_ swarm.Address) bool { return true }
func (m *mockStorer) IsSampling() bool                           { return false }

func (m *mockStorer) ReserveSizeWithinRadius() uint64 { return 0 }

func (m *mockStorer) ProbeSample(_ context.Context, _ []byte, _ uint8, k int) (storer.ProbeStats, error) {
	return storer.ProbeStats{K: k}, nil
}

func (m *mockStorer) DebugInfo(_ context.Context) (storer.Info, error) {
	return m.debugInfo, nil
}

func (m *mockStorer) NeighborhoodsStat(ctx context.Context) ([]*storer.NeighborhoodStat, error) {
	return nil, nil
}

func (m *mockStorer) Put(ctx context.Context, ch swarm.Chunk) error {
	return m.chunkStore.Put(ctx, ch)
}

func (m *mockStorer) SetStorageRadius(radius uint8) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.storageRadius = radius
}

func (m *mockStorer) SetCommittedDepth(depth uint8) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.committedDepth = depth
}
