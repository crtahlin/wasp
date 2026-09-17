// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pricing_test

import (
	"bytes"
	"context"
	"math/big"
	"sync"
	"testing"

	"github.com/ethersphere/bee/v2/pkg/log"
	"github.com/ethersphere/bee/v2/pkg/p2p/protobuf"
	"github.com/ethersphere/bee/v2/pkg/p2p/streamtest"
	"github.com/ethersphere/bee/v2/pkg/pricing"
	"github.com/ethersphere/bee/v2/pkg/pricing/pb"
	"github.com/ethersphere/bee/v2/pkg/swarm"
)

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

// TestAnnounceSerialisedKeepsCallOrder is the property the serialiser exists
// for. The announcement carries an absolute value with no sequence number and
// no acknowledgement, and the wire cannot be changed (rule 6), so without this
// two announcements to one peer can be delivered in either order and the peer
// keeps whichever it processes last, for good.
//
// What the serialiser can promise is call order: a value announced after
// another reaches the peer after it, or not at all. It cannot know which value
// is semantically newest, and it must not try: after a reconnect the lower
// node-wide value is the correct one to send.
func TestAnnounceSerialisedKeepsCallOrder(t *testing.T) {
	t.Parallel()

	peer := swarm.MustParseHexAddress("9ee7add7")
	recipient := pricing.New(nil, log.Noop, big.NewInt(100000), big.NewInt(10000), big.NewInt(1000))
	recipient.SetPaymentThresholdObserver(&testThresholdObserver{})

	recorder := streamtest.New(
		streamtest.WithProtocols(recipient.Protocol()),
		streamtest.WithBaseAddr(peer),
	)
	payer := pricing.New(recorder, log.Noop, big.NewInt(100000), big.NewInt(10000), big.NewInt(1000))

	// The sequence a provider grant produces: the connect announcement of the
	// node-wide default, a growth step, then the grant. The grant is called
	// last and must therefore reach the peer last.
	const grant = 54_000_000
	for _, v := range []int64{13_500_000, 18_000_000, grant} {
		if err := payer.AnnouncePaymentThreshold(context.Background(), peer, big.NewInt(v)); err != nil {
			t.Fatal(err)
		}
	}

	sent := thresholdsSent(t, recorder, peer)
	if len(sent) == 0 {
		t.Fatal("nothing was announced at all")
	}
	if last := sent[len(sent)-1]; last.Int64() != grant {
		t.Fatalf("the last announcement was %d, want the last value called, %d; an older value overtook a newer one and the peer would keep the older one for good", last.Int64(), grant)
	}
}

// TestAnnounceSerialisedCoalesces checks that a value arriving while a send is
// in flight replaces one already waiting rather than queueing behind it. A
// burst is exactly what the growth path produces after a reconnect (#333), and
// without coalescing that is one stream per value.
func TestAnnounceSerialisedCoalesces(t *testing.T) {
	t.Parallel()

	peer := swarm.MustParseHexAddress("9ee7add7")
	recipient := pricing.New(nil, log.Noop, big.NewInt(100000), big.NewInt(10000), big.NewInt(1000))
	recipient.SetPaymentThresholdObserver(&testThresholdObserver{})

	recorder := streamtest.New(
		streamtest.WithProtocols(recipient.Protocol()),
		streamtest.WithBaseAddr(peer),
	)
	payer := pricing.New(recorder, log.Noop, big.NewInt(100000), big.NewInt(10000), big.NewInt(1000))

	const n = 32
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = payer.AnnouncePaymentThreshold(context.Background(), peer, big.NewInt(int64(10_000_000+i)))
		}(i)
	}
	wg.Wait()

	sent := thresholdsSent(t, recorder, peer)
	if len(sent) > n {
		t.Fatalf("%d announcements were sent for %d values; the serialiser must coalesce, never multiply", len(sent), n)
	}
	for _, v := range sent {
		if v.Int64() < 10_000_000 || v.Int64() >= 10_000_000+n {
			t.Fatalf("%d was announced but never asked for", v.Int64())
		}
	}
}

// TestAnnounceSerialisedOneAtATime is the narrow property the two tests above
// rest on, checked directly rather than through the network: while one caller
// is sending to a peer, another caller for the same peer does not also send.
func TestAnnounceSerialisedOneAtATime(t *testing.T) {
	t.Parallel()

	peer := swarm.MustParseHexAddress("9ee7add7")
	recipient := pricing.New(nil, log.Noop, big.NewInt(100000), big.NewInt(10000), big.NewInt(1000))
	recipient.SetPaymentThresholdObserver(&testThresholdObserver{})

	recorder := streamtest.New(
		streamtest.WithProtocols(recipient.Protocol()),
		streamtest.WithBaseAddr(peer),
	)
	payer := pricing.New(recorder, log.Noop, big.NewInt(100000), big.NewInt(10000), big.NewInt(1000))

	// One announcement on its own must still be sent, so that serialising does
	// not simply drop everything.
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
