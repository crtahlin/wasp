// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package providers_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ethersphere/bee/v2/pkg/bzz"
	"github.com/ethersphere/bee/v2/pkg/providers"
	"github.com/ethersphere/bee/v2/pkg/swarm"
)

// These cover issue #369: discovery and hinted connection are node-scoped
// background work and must not die with the request that started them. See
// docs/experiments/content-providers/discovery-lifetime.md.
//
// Two hazards run through all of them, and each has already produced a test
// that proved nothing:
//
//   - The in-memory network takes a context and ignores it, so a stub that also
//     ignores it makes a cancelled discovery look identical to a live one. Every
//     stub here returns ctx.Err() first, as a real dial does.
//   - Close cancels the service context BEFORE waiting on the goroutine, so
//     closing first can cancel correct work in flight. Each test waits for the
//     outcome it asserts before closing.

var (
	errDialRefused      = errors.New("dial refused")
	errNotInAddressBook = errors.New("not in the address book")
)

// blockingConnect returns a stub that reports when it is first called and then
// waits for its context, which is what a hung dial looks like.
func blockingConnect() (connect connectFunc, running <-chan struct{}, canceled <-chan error) {
	var once sync.Once
	start := make(chan struct{})
	done := make(chan error, 8)
	return func(ctx context.Context, _ *bzz.Address) (bool, error) {
		once.Do(func() { close(start) })
		<-ctx.Done()
		done <- ctx.Err()
		return false, ctx.Err()
	}, start, done
}

// recordingConnect returns a stub that records the overlays it dials and
// respects the context.
func recordingConnect() (connectFunc, <-chan swarm.Address) {
	dialed := make(chan swarm.Address, 8)
	return func(ctx context.Context, addr *bzz.Address) (bool, error) {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		dialed <- addr.Overlay
		return false, nil
	}, dialed
}

func announceOne(t *testing.T, pa *providers.Service) []byte {
	t.Helper()
	k := swarm.RandAddress(t).Bytes()
	if err := pa.Announce(context.Background(), k, batch); err != nil {
		t.Fatal(err)
	}
	return k
}

func nilConnect(context.Context, *bzz.Address) (bool, error) { return false, nil }

