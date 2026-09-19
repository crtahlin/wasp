// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package providers_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
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

	connect, running, canceled := blockingConnect()
	pa := newService(t, n, a, c, nilConnect)
	reader := newService(t, n, b, c, connect)
	// Two seconds, not 200 milliseconds. This bound has to cover the LOOKUP as
	// well as the dial, and the lookup fans out to Slots goroutines plus one
	// per candidate; under -race on a single core with the rest of the package
	// running in parallel it does not finish in 200 ms, the records come back
	// empty, Connect is never called, and this test fails blaming the timeout
	// for something the timeout did not do. Measured: three failures in five at
	// -race -cpu=1. Two seconds is still 15x under the default, so it still
	// proves the bound is the service's and not the default.
	reader.SetDiscoverTimeout(2 * time.Second)
	if got := reader.DiscoverBound(); got != 2*time.Second {
		t.Fatalf("the bound did not take effect: %v", got)
	}
	k := announceOne(t, pa)

	reader.Discover(context.Background(), k, &adder{})

	// wait for the dial first, so "never reached the dial" reports as itself
	// rather than as a timeout failure
	select {
	case <-running:
	case <-time.After(20 * time.Second):
		t.Fatal("the discovery never reached the connect")
	}
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

	// 200 ms is safe here, and this is the stronger half of the pair for the
	// same reason: the hinted path performs no lookup, so the bound covers only
	// the dial.
	connect, running, canceled := blockingConnect()
	resolve := func(swarm.Address) (*bzz.Address, error) { return a.addr, nil }
	reader := newServiceResolving(t, n, b, c, connect, resolve)
	reader.SetDiscoverTimeout(200 * time.Millisecond)

	reader.ConnectHints(context.Background(), []swarm.Address{a.addr.Overlay})

	select {
	case <-running:
	case <-time.After(20 * time.Second):
		t.Fatal("the hinted connect never reached the dial")
	}
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

// TestDiscoveriesStartedIsADenominator is the cache arm of the measurement
// expressed as a unit test: three discoveries of the same key inside the cache
// window perform one real lookup and two cache hits, and the started counter
// must still read three.
//
// An earlier version of this test did one discovery and asserted each counter
// was 1, which tests "it increments when everything works" and not the property
// the counter exists for. Moving DiscoveriesStarted so that it counted only
// runs whose lookup returned records passed that version, while destroying the
// denominator outright.
//
// The three calls are sequenced through the dial rather than started together,
// because concurrent runs race the cache and give three real lookups.
func TestDiscoveriesStartedIsADenominator(t *testing.T) {
	t.Parallel()

	n, c := newNetwork(), &clock{t: midWindow(1000)}
	a, b := newNode(t, 1), newNode(t, 1)

	connect, dialed := recordingConnect()
	pa := newService(t, n, a, c, nilConnect)
	reader := newService(t, n, b, c, connect)
	k := announceOne(t, pa)

	for i := range 3 {
		reader.Discover(context.Background(), k, &adder{})
		select {
		case <-dialed:
		case <-time.After(20 * time.Second):
			t.Fatalf("discovery %d never reached the dial", i+1)
		}
	}
	_ = reader.Close()

	got := reader.Counters(t)
	if got.DiscoveriesStarted != 3 {
		t.Fatalf("discoveries=%v, want 3", got.DiscoveriesStarted)
	}
	if got.LookupsCompleted != 1 {
		t.Fatalf("completed=%v, want 1: only the first run should read the network", got.LookupsCompleted)
	}
	if got.LookupsServedFromCache != 2 {
		t.Fatalf("cached=%v, want 2", got.LookupsServedFromCache)
	}
}

// TestDiscoveriesStartedCountsAFruitlessRun is the denominator property itself,
// and the one a three-run cache test does not reach. Every lookup in that test
// finds records, so moving the increment to fire only when records came back
// survives it. This one discovers a key nobody announced: the lookup runs,
// completes, and finds nothing, and the counter must still say a discovery
// happened. That is the whole distinction the counter exists to make, in the
// spec's words, one completed lookup against no discovery having run at all.
func TestDiscoveriesStartedCountsAFruitlessRun(t *testing.T) {
	t.Parallel()

	n, c := newNetwork(), &clock{t: midWindow(1000)}
	b := newNode(t, 1)

	var calls int64
	reader := newService(t, n, b, c, func(context.Context, *bzz.Address) (bool, error) {
		atomic.AddInt64(&calls, 1)
		return false, nil
	})

	// nobody announced this key
	found := &adder{}
	reader.Discover(context.Background(), swarm.RandAddress(t).Bytes(), found)

	// There is no dial to wait on here, so wait for the lookup to land. Closing
	// first would cancel it and this would count a cancellation instead, which
	// is not the property under test.
	deadline := time.Now().Add(20 * time.Second)
	for reader.Counters(t).LookupsCompleted == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the lookup never completed")
		}
		time.Sleep(5 * time.Millisecond)
	}
	_ = reader.Close()

	got := reader.Counters(t)
	if got.DiscoveriesStarted != 1 {
		t.Fatalf("discoveries=%v, want 1: a run that found nothing still ran", got.DiscoveriesStarted)
	}
	if got.LookupsCompleted != 1 {
		t.Fatalf("completed=%v, want 1", got.LookupsCompleted)
	}
	// These last two are corroboration, not the property: with no records there
	// is nothing to dial or add either way. The guard itself is held by
	// TestCancelledRunStopsDialingButKeepsTheSet.
	if c := atomic.LoadInt64(&calls); c != 0 {
		t.Fatalf("a fruitless run dialed %d times, want 0", c)
	}
	if len(found.list()) != 0 {
		t.Fatalf("a fruitless run added %v to the set", found.list())
	}
}

