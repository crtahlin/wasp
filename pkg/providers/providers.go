// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package providers

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/ethersphere/bee/v2/pkg/bzz"
	"github.com/ethersphere/bee/v2/pkg/crypto"
	"github.com/ethersphere/bee/v2/pkg/log"
	"github.com/ethersphere/bee/v2/pkg/postage"
	"github.com/ethersphere/bee/v2/pkg/soc"
	"github.com/ethersphere/bee/v2/pkg/storage"
	"github.com/ethersphere/bee/v2/pkg/swarm"
)

// loggerName is the tree path name of the logger for this package.
const loggerName = "providers"

const (
	// lookupReadDeadline bounds each slot and record read of a lookup, so a
	// chunk that was never written does not hold the lookup up.
	lookupReadDeadline = 2 * time.Second
	// lookupCandidates is the most candidate records a lookup verifies.
	lookupCandidates = 16
	// lookupCacheTTL is how long a lookup result, empty or not, is reused.
	lookupCacheTTL = 10 * time.Minute
	// lookupCacheMax is the number of cached keys above which expired
	// entries are dropped.
	lookupCacheMax = 1024
	// readBackDelay is how long after writing its pointer entry a provider
	// checks that the entry is still there.
	readBackDelay = time.Minute
	// maxRewrites is how often a lost pointer entry is written again in one
	// window.
	maxRewrites = 3
	// preWrite is how long before a window starts that its record is
	// written, so that readers can read the current window only.
	preWrite = time.Hour
	// tickInterval is how often announcements are checked.
	tickInterval = time.Minute

	announcedPrefix = "providers_announced_"
)

// Fetcher retrieves a chunk from the network without reading the local store.
type Fetcher interface {
	RetrieveChunk(ctx context.Context, addr, sourcePeerAddr swarm.Address) (swarm.Chunk, error)
}

// PutterSession pushes stamped chunks to the network.
type PutterSession interface {
	Put(context.Context, swarm.Chunk) error
	Done(swarm.Address) error
	Cleanup() error
}

// Adder receives the overlays of providers found by a lookup.
type Adder interface {
	Add(peers ...swarm.Address)
}

// Options holds what the service needs from the node.
type Options struct {
	Logger    log.Logger
	NetworkID uint64
	// Overlay is this node's overlay; a lookup never offers the node itself.
	Overlay swarm.Address
	// Signer is the node's signer; records are signed with it.
	Signer crypto.Signer
	// Getter reads chunks for lookups: the local store first, then the
	// network.
	Getter storage.Getter
	// Fetcher reads chunks from the network only. The read-back uses it so
	// that it does not see the node's own local copy.
	Fetcher Fetcher
	// Uploader starts a session that pushes stamped chunks to the network.
	Uploader func() PutterSession
	// Stamper returns a stamper for a batch and a function that saves the
	// batch's issuer state.
	Stamper func(batchID []byte) (postage.Stamper, func() error, error)
	// Address returns this node's address, freshly signed, with at most
	// MaxUnderlays underlays.
	Address func() (*bzz.Address, error)
	// Connect connects to a provider found by a lookup, without forcing past
	// a full bin. It may be nil.
	Connect func(ctx context.Context, addr *bzz.Address) error
	// Resolve returns a peer's address from the address book, for dialing
	// an overlay named in a download hint. It may be nil.
	Resolve func(overlay swarm.Address) (*bzz.Address, error)
	// Store keeps the announced content keys across restarts.
	Store storage.StateStorer
}

// Announcement is a content key this node announces.
type Announcement struct {
	Key     []byte   `json:"key"`
	BatchID []byte   `json:"batchID"`
	Written []uint64 `json:"written"`
}

// Service announces the content this node provides and looks up providers of
// content for downloads.
type Service struct {
	opts   Options
	logger log.Logger
	owner  []byte

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu     sync.Mutex
	now    func() time.Time
	cache  map[string]cacheEntry
	checks []readBack
	warned map[string]uint64
	closed bool

	// annMu keeps a withdrawal from racing the loop's save of the same key.
	annMu sync.Mutex
}

type cacheEntry struct {
	records []*Record
	expires time.Time
}

// readBack is a pending check that this node's pointer entry survived.
type readBack struct {
	key     []byte
	batchID []byte
	window  uint64
	due     time.Time
	tries   int
}

