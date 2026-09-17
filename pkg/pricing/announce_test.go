// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pricing_test

import (
	"bytes"
	"context"
	"math/big"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethersphere/bee/v2/pkg/log"
	"github.com/ethersphere/bee/v2/pkg/p2p"
	"github.com/ethersphere/bee/v2/pkg/p2p/protobuf"
	"github.com/ethersphere/bee/v2/pkg/p2p/streamtest"
	"github.com/ethersphere/bee/v2/pkg/pricing"
	"github.com/ethersphere/bee/v2/pkg/pricing/pb"
	"github.com/ethersphere/bee/v2/pkg/swarm"
)

// lockedObserver is a PaymentThresholdObserver safe for the several handler
// goroutines these tests produce. The package's own testThresholdObserver
// writes three fields with no mutex, which is fine for one announcement and
// races once there are several.
type lockedObserver struct {
	mu sync.Mutex
	n  int
}

func (o *lockedObserver) NotifyPaymentThreshold(_ swarm.Address, _ *big.Int) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.n++
	return nil
}

// thresholdsSent reads every announcement the recorder captured for a peer, in
// the order they were sent.
func thresholdsSent(t *testing.T, recorder *streamtest.Recorder, peer swarm.Address) []*big.Int {
	t.Helper()

	records, err := recorder.Records(peer, "pricing", "1.0.0", "pricing")
	if err != nil {
		t.Fatal(err)
	}

	out := make([]*big.Int, 0, len(records))
	for _, rec := range records {
		messages, err := protobuf.ReadMessages(
			bytes.NewReader(rec.In()),
			func() protobuf.Message { return new(pb.AnnouncePaymentThreshold) },
		)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range messages {
			out = append(out, new(big.Int).SetBytes(m.(*pb.AnnouncePaymentThreshold).PaymentThreshold))
		}
	}
	return out
}

// blockingStreamer lets a test hold the first announcement open, so a second
// caller genuinely arrives while one is in flight. Without that, the tests
// below pass with no serialiser at all: sequential sends are already ordered.
type blockingStreamer struct {
	inner   p2p.Streamer
	hold    chan struct{} // closed to release the held send
	entered chan struct{} // closed once the first send has started
	once    sync.Once
	n       atomic.Int32
}

func (b *blockingStreamer) NewStream(ctx context.Context, addr swarm.Address, h p2p.Headers, protocol, version, stream string) (p2p.Stream, error) {
	if b.n.Add(1) == 1 {
		b.once.Do(func() { close(b.entered) })
		<-b.hold
	}
	return b.inner.NewStream(ctx, addr, h, protocol, version, stream)
}

// TestAnnounceSerialisedDropsStaleValue is the property the serialiser exists
// for, and the one the spec calls the residual risk of the whole design: while
// one announcement is in flight, newer values arrive, and the peer must end on
// the newest rather than on one that overtook it.
//
// It fails without the serialiser: every caller would open its own stream and
// the arrival order would be whatever the scheduler chose.
func TestAnnounceSerialisedDropsStaleValue(t *testing.T) {
	t.Parallel()

	peer := swarm.MustParseHexAddress("9ee7add7")
	recipient := pricing.New(nil, log.Noop, big.NewInt(100000), big.NewInt(10000), big.NewInt(1000))
	recipient.SetPaymentThresholdObserver(&lockedObserver{})

	recorder := streamtest.New(
		streamtest.WithProtocols(recipient.Protocol()),
		streamtest.WithBaseAddr(peer),
	)
	blocker := &blockingStreamer{inner: recorder, hold: make(chan struct{}), entered: make(chan struct{})}
	payer := pricing.New(blocker, log.Noop, big.NewInt(100000), big.NewInt(10000), big.NewInt(1000))

	// First caller: starts sending and is held inside the stream.
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = payer.AnnouncePaymentThreshold(context.Background(), peer, big.NewInt(13_500_000))
	}()
	<-blocker.entered

	// Three more values arrive while that send is in flight. They must
	// coalesce, and the last one called must be the one that survives.
	const newest = 54_000_000
	for _, v := range []int64{18_000_000, 27_000_000, newest} {
		if err := payer.AnnouncePaymentThreshold(context.Background(), peer, big.NewInt(v)); err != nil {
			t.Fatalf("a coalesced caller reported an error for a send it did not make: %v", err)
		}
	}

	close(blocker.hold)
	<-done

	// The queued value is drained on its own goroutine, so wait for it.
	var sent []*big.Int
	for range 200 {
		sent = thresholdsSent(t, recorder, peer)
		if len(sent) >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if len(sent) < 2 {
		t.Fatalf("only %d announcements reached the peer; the values queued behind the held send were never delivered", len(sent))
	}
	if len(sent) == 4 {
		t.Fatal("all four values were sent, so nothing coalesced: the serialiser is not taking effect")
	}
	if last := sent[len(sent)-1].Int64(); last != newest {
		t.Fatalf("the peer ended on %d, want the newest value %d; a stale value overtook a newer one and the peer would keep it for good", last, newest)
	}
}