// TestDiscoverSurvivesCallerCancel is the defect itself: before the fix the
// goroutine derived from the caller, so a context already cancelled meant the
// lookup never ran. The context is cancelled before the call rather than during
// it, which removes the race from the test.
func TestDiscoverSurvivesCallerCancel(t *testing.T) {
	t.Parallel()

	n, c := newNetwork(), &clock{t: midWindow(1000)}
	a, b := newNode(t, 1), newNode(t, 1)

	connect, dialed := recordingConnect()
	pa := newService(t, n, a, c, nilConnect)
	reader := newService(t, n, b, c, connect)
	k := announceOne(t, pa)

	dead, cancel := context.WithCancel(context.Background())
	cancel()

	found := &adder{}
	reader.Discover(dead, k, found)

	select {
	case got := <-dialed:
		if !got.Equal(a.addr.Overlay) {
			t.Fatalf("dialed %v, want the provider", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("discovery with a cancelled caller did not connect")
	}

	_ = reader.Close()

	if got := found.list(); len(got) != 1 || !got[0].Equal(a.addr.Overlay) {
		t.Fatalf("discovery with a cancelled caller added %v, want the provider", got)
	}
}

// TestConnectHintsSurvivesCallerCancel is the same property for the hinted
// path, where the caller is the HTTP request itself.
func TestConnectHintsSurvivesCallerCancel(t *testing.T) {
	t.Parallel()

	n, c := newNetwork(), &clock{t: midWindow(1000)}
	a, b := newNode(t, 1), newNode(t, 1)

	connect, dialed := recordingConnect()
	resolve := func(o swarm.Address) (*bzz.Address, error) {
		if o.Equal(a.addr.Overlay) {
			return a.addr, nil
		}
		return nil, errNotInAddressBook
	}

	reader := newServiceResolving(t, n, b, c, connect, resolve)

	dead, cancel := context.WithCancel(context.Background())
	cancel()

	reader.ConnectHints(dead, []swarm.Address{a.addr.Overlay})

	select {
	case got := <-dialed:
		if !got.Equal(a.addr.Overlay) {
			t.Fatalf("dialed %v, want the hinted provider", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("hinted connect with a cancelled caller did not connect")
	}

	_ = reader.Close()
}

// TestDiscoverStopsOnClose pins the guarantee the fix must not weaken: the
// service context still ends the work. The stub signals that the goroutine is
// running before Close is called, because goBackground checks the closed flag
// under a lock and a Close racing the call would make the goroutine never start
// and the test pass having proved nothing.
//
// The timeout is left at its default deliberately. Shortened, the timeout rather
// than Close would cancel the blocked dial, and the test would pass with the
// shutdown path deleted.
func TestDiscoverStopsOnClose(t *testing.T) {
	t.Parallel()

	n, c := newNetwork(), &clock{t: midWindow(1000)}
	a, b := newNode(t, 1), newNode(t, 1)

	connect, running, canceled := blockingConnect()
	pa := newService(t, n, a, c, nilConnect)
	reader := newService(t, n, b, c, connect)
	k := announceOne(t, pa)

	reader.Discover(context.Background(), k, &adder{})
	assertStopsOnClose(t, reader, running, canceled)
}

// TestConnectHintsStopsOnClose is the same for the hinted path, which runs on
// every request carrying the header and so is the one whose goroutines
// accumulate if shutdown stops reaching them.
func TestConnectHintsStopsOnClose(t *testing.T) {
	t.Parallel()

	n, c := newNetwork(), &clock{t: midWindow(1000)}
	a, b := newNode(t, 1), newNode(t, 1)

	connect, running, canceled := blockingConnect()
	resolve := func(swarm.Address) (*bzz.Address, error) { return a.addr, nil }
	reader := newServiceResolving(t, n, b, c, connect, resolve)

	reader.ConnectHints(context.Background(), []swarm.Address{a.addr.Overlay})
	assertStopsOnClose(t, reader, running, canceled)
}

func assertStopsOnClose(t *testing.T, svc *providers.Service, running <-chan struct{}, canceled <-chan error) {
	t.Helper()

	select {
	case <-running:
	case <-time.After(10 * time.Second):
		t.Fatal("the background goroutine never reached the connect")
	}

	done := make(chan struct{})
	go func() {
		_ = svc.Close()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not return within two seconds")
	}

	select {
	case err := <-canceled:
		// the cause must be Close, not the run's own deadline: asserting only
		// that it is non-nil lets a shortened timeout satisfy this test with
		// the shutdown path removed
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("the blocked connect saw %v, want context.Canceled", err)
		}
	default:
		t.Fatal("the blocked connect did not return")
	}
}

// TestDiscoverBoundedByTimeout is the other half: a connect that never returns
// is abandoned rather than held for the life of the node.
func TestDiscoverBoundedByTimeout(t *testing.T) {
	t.Parallel()

	n, c := newNetwork(), &clock{t: midWindow(1000)}
	a, b := newNode(t, 1), newNode(t, 1)

	connect, _, canceled := blockingConnect()
	pa := newService(t, n, a, c, nilConnect)
	reader := newService(t, n, b, c, connect)
	reader.SetDiscoverTimeout(200 * time.Millisecond)
	k := announceOne(t, pa)

	reader.Discover(context.Background(), k, &adder{})
	assertBoundedByTimeout(t, canceled)
}

// TestConnectHintsBoundedByTimeout matters more than the Discover one: the
// hinted path fires on every request carrying the header, with no lookup cache
// in front of it, so an unbounded run there accumulates goroutines fastest.
// Without this test the hinted path could be left unbounded and the whole suite
// would still pass.
func TestConnectHintsBoundedByTimeout(t *testing.T) {
	t.Parallel()

	n, c := newNetwork(), &clock{t: midWindow(1000)}
	a, b := newNode(t, 1), newNode(t, 1)

	connect, _, canceled := blockingConnect()
	resolve := func(swarm.Address) (*bzz.Address, error) { return a.addr, nil }
	reader := newServiceResolving(t, n, b, c, connect, resolve)
	reader.SetDiscoverTimeout(200 * time.Millisecond)

	reader.ConnectHints(context.Background(), []swarm.Address{a.addr.Overlay})
	assertBoundedByTimeout(t, canceled)
}

func assertBoundedByTimeout(t *testing.T, canceled <-chan error) {
	t.Helper()

	select {
	case err := <-canceled:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("the connect saw %v, want context.DeadlineExceeded", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the timeout did not abandon a connect that never returns")
	}
}

// TestLookupCountersDistinguishCacheFromWork is the pair the cache arm of the
// measurement decides on. An earlier version of the spec defined these two so
// that a cache hit raised both, which made that arm demand a total the code
// cannot produce.
func TestLookupCountersDistinguishCacheFromWork(t *testing.T) {
	t.Parallel()

	n, c := newNetwork(), &clock{t: midWindow(1000)}
	a, b := newNode(t, 1), newNode(t, 1)

	pa := newService(t, n, a, c, nil)
	reader := newService(t, n, b, c, nil)
	k := announceOne(t, pa)

	// a key that is not a plain reference must touch neither counter
	if _, err := reader.Lookup(context.Background(), []byte{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	got := reader.Counters(t)
	if got.LookupsCompleted != 0 || got.LookupsServedFromCache != 0 {
		t.Fatalf("a short key moved the counters: completed=%v cached=%v",
			got.LookupsCompleted, got.LookupsServedFromCache)
	}

	if _, err := reader.Lookup(context.Background(), k); err != nil {
		t.Fatal(err)
	}
	got = reader.Counters(t)
	if got.LookupsCompleted != 1 || got.LookupsServedFromCache != 0 {
		t.Fatalf("after a real lookup: completed=%v cached=%v, want 1 and 0",
			got.LookupsCompleted, got.LookupsServedFromCache)
	}

	if _, err := reader.Lookup(context.Background(), k); err != nil {
		t.Fatal(err)
	}
	got = reader.Counters(t)
	if got.LookupsCompleted != 1 || got.LookupsServedFromCache != 1 {
		t.Fatalf("after a cache hit: completed=%v cached=%v, want 1 and 1",
			got.LookupsCompleted, got.LookupsServedFromCache)
	}
	if got.LookupsCanceled != 0 {
		t.Fatalf("a lookup was counted as canceled: %v", got.LookupsCanceled)
	}
}

// TestLookupCanceledCounted covers arm 1's primary observable, which drives its
// first reject clause. Without it the counter could be wired to nothing and the
// arm would read flat and pass.
func TestLookupCanceledCounted(t *testing.T) {
	t.Parallel()

	n, c := newNetwork(), &clock{t: midWindow(1000)}
	a, b := newNode(t, 1), newNode(t, 1)

	pa := newService(t, n, a, c, nil)
	reader := newService(t, n, b, c, nil)
	k := announceOne(t, pa)

	dead, cancel := context.WithCancel(context.Background())
	cancel()

	// Lookup is called directly, so the caller's cancellation is not detached
	// the way Discover detaches it
	if _, err := reader.Lookup(dead, k); err == nil {
		t.Fatal("a lookup with a cancelled context returned no error")
	}

	got := reader.Counters(t)
	if got.LookupsCanceled != 1 {
		t.Fatalf("canceled=%v, want 1", got.LookupsCanceled)
	}
	if got.LookupsCompleted != 0 {
		t.Fatalf("a canceled lookup was also counted as completed: %v", got.LookupsCompleted)
	}
}

// TestStartedCountersAreTheDenominators covers the two counters that separate
// "the work ran and found nothing" from "the work never ran", which is the only
// thing the measurement uses them for.
func TestStartedCountersAreTheDenominators(t *testing.T) {
	t.Parallel()

	n, c := newNetwork(), &clock{t: midWindow(1000)}
	a, b := newNode(t, 1), newNode(t, 1)

	connect, dialed := recordingConnect()
	resolve := func(swarm.Address) (*bzz.Address, error) { return a.addr, nil }
	pa := newService(t, n, a, c, nilConnect)
	reader := newServiceResolving(t, n, b, c, connect, resolve)
	k := announceOne(t, pa)

	reader.Discover(context.Background(), k, &adder{})
	<-dialed
	reader.ConnectHints(context.Background(), []swarm.Address{a.addr.Overlay})
	<-dialed
	_ = reader.Close()

	got := reader.Counters(t)
	if got.DiscoveriesStarted != 1 {
		t.Fatalf("discoveries=%v, want 1", got.DiscoveriesStarted)
	}
	if got.HintedConnectsStarted != 1 {
		t.Fatalf("hinted connects=%v, want 1", got.HintedConnectsStarted)
	}
}

// TestConnectCountersDistinguishDialFromAlreadyConnected is what stops the
// measurement's dial arms passing without a dial. The cancellation cases are
// what stop a shutdown showing up as providers that cannot be reached: without
// them one Close could add a failure per record.
func TestConnectCountersDistinguishDialFromAlreadyConnected(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name                     string
		already                  bool
		err                      error
		dialed, alreadyC, failed float64
	}{
		{"a dial", false, nil, 1, 0, 0},
		{"already connected", true, nil, 0, 1, 0},
		{"a failure", false, errDialRefused, 0, 0, 1},
		{"canceled", false, context.Canceled, 0, 0, 0},
		{"timed out", false, context.DeadlineExceeded, 0, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			n, clk := newNetwork(), &clock{t: midWindow(1000)}
			a, b := newNode(t, 1), newNode(t, 1)

			called := make(chan struct{}, 4)
			pa := newService(t, n, a, clk, nilConnect)
			reader := newService(t, n, b, clk, func(context.Context, *bzz.Address) (bool, error) {
				called <- struct{}{}
				return tc.already, tc.err
			})
			k := announceOne(t, pa)

			reader.Discover(context.Background(), k, &adder{})
			select {
			case <-called:
			case <-time.After(10 * time.Second):
				t.Fatal("the discovery never reached the connect")
			}
			_ = reader.Close()

			got := reader.Counters(t)
			if got.ConnectsDialed != tc.dialed ||
				got.ConnectsAlreadyConnected != tc.alreadyC ||
				got.ConnectsFailed != tc.failed {
				t.Fatalf("dialed=%v already=%v failed=%v, want %v %v %v",
					got.ConnectsDialed, got.ConnectsAlreadyConnected, got.ConnectsFailed,
					tc.dialed, tc.alreadyC, tc.failed)
			}
		})
	}
}