// New returns a service. Start runs its announcement loop.
func New(o Options) (*Service, error) {
	owner, err := o.Signer.EthereumAddress()
	if err != nil {
		return nil, fmt.Errorf("owner address: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{
		opts:   o,
		logger: o.Logger.WithName(loggerName).Register(),
		owner:  owner.Bytes(),
		ctx:    ctx,
		cancel: cancel,
		now:    time.Now,
		cache:  make(map[string]cacheEntry),
		warned: make(map[string]uint64),
	}, nil
}

// Start runs the loop that keeps announcements written and checks pointer
// entries.
func (s *Service) Start() {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(tickInterval)
		defer ticker.Stop()
		for {
			s.runOnce(s.ctx)
			select {
			case <-s.ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

// Close stops the loop and any running lookups.
func (s *Service) Close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.cancel()
	s.wg.Wait()
	return nil
}

// goBackground runs f in a goroutine that Close waits for, unless the service
// is already closed.
func (s *Service) goBackground(f func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		f()
	}()
}

// Announce makes this node announce content key k, writing records and
// pointer entries stamped from batchID. It writes the current window at once
// and returns its error; later windows are written by the loop.
func (s *Service) Announce(ctx context.Context, k, batchID []byte) error {
	w := WindowAt(s.clock())
	if err := s.publish(ctx, k, batchID, w); err != nil {
		return err
	}
	a := Announcement{Key: k, BatchID: batchID, Written: []uint64{w}}
	s.publishDue(ctx, &a)

	s.annMu.Lock()
	defer s.annMu.Unlock()
	return s.save(a)
}

// Withdraw stops announcing content key k. The node stays listed until the
// last window it wrote ends.
func (s *Service) Withdraw(k []byte) error {
	s.annMu.Lock()
	defer s.annMu.Unlock()

	s.mu.Lock()
	s.checks = slices.DeleteFunc(s.checks, func(c readBack) bool { return string(c.key) == string(k) })
	delete(s.warned, string(k))
	s.mu.Unlock()
	return s.opts.Store.Delete(announcedKey(k))
}

// isAnnounced reports whether content key k is still announced.
func (s *Service) isAnnounced(k []byte) bool {
	var a Announcement
	return s.opts.Store.Get(announcedKey(k), &a) == nil
}

// Announced returns the content keys this node announces.
func (s *Service) Announced() ([]Announcement, error) {
	var out []Announcement
	err := s.opts.Store.Iterate(announcedPrefix, func(_, value []byte) (bool, error) {
		var a Announcement
		if err := json.Unmarshal(value, &a); err != nil {
			return false, fmt.Errorf("decode announcement: %w", err)
		}
		out = append(out, a)
		return false, nil
	})
	return out, err
}

// Lookup returns the verified providers of content key k in the current
// window. Results, empty or not, are cached for lookupCacheTTL.
func (s *Service) Lookup(ctx context.Context, k []byte) ([]*Record, error) {
	// only plain references have records; see checkKey
	if len(k) != swarm.HashSize {
		return nil, nil
	}
	key := string(k)

	s.mu.Lock()
	now := s.now()
	if e, ok := s.cache[key]; ok && now.Before(e.expires) {
		s.mu.Unlock()
		return e.records, nil
	}
	s.mu.Unlock()

	w := WindowAt(now)
	records := s.verify(ctx, k, w, s.candidates(ctx, k, w))
	if err := ctx.Err(); err != nil {
		// an interrupted lookup is not cached
		return records, err
	}

	s.mu.Lock()
	if len(s.cache) >= lookupCacheMax {
		for ck, e := range s.cache {
			if !now.Before(e.expires) {
				delete(s.cache, ck)
			}
		}
		// still full of live entries: drop any, so the cache stays bounded
		for ck := range s.cache {
			if len(s.cache) < lookupCacheMax {
				break
			}
			delete(s.cache, ck)
		}
	}
	s.cache[key] = cacheEntry{records: records, expires: now.Add(lookupCacheTTL)}
	s.mu.Unlock()

	return records, nil
}

// Discover looks up the providers of content key k in the background,
// connects to them, and adds their overlays to set. It stops when ctx is done
// or the service closes.
func (s *Service) Discover(ctx context.Context, k []byte, set Adder) {
	s.goBackground(func() {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		stop := context.AfterFunc(s.ctx, cancel)
		defer stop()

		records, err := s.Lookup(ctx, k)
		if err != nil {
			s.logger.Debug("provider lookup failed", "key", hex.EncodeToString(k), "error", err)
		}
		for _, r := range records {
			if r.Address.Overlay.Equal(s.opts.Overlay) {
				continue
			}
			// add first: the preferred set only uses connected peers, so a
			// provider that is already connected is useful at once
			set.Add(r.Address.Overlay)
			if s.opts.Connect != nil {
				if err := s.opts.Connect(ctx, r.Address); err != nil {
					s.logger.Debug("connect to provider failed", "peer_address", r.Address.Overlay, "error", err)
				}
			}
		}
	})
}

// ConnectHints connects, in the background, to the overlays named in a
// download hint that the address book knows. It stops when ctx is done or the
// service closes.
func (s *Service) ConnectHints(ctx context.Context, overlays []swarm.Address) {
	if s.opts.Resolve == nil || s.opts.Connect == nil {
		return
	}
	s.goBackground(func() {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		stop := context.AfterFunc(s.ctx, cancel)
		defer stop()

		for _, o := range overlays {
			if o.Equal(s.opts.Overlay) {
				continue
			}
			addr, err := s.opts.Resolve(o)
			if err != nil {
				s.logger.Debug("hinted provider not in the address book", "peer_address", o, "error", err)
				continue
			}
			if err := s.opts.Connect(ctx, addr); err != nil {
				s.logger.Debug("connect to hinted provider failed", "peer_address", o, "error", err)
			}
		}
	})
}

// runOnce writes the windows that are due for every announcement and
// processes due read-back checks.
func (s *Service) runOnce(ctx context.Context) {
	announcements, err := s.Announced()
	if err != nil {
		s.logger.Debug("read announcements failed", "error", err)
		return
	}
	for i := range announcements {
		a := announcements[i]
		if s.publishDue(ctx, &a) {
			if err := s.saveIfAnnounced(a); err != nil {
				s.logger.Debug("save announcement failed", "key", hex.EncodeToString(a.Key), "error", err)
			}
		}
	}
	s.processChecks(ctx)
}

// publishDue writes the current window, and the next one during the last
// preWrite of the current window, unless already written. It forgets windows
// that have ended and reports whether a changed.
func (s *Service) publishDue(ctx context.Context, a *Announcement) bool {
	now := s.clock()
	w := WindowAt(now)
	due := []uint64{w}
	if windowStart(w+1).Sub(now) <= preWrite {
		due = append(due, w+1)
	}

	changed := false
	for _, ww := range due {
		if slices.Contains(a.Written, ww) {
			continue
		}
		if err := s.publish(ctx, a.Key, a.BatchID, ww); err != nil {
			s.warnOnce(a.Key, ww, err)
			continue
		}
		a.Written = append(a.Written, ww)
		changed = true
	}

	n := len(a.Written)
	a.Written = slices.DeleteFunc(a.Written, func(ww uint64) bool { return ww < w })
	return changed || len(a.Written) != n
}

// publish writes this node's record and pointer entry for content key k in
// window w, and schedules the read-back of the entry.
func (s *Service) publish(ctx context.Context, k, batchID []byte, w uint64) error {
	addr, err := s.opts.Address()
	if err != nil {
		return fmt.Errorf("own address: %w", err)
	}
	record, err := NewRecordChunk(s.opts.Signer, k, w, addr, CapabilityFull)
	if err != nil {
		return err
	}

	i := SlotFor(s.owner)
	owners, _ := s.readSlot(ctx, s.fetch, k, w, i)
	slot, err := NewSlotChunk(k, w, i, AddToSlot(owners, s.owner))
	if err != nil {
		return err
	}

	if err := s.upload(ctx, batchID, record, slot); err != nil {
		return err
	}

	s.mu.Lock()
	s.checks = append(s.checks, readBack{key: k, batchID: batchID, window: w, due: s.now().Add(readBackDelay)})
	s.mu.Unlock()
	return nil
}

// processChecks reads back the pointer entries that are due, and writes an
// entry again when it is missing, at most maxRewrites times per window.
func (s *Service) processChecks(ctx context.Context) {
	s.mu.Lock()
	now := s.now()
	var due []readBack
	s.checks = slices.DeleteFunc(s.checks, func(c readBack) bool {
		if now.Before(c.due) {
			return false
		}
		due = append(due, c)
		return true
	})
	s.mu.Unlock()

	i := SlotFor(s.owner)
	for _, c := range due {
		if !s.isAnnounced(c.key) {
			continue
		}
		owners, _ := s.readSlot(ctx, s.fetch, c.key, c.window, i)
		if containsOwner(owners, s.owner) {
			continue
		}
		if c.tries >= maxRewrites {
			s.logger.Debug("pointer entry lost, not writing it again in this window", "key", hex.EncodeToString(c.key), "window", c.window)
			continue
		}

		slot, err := NewSlotChunk(c.key, c.window, i, AddToSlot(owners, s.owner))
		if err == nil {
			err = s.upload(ctx, c.batchID, slot)
		}
		if err != nil {
			s.logger.Debug("rewrite pointer entry failed", "key", hex.EncodeToString(c.key), "window", c.window, "error", err)
		}

		c.tries++
		c.due = now.Add(readBackDelay)
		s.mu.Lock()
		s.checks = append(s.checks, c)
		s.mu.Unlock()
	}
}

// candidates reads the pointer slots of content key k in window w and returns
// at most lookupCandidates distinct owners.
func (s *Service) candidates(ctx context.Context, k []byte, w uint64) [][]byte {
	slots := make([][][]byte, Slots)
	var wg sync.WaitGroup
	for i := range Slots {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if owners, err := s.readSlot(ctx, s.opts.Getter.Get, k, w, i); err == nil {
				slots[i] = owners
			}
		}()
	}
	wg.Wait()

	var out [][]byte
	for _, owners := range slots {
		for _, o := range owners {
			if containsOwner(out, o) {
				continue
			}
			out = append(out, o)
			if len(out) == lookupCandidates {
				return out
			}
		}
	}
	return out
}

// verify reads and verifies the records of the owners, and returns the valid
// ones in the order of owners.
func (s *Service) verify(ctx context.Context, k []byte, w uint64, owners [][]byte) []*Record {
	records := make([]*Record, len(owners))
	var wg sync.WaitGroup
	for i, owner := range owners {
		wg.Add(1)
		go func() {
			defer wg.Done()
			addr, err := RecordAddress(k, owner, w)
			if err != nil {
				return
			}
			rctx, cancel := context.WithTimeout(ctx, lookupReadDeadline)
			defer cancel()
			ch, err := s.opts.Getter.Get(rctx, addr)
			if err != nil {
				return
			}
			r, err := VerifyRecord(ch, k, w, s.opts.NetworkID)
			if err != nil {
				s.logger.Debug("provider record rejected", "key", hex.EncodeToString(k), "error", err)
				return
			}
			records[i] = r
		}()
	}
	wg.Wait()

	return slices.DeleteFunc(records, func(r *Record) bool { return r == nil })
}

// readSlot reads pointer slot i of content key k in window w with get.
func (s *Service) readSlot(ctx context.Context, get func(context.Context, swarm.Address) (swarm.Chunk, error), k []byte, w uint64, i int) ([][]byte, error) {
	signer, err := IndexSigner(k)
	if err != nil {
		return nil, err
	}
	owner, err := signer.EthereumAddress()
	if err != nil {
		return nil, err
	}
	addr, err := soc.CreateAddress(SlotID(k, w, i), owner.Bytes())
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, lookupReadDeadline)
	defer cancel()
	ch, err := get(ctx, addr)
	if err != nil {
		return nil, err
	}
	return ParseSlot(ch, k, w, i)
}