// TestAnnounceSerialisedCallerGetsOwnError checks the attribution a peer
// disconnect hangs on. init returns this error from ConnectIn, and libp2p
// disconnects the peer on any non-nil return, so a caller must never be handed
// a failure from somebody else's send.
func TestAnnounceSerialisedCallerGetsOwnError(t *testing.T) {
	t.Parallel()

	peer := swarm.MustParseHexAddress("9ee7add7")
	recipient := pricing.New(nil, log.Noop, big.NewInt(100000), big.NewInt(10000), big.NewInt(1000))
	recipient.SetPaymentThresholdObserver(&lockedObserver{})

	recorder := streamtest.New(
		streamtest.WithProtocols(recipient.Protocol()),
		streamtest.WithBaseAddr(peer),
	)
	blocker := &blockingStreamer{inner: recorder, hold: make(chan struct{}), entered: make(chan struct{})}
	payer := pricing.New(blocker, log.Noop, big.NewInt(100000), big.NewInt(10000), big.NewInt(1000))

	go func() {
		_ = payer.AnnouncePaymentThreshold(context.Background(), peer, big.NewInt(13_500_000))
	}()
	<-blocker.entered

	// This caller is coalesced into the in-flight send. Whatever happens to
	// that send, this one must not report an error, because reporting one here
	// would disconnect the peer.
	err := payer.AnnouncePaymentThreshold(context.Background(), peer, big.NewInt(54_000_000))
	close(blocker.hold)

	if err != nil {
		t.Fatalf("a coalesced caller returned %v; init returns this from ConnectIn and libp2p would disconnect the peer for a send this caller never made", err)
	}
}

// TestAnnounceSerialisedUncontendedStillSends guards the obvious regression:
// serialising must not swallow an announcement that had nothing to contend
// with. This is what init does on every connect.
func TestAnnounceSerialisedUncontendedStillSends(t *testing.T) {
	t.Parallel()

	peer := swarm.MustParseHexAddress("9ee7add7")
	recipient := pricing.New(nil, log.Noop, big.NewInt(100000), big.NewInt(10000), big.NewInt(1000))
	recipient.SetPaymentThresholdObserver(&lockedObserver{})

	recorder := streamtest.New(
		streamtest.WithProtocols(recipient.Protocol()),
		streamtest.WithBaseAddr(peer),
	)
	payer := pricing.New(recorder, log.Noop, big.NewInt(100000), big.NewInt(10000), big.NewInt(1000))

	// One announcement on its own must still be sent, and exactly once.
	if err := payer.AnnouncePaymentThreshold(context.Background(), peer, big.NewInt(54_000_000)); err != nil {
		t.Fatal(err)
	}
	sent := thresholdsSent(t, recorder, peer)
	if len(sent) != 1 {
		t.Fatalf("one announcement produced %d sends, want 1", len(sent))
	}
	if sent[0].Int64() != 54_000_000 {
		t.Fatalf("announced %d, want 54000000", sent[0].Int64())
	}
}
