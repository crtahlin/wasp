// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package retrieval

import (
	"context"
	"encoding/hex"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/ethersphere/bee/v2/pkg/accounting"
	"github.com/ethersphere/bee/v2/pkg/p2p"
	"github.com/ethersphere/bee/v2/pkg/safe"
	"github.com/ethersphere/bee/v2/pkg/skippeers"
	"github.com/ethersphere/bee/v2/pkg/swarm"
	"github.com/ethersphere/bee/v2/pkg/topology"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// LocalOnlyHeader is the stream header with which a requester asks a peer to
// answer a retrieval request from its own store only, without forwarding it.
// A peer that does not know the header ignores it and handles the request as
// usual.
const LocalOnlyHeader = "wasp-local-only"

const (
	// maxPreferredAttempts is how many preferred peers are tried for one chunk
	// before normal peer selection takes over.
	maxPreferredAttempts = 2
	// preferredWait is how long a preferred attempt may run before the next
	// attempt starts. The slow attempt is not canceled.
	preferredWait = 500 * time.Millisecond
	// demoteAfterMisses is how many misses in a row drop a preferred peer.
	demoteAfterMisses = 16
	// demoteFor is how long a dropped preferred peer stays out of the set.
	demoteFor = 10 * time.Minute
	// preferredSuffix separates the preferred-set fingerprint in a
	// singleflight key.
	preferredSuffix = "_preferred_"
)

var (
	errNotHeldLocally = errors.New("wasp: chunk not held locally")
	errLocalOnlyLimit = errors.New("wasp: local-only limit")
)

// PreferredSet holds the peers that a download prefers for its chunks, such as
// providers named by the caller or found by a lookup. It is safe for concurrent
// use, so a lookup can add peers while the download runs.
type PreferredSet struct {
	mu      sync.Mutex
	peers   []swarm.Address
	misses  map[string]int
	demoted map[string]time.Time
	now     func() time.Time
}

// NewPreferredSet returns a set holding the given peers.
func NewPreferredSet(peers ...swarm.Address) *PreferredSet {
	p := &PreferredSet{
		misses:  make(map[string]int),
		demoted: make(map[string]time.Time),
		now:     time.Now,
	}
	p.Add(peers...)
	return p
}

// Add adds the peers that are not in the set yet.
func (p *PreferredSet) Add(peers ...swarm.Address) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, a := range peers {
		if a.IsZero() || swarm.ContainsAddress(p.peers, a) {
			continue
		}
		p.peers = append(p.peers, a)
	}
}

// Peers returns the peers in the set that are not currently dropped.
func (p *PreferredSet) Peers() []swarm.Address {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := p.now()
	peers := make([]swarm.Address, 0, len(p.peers))
	for _, a := range p.peers {
		if until, ok := p.demoted[a.ByteString()]; ok {
			if now.Before(until) {
				continue
			}
			delete(p.demoted, a.ByteString())
		}
		peers = append(peers, a)
	}
	return peers
}

// hit records that the peer delivered a chunk.
func (p *PreferredSet) hit(peer swarm.Address) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.misses, peer.ByteString())
}

// miss records that the peer did not deliver a chunk, and drops the peer after
// demoteAfterMisses misses in a row.
func (p *PreferredSet) miss(peer swarm.Address) {
	p.mu.Lock()
	defer p.mu.Unlock()

	k := peer.ByteString()
	p.misses[k]++
	if p.misses[k] >= demoteAfterMisses {
		p.demoteLocked(k)
	}
}

// demote drops the peer at once, for example after it delivered an invalid
// chunk.
func (p *PreferredSet) demote(peer swarm.Address) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.demoteLocked(peer.ByteString())
}

func (p *PreferredSet) demoteLocked(k string) {
	delete(p.misses, k)
	p.demoted[k] = p.now().Add(demoteFor)
}

type preferredKey struct{}

// WithPreferredPeers returns a context that makes origin retrievals try the
// peers in set first.
func WithPreferredPeers(ctx context.Context, set *PreferredSet) context.Context {
	return context.WithValue(ctx, preferredKey{}, set)
}

// PreferredPeers returns the preferred set carried by ctx, or nil.
func PreferredPeers(ctx context.Context) *PreferredSet {
	set, _ := ctx.Value(preferredKey{}).(*PreferredSet)
	return set
}

// HasPreferredPeers reports whether ctx carries a preferred set, including one
// deliberately set to nil.
//
// It distinguishes "no set was ever attached" from "preference was switched off
// on purpose", which PreferredPeers cannot: a typed nil stored under the key
// satisfies the type assertion there, so the value is nil while the key is
// present. Today no reachable path tells the two apart, and the spec for #299
// says so rather than claiming a defect this prevents. It is kept because it
// states the intent exactly and the suppressed context reaching a wrapper is
// one refactor away.
func HasPreferredPeers(ctx context.Context) bool {
	_, ok := ctx.Value(preferredKey{}).(*PreferredSet)
	return ok
}

