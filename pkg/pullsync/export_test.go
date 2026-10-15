// Copyright 2020 The Swarm Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pullsync

import (
	"context"

	"github.com/ethersphere/bee/v2/pkg/p2p"
	"github.com/ethersphere/bee/v2/pkg/swarm"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// SetAfterMakeOffer replaces the hook that runs between building an offer and
// unregistering the waiting request, and returns a function that restores it.
func SetAfterMakeOffer(f func()) (restore func()) {
	prev := afterMakeOffer
	afterMakeOffer = f
	return func() { afterMakeOffer = prev }
}

// RequestsReplaced returns the replaced-request counter for a reason.
func (s *Syncer) RequestsReplaced(reason string) float64 {
	return metricValue(s.metrics.RequestsReplaced.WithLabelValues(reason))
}

// WaitingRequests returns the waiting-request gauge.
func (s *Syncer) WaitingRequests() float64 {
	return metricValue(s.metrics.WaitingRequests)
}

// WaitingTracked returns the number of tracked waiting requests.
func (s *Syncer) WaitingTracked() int {
	return s.waiting.count()
}

// WaitingKeys returns the number of (peer, bin) keys in the waiting-request
// map.
func (s *Syncer) WaitingKeys() int {
	s.waiting.mu.Lock()
	defer s.waiting.mu.Unlock()
	return len(s.waiting.m)
}

func metricValue(m prometheus.Metric) float64 {
	var d dto.Metric
	if err := m.Write(&d); err != nil {
		return -1
	}
	if c := d.GetCounter(); c != nil {
		return c.GetValue()
	}
	return d.GetGauge().GetValue()
}

// RequestsAbandoned returns the abandoned-request counter for a reason.
func (s *Syncer) RequestsAbandoned(reason string) float64 {
	return metricValue(s.metrics.RequestsAbandoned.WithLabelValues(reason))
}

// RequestsAbandonedTotal returns the abandoned-request counter summed over
// every reason.
func (s *Syncer) RequestsAbandonedTotal() float64 {
	var sum float64
	for _, r := range []string{reasonReset, reasonDisconnect, reasonEOF, reasonError, reasonUnexpectedData} {
		sum += s.RequestsAbandoned(r)
	}
	return sum
}

// RequestsUnwatched returns the unwatched-request counter.
func (s *Syncer) RequestsUnwatched() float64 {
	return metricValue(s.metrics.RequestsUnwatched)
}

// RequestsRefused returns the refused-request counter for a reason.
func (s *Syncer) RequestsRefused(reason string) float64 {
	return metricValue(s.metrics.RequestsRefused.WithLabelValues(reason))
}

// ReasonBinOutOfRange is the refusal reason for a bin the reserve cannot hold.
const ReasonBinOutOfRange = reasonBinOutOfRange

// Exported protocol identifiers for tests and fuzzers that need to open or
// register the pullsync stream through the p2p streamtest recorder.
const (
	ProtocolName    = protocolName
	ProtocolVersion = protocolVersion
	StreamName      = streamName
)

const (
	MaxPeerSyncStreams   = maxPeerSyncStreams
	MaxPeerCursorStreams = maxPeerCursorStreams
)

var (
	ErrStreamLimitExceeded = errStreamLimitExceeded
	NewPeerStreamLimiter   = newPeerStreamLimiter
)

func (l *peerStreamLimiter) Acquire(peer swarm.Address) (func(), bool) { return l.acquire(peer) }
func (l *peerStreamLimiter) Clear(peer swarm.Address)                  { l.clear(peer) }

// Handler exposes the pullsync stream handler.
func (s *Syncer) Handler(ctx context.Context, p p2p.Peer, stream p2p.Stream) error {
	return s.handler(ctx, p, stream)
}

// CursorHandler exposes the cursor stream handler.
func (s *Syncer) CursorHandler(ctx context.Context, p p2p.Peer, stream p2p.Stream) error {
	return s.cursorHandler(ctx, p, stream)
}

// FillSyncStreams takes all sync stream slots of the peer.
func (s *Syncer) FillSyncStreams(peer swarm.Address) {
	for range maxPeerSyncStreams {
		s.syncStreams.acquire(peer)
	}
}

// FillCursorStreams takes all cursor stream slots of the peer.
func (s *Syncer) FillCursorStreams(peer swarm.Address) {
	for range maxPeerCursorStreams {
		s.cursorStreams.acquire(peer)
	}
}

// StreamLimitExceededCount returns the stream limit exceeded metric value.
func (s *Syncer) StreamLimitExceededCount() float64 {
	var m dto.Metric
	if err := s.metrics.StreamLimitExceeded.Write(&m); err != nil {
		return -1
	}
	return m.GetCounter().GetValue()
}

// UnwrapHeld exposes the hand-off of a held chunk to pss or gsoc.
func (s *Syncer) UnwrapHeld(ch swarm.Chunk) { s.unwrapHeld(ch) }
