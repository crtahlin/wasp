// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pullsync_test

import (
	"context"
	"testing"
	"testing/synctest"

	"github.com/ethersphere/bee/v2/pkg/p2p/streamtest"
)

// Bee v2.8.3 disconnects a peer that holds more than 128 sync or 4 cursor
// streams at once. These tests check that the slot of a request ends with
// the request in wasp's own paths: a request still waiting for its first
// chunk whose requester resets (#641), a waiting request replaced by the
// same peer asking again (#640), and a cursors request whose requester
// resets before asking.

func TestSyncSlotReleasedWhenRequesterResets(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		server, _ := newPullSync(t, nil, 5)
		client := newClient(t, server, peerA)
		ctx, cancel := context.WithCancel(context.Background())

		res := startSync(ctx, client, 22, 240)
		synctest.Wait()
		if got := server.SyncStreamsInUse(peerA); got != 1 {
			t.Fatalf("sync slots %d while the request waits, want 1", got)
		}

		cancel()
		<-res
		synctest.Wait()
		if got := server.SyncStreamsInUse(peerA); got != 0 {
			t.Fatalf("sync slots %d after the requester reset, want 0", got)
		}
	})
}

func TestSyncSlotReleasedWhenRequestReplaced(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		server, _ := newPullSync(t, nil, 5)
		client := newClient(t, server, peerA)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		first := startSync(ctx, client, 22, 240)
		synctest.Wait()
		second := startSync(ctx, client, 22, 240)
		synctest.Wait()
		requireEnded(t, "first request", first)
		synctest.Wait()

		if got := server.SyncStreamsInUse(peerA); got != 1 {
			t.Fatalf("sync slots %d after the first request was replaced, want 1", got)
		}

		cancel()
		<-second
		synctest.Wait()
		if got := server.SyncStreamsInUse(peerA); got != 0 {
			t.Fatalf("sync slots %d after both requests ended, want 0", got)
		}
	})
}

func TestCursorSlotReleasedWhenRequesterResets(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		server, _ := newPullSync(t, nil, 5)
		recorder := streamtest.New(streamtest.WithProtocols(server.Protocol()), streamtest.WithBaseAddr(peerA))

		// More cursors requests than the limit, one after the other, each
		// reset before it asks: none may hold its slot afterwards, or the
		// fifth would disconnect the peer.
		for i := range 2 * 4 {
			stream, err := recorder.NewStream(context.Background(), peerA, nil, psProtocol, psVersion, "cursors")
			if err != nil {
				t.Fatal(err)
			}
			synctest.Wait()
			if got := server.CursorStreamsInUse(peerA); got != 1 {
				t.Fatalf("request %d: cursor slots %d while it waits, want 1", i, got)
			}
			_ = stream.Reset()
			synctest.Wait()
			if got := server.CursorStreamsInUse(peerA); got != 0 {
				t.Fatalf("request %d: cursor slots %d after the reset, want 0", i, got)
			}
		}
		if got := server.StreamLimitExceededCount(); got != 0 {
			t.Fatalf("stream limit exceeded %v times, want 0", got)
		}
	})
}