// fingerprint returns a short digest of the peers that does not depend on
// their order. It keeps retrievals of the same chunk with different preferred
// peers apart in singleflight.
func fingerprint(peers []swarm.Address) string {
	if len(peers) == 0 {
		return ""
	}

	keys := make([]string, 0, len(peers))
	for _, a := range peers {
		keys = append(keys, a.ByteString())
	}
	sort.Strings(keys)

	h := swarm.NewHasher()
	for _, k := range keys {
		_, _ = h.Write([]byte(k))
	}
	return hex.EncodeToString(h.Sum(nil)[:8])
}

// localOnlyHeaders returns the stream headers of a preferred attempt. The map
// is new on every call because opening a stream adds to it.
func localOnlyHeaders() p2p.Headers {
	return p2p.Headers{LocalOnlyHeader: {1}}
}

// preferredCandidates returns at most maxPreferredAttempts peers from peers
// that are connected full nodes and not in skip, ordered by closeness to the
// chunk. Light peers never enter the topology's connected set, which matters
// because a light node blocklists a peer that opens a retrieval stream to it.
func (s *Service) preferredCandidates(peers []swarm.Address, chunkAddr swarm.Address, skip []swarm.Address) []swarm.Address {
	candidates := make([]swarm.Address, 0, len(peers))
	for _, a := range peers {
		if a.Equal(s.addr) || swarm.ContainsAddress(skip, a) || !s.connectedFullNode(a) {
			continue
		}
		candidates = append(candidates, a)
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		cmp, err := swarm.DistanceCmp(chunkAddr, candidates[i], candidates[j])
		return err == nil && cmp == 1
	})

	if len(candidates) > maxPreferredAttempts {
		candidates = candidates[:maxPreferredAttempts]
	}
	return candidates
}

// connectedFullNode reports whether peer is in the topology's connected set.
// Asking for the peer closest to the peer's own address returns the peer
// itself exactly when it is connected.
func (s *Service) connectedFullNode(peer swarm.Address) bool {
	if s.peerSuggester == nil {
		return false
	}
	closest, err := s.peerSuggester.ClosestPeer(peer, false, topology.Select{})
	return err == nil && closest.Equal(peer)
}

// retrievePreferred starts a local-only attempt at a preferred peer. It
// returns nil when the attempt started, and otherwise the reason it did not.
// The caller needs the reason, not just the failure: an overdraft clears by
// itself after overDraftRefresh and is worth waiting for, while a peer that is
// not connected never will be. Returning a bare bool here is what made a
// transient refusal permanent, see #324.
func (s *Service) retrievePreferred(
	ctx, spanCtx context.Context,
	quit chan struct{},
	chunkAddr, peer swarm.Address,
	skip *skippeers.List,
	resultC chan retrievalResult,
) error {
	action, err := s.prepareCredit(ctx, peer, chunkAddr, true)
	if err != nil {
		skip.Add(chunkAddr, peer, overDraftRefresh)
		if errors.Is(err, accounting.ErrOverdraft) {
			s.metrics.PreferredOverdrafts.Inc()
		}
		return err
	}
	// a preferred peer is asked once per chunk; normal selection would only
	// make it forward
	skip.Forever(chunkAddr, peer)
	s.metrics.PreferredAttempts.Inc()

	safe.Go(s.logger, "retrieval-retrieve-preferred", func() {
		span, _, ctx := s.tracer.FollowSpanFromContext(spanCtx, "retrieve-chunk", s.logger, trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(
			attribute.String("swarm.chunk.address", chunkAddr.String()),
			attribute.Bool("swarm.chunk.preferred", true),
		))
		defer span.End()
		s.retrieveChunk(ctx, quit, chunkAddr, peer, resultC, action, span, localOnlyHeaders(), true)
	})
	return nil
}

// preferredResult records the outcome of a preferred attempt in the set.
func (s *Service) preferredResult(set *PreferredSet, res retrievalResult) {
	switch {
	case res.err == nil:
		s.metrics.PreferredHits.Inc()
		if set != nil {
			set.hit(res.peer)
		}
	case errors.Is(res.err, swarm.ErrInvalidChunk):
		s.metrics.PreferredMisses.Inc()
		if set != nil {
			set.demote(res.peer)
		}
	default:
		s.metrics.PreferredMisses.Inc()
		if set != nil {
			set.miss(res.peer)
		}
	}
}

// localOnlyMiss answers a local-only request for a chunk this node does not
// hold. A miss earns nothing, so misses are limited per peer and for the whole
// node, and past a limit the answer is the limit error. Both answers are sent
// before any debit.
func (s *Service) localOnlyMiss(peer swarm.Address) error {
	if !s.peerMissLimiter.Allow(peer.ByteString(), 1) || !s.nodeMissLimiter.Allow("", 1) {
		s.metrics.LocalOnlyLimited.Inc()
		return errLocalOnlyLimit
	}
	s.metrics.LocalOnlyMisses.Inc()
	return errNotHeldLocally
}