func (s *Service) fetch(ctx context.Context, addr swarm.Address) (swarm.Chunk, error) {
	return s.opts.Fetcher.RetrieveChunk(ctx, addr, swarm.ZeroAddress)
}

// upload stamps the chunks from the batch and pushes them in one session.
func (s *Service) upload(ctx context.Context, batchID []byte, chunks ...swarm.Chunk) (err error) {
	stamper, save, err := s.opts.Stamper(batchID)
	if err != nil {
		return fmt.Errorf("stamper: %w", err)
	}
	session := s.opts.Uploader()
	defer func() {
		if err != nil {
			err = errors.Join(err, session.Cleanup())
		}
		err = errors.Join(err, save())
	}()

	for _, ch := range chunks {
		stamp, serr := stamper.Stamp(ch.Address(), ch.Address())
		if serr != nil {
			return fmt.Errorf("stamp: %w", serr)
		}
		if perr := session.Put(ctx, ch.WithStamp(stamp)); perr != nil {
			return fmt.Errorf("upload: %w", perr)
		}
	}
	return session.Done(chunks[0].Address())
}

func (s *Service) save(a Announcement) error {
	return s.opts.Store.Put(announcedKey(a.Key), a)
}

// saveIfAnnounced saves a, unless it was withdrawn, or announced again with
// another batch, while the loop was publishing it.
func (s *Service) saveIfAnnounced(a Announcement) error {
	s.annMu.Lock()
	defer s.annMu.Unlock()

	var cur Announcement
	if err := s.opts.Store.Get(announcedKey(a.Key), &cur); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil
		}
		return err
	}
	if !bytes.Equal(cur.BatchID, a.BatchID) {
		return nil
	}
	return s.save(a)
}

// warnOnce logs a failed write once per content key and window, so an
// unusable batch does not log on every tick.
func (s *Service) warnOnce(k []byte, w uint64, err error) {
	s.mu.Lock()
	last, ok := s.warned[string(k)]
	s.warned[string(k)] = w
	s.mu.Unlock()
	if !ok || last != w {
		s.logger.Warning("provider announcement not written", "key", hex.EncodeToString(k), "window", w, "error", err)
	}
}

func (s *Service) clock() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.now()
}

func announcedKey(k []byte) string {
	return announcedPrefix + hex.EncodeToString(k)
}

func windowStart(w uint64) time.Time {
	return time.Unix(int64(w*uint64(Window/time.Second)), 0)
}