// TestHintedConnectsStartedCounted is the same denominator for the hinted path,
// which has no cache in front of it, so counting once per call is the whole
// property.
func TestHintedConnectsStartedCounted(t *testing.T) {
	t.Parallel()

	n, c := newNetwork(), &clock{t: midWindow(1000)}
	a, b := newNode(t, 1), newNode(t, 1)

	connect, dialed := recordingConnect()
	resolve := func(swarm.Address) (*bzz.Address, error) { return a.addr, nil }
	reader := newServiceResolving(t, n, b, c, connect, resolve)

	reader.ConnectHints(context.Background(), []swarm.Address{a.addr.Overlay})
	select {
	case <-dialed:
	case <-time.After(20 * time.Second):
		t.Fatal("the hinted connect never reached the dial")
	}
	_ = reader.Close()

	if got := reader.Counters(t).HintedConnectsStarted; got != 1 {
		t.Fatalf("hinted connects=%v, want 1", got)
	}
}

// TestCancelledRunStopsDialingButKeepsTheSet holds the guard the spec asserts:
// a run cut off before it dials adds every overlay it found to the preferred
// set, which outlives the request, and calls Connect for none of them. Without
// this test both guards could be deleted and nothing would notice, because
// countConnect no longer counts a cancelled connect.
func TestCancelledRunStopsDialingButKeepsTheSet(t *testing.T) {
	t.Parallel()

	n, c := newNetwork(), &clock{t: midWindow(1000)}
	a, b := newNode(t, 1), newNode(t, 1)

	var calls int64
	pa := newService(t, n, a, c, nilConnect)
	reader := newService(t, n, b, c, func(context.Context, *bzz.Address) (bool, error) {
		atomic.AddInt64(&calls, 1)
		return false, nil
	})
	k := announceOne(t, pa)

	// A NEGATIVE bound, not a tiny positive one. context.WithDeadline cancels
	// synchronously when the deadline has already passed, so the run's context
	// is dead before the loop can look at it, on every platform. With 1ns it
	// schedules a timer instead and whether that fires before the check is a
	// race, which failed on Windows where the timer granularity is coarse.
	found := &adder{}
	reader.SetDiscoverTimeout(-time.Second)
	reader.Discover(context.Background(), k, found)
	_ = reader.Close()

	if got := atomic.LoadInt64(&calls); got != 0 {
		t.Fatalf("a run cut off before dialing called Connect %d times, want 0", got)
	}
	if got := found.list(); len(got) != 1 {
		t.Fatalf("the cut-off run added %v to the set, want the provider kept", got)
	}
}

// TestCancelledHintedRunDoesNotDial is the same guard on the hinted path. It
// has no set to keep, so the whole property is that it stops.
func TestCancelledHintedRunDoesNotDial(t *testing.T) {
	t.Parallel()

	n, c := newNetwork(), &clock{t: midWindow(1000)}
	a, b := newNode(t, 1), newNode(t, 1)

	var calls int64
	resolve := func(swarm.Address) (*bzz.Address, error) { return a.addr, nil }
	reader := newServiceResolving(t, n, b, c, func(context.Context, *bzz.Address) (bool, error) {
		atomic.AddInt64(&calls, 1)
		return false, nil
	}, resolve)

	// negative for the reason in TestCancelledRunStopsDialingButKeepsTheSet:
	// an already-passed deadline cancels synchronously, a 1ns one races a timer
	reader.SetDiscoverTimeout(-time.Second)
	reader.ConnectHints(context.Background(), []swarm.Address{a.addr.Overlay})
	_ = reader.Close()

	if got := atomic.LoadInt64(&calls); got != 0 {
		t.Fatalf("a hinted run cut off before dialing called Connect %d times, want 0", got)
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
