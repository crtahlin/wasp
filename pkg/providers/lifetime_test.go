// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package providers_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ethersphere/bee/v2/pkg/bzz"
	"github.com/ethersphere/bee/v2/pkg/log"
	"github.com/ethersphere/bee/v2/pkg/postage"
	postagemock "github.com/ethersphere/bee/v2/pkg/postage/mock"
	"github.com/ethersphere/bee/v2/pkg/providers"
	statestoremock "github.com/ethersphere/bee/v2/pkg/statestore/mock"
	"github.com/ethersphere/bee/v2/pkg/swarm"
)

// These cover issue #369: discovery and hinted connection are node-scoped
// background work and must not die with the request that started them. See
// docs/experiments/content-providers/discovery-lifetime.md.

var errNotInAddressBook = errors.New("not in the address book")

// newServiceResolving is newServiceWith plus a Resolve, which ConnectHints
// needs and which the shared helper leaves nil.
func newServiceResolving(
	t *testing.T,
	n *network,
	nd node,
	c *clock,
	connect func(context.Context, *bzz.Address) error,
	resolve func(swarm.Address) (*bzz.Address, error),
) *providers.Service {
	t.Helper()

	svc, err := providers.New(providers.Options{
		Logger:    log.Noop,
		NetworkID: networkID,
		Overlay:   nd.addr.Overlay,
		Signer:    nd.signer,
		Getter:    n,
		Fetcher:   n,
		Uploader:  n.session,
		Stamper: func([]byte) (postage.Stamper, func() error, error) {
			return postagemock.NewStamper(), func() error { return nil }, nil
		},
		Address: func() (*bzz.Address, error) { return nd.addr, nil },
		Connect: connect,
		Resolve: resolve,
		Store:   statestoremock.NewStateStore(),
	})
	if err != nil {
		t.Fatal(err)
	}
	svc.SetNow(c.now)
	t.Cleanup(func() { _ = svc.Close() })
	return svc
}

// TestDiscoverSurvivesCallerCancel is the defect itself: before the fix the
// goroutine derived from the caller, so a context already cancelled meant the
// lookup never ran. The context is cancelled before the call rather than during
// it, which removes the race from the test.
func TestDiscoverSurvivesCallerCancel(t *testing.T) {
	t.Parallel()

	n, c := newNetwork(), &clock{t: midWindow(1000)}
	a, b := newNode(t, 1), newNode(t, 1)

	// the stub respects the context, as a real dial does: without that the
	// test cannot see the defect at all, since the in-memory network ignores
	// cancellation and a cancelled discovery would still appear to connect
	dialed := make(chan swarm.Address, 4)
	connect := func(ctx context.Context, addr *bzz.Address) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		dialed <- addr.Overlay
		return nil
	}

	pa := newService(t, n, a, c, connect)
	reader := newService(t, n, b, c, connect)
	k := swarm.RandAddress(t).Bytes()
	if err := pa.Announce(context.Background(), k, batch); err != nil {
		t.Fatal(err)
	}

	dead, cancel := context.WithCancel(context.Background())
	cancel()

	found := &adder{}
	reader.Discover(dead, k, found)

	// Wait for the dial rather than relying on Close to sequence it: Close
	// cancels the service context before waiting on the goroutine, so calling
	// it first can cancel the work in flight and fail a correct
	// implementation.
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

	// as above, the stub respects the context
	dialed := make(chan swarm.Address, 4)
	connect := func(ctx context.Context, addr *bzz.Address) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		dialed <- addr.Overlay
		return nil
	}
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

	// as above, wait for the dial before closing
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
// and a Close racing the call would make the goroutine never start and the
// test pass having proved nothing.
func TestDiscoverStopsOnClose(t *testing.T) {
	t.Parallel()

	n, c := newNetwork(), &clock{t: midWindow(1000)}
	a, b := newNode(t, 1), newNode(t, 1)

	running := make(chan struct{})
	sawCancel := make(chan error, 1)
	var once bool
	connect := func(ctx context.Context, _ *bzz.Address) error {
		if !once {
			once = true
			close(running)
		}
		<-ctx.Done()
		sawCancel <- ctx.Err()
		return ctx.Err()
	}

	pa := newService(t, n, a, c, func(context.Context, *bzz.Address) error { return nil })
	reader := newService(t, n, b, c, connect)
	k := swarm.RandAddress(t).Bytes()
	if err := pa.Announce(context.Background(), k, batch); err != nil {
		t.Fatal(err)
	}

	reader.Discover(context.Background(), k, &adder{})

	select {
	case <-running:
	case <-time.After(10 * time.Second):
		t.Fatal("the discovery goroutine never reached the connect")
	}

	done := make(chan struct{})
	go func() {
		_ = reader.Close()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not return within two seconds")
	}

	select {
	case err := <-sawCancel:
		if err == nil {
			t.Fatal("the blocked connect saw no cancellation")
		}
	default:
		t.Fatal("the blocked connect did not return")
	}
}

// TestDiscoverBoundedByTimeout is the other half of that: a connect that never
// returns is abandoned rather than held for the life of the node.
func TestDiscoverBoundedByTimeout(t *testing.T) {
	t.Parallel()

	defer providers.SetDiscoverTimeout(200 * time.Millisecond)()

	n, c := newNetwork(), &clock{t: midWindow(1000)}
	a, b := newNode(t, 1), newNode(t, 1)

	sawCancel := make(chan error, 1)
	connect := func(ctx context.Context, _ *bzz.Address) error {
		<-ctx.Done()
		sawCancel <- ctx.Err()
		return ctx.Err()
	}

	pa := newService(t, n, a, c, func(context.Context, *bzz.Address) error { return nil })
	reader := newService(t, n, b, c, connect)
	k := swarm.RandAddress(t).Bytes()
	if err := pa.Announce(context.Background(), k, batch); err != nil {
		t.Fatal(err)
	}

	reader.Discover(context.Background(), k, &adder{})

	select {
	case err := <-sawCancel:
		if err == nil {
			t.Fatal("the connect was not cancelled by the timeout")
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
	k := swarm.RandAddress(t).Bytes()
	if err := pa.Announce(context.Background(), k, batch); err != nil {
		t.Fatal(err)
	}

	// a key that is not a plain reference must touch neither counter
	if _, err := reader.Lookup(context.Background(), []byte{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	c1 := reader.Counters()
	if c1.LookupsCompleted != 0 || c1.LookupsFromCache != 0 {
		t.Fatalf("a short key moved the counters: completed=%v cached=%v",
			c1.LookupsCompleted, c1.LookupsFromCache)
	}

	if _, err := reader.Lookup(context.Background(), k); err != nil {
		t.Fatal(err)
	}
	c2 := reader.Counters()
	if c2.LookupsCompleted != 1 || c2.LookupsFromCache != 0 {
		t.Fatalf("after a real lookup: completed=%v cached=%v, want 1 and 0",
			c2.LookupsCompleted, c2.LookupsFromCache)
	}

	if _, err := reader.Lookup(context.Background(), k); err != nil {
		t.Fatal(err)
	}
	c3 := reader.Counters()
	if c3.LookupsCompleted != 1 || c3.LookupsFromCache != 1 {
		t.Fatalf("after a cache hit: completed=%v cached=%v, want 1 and 1",
			c3.LookupsCompleted, c3.LookupsFromCache)
	}
}

// TestConnectCountersDistinguishDialFromAlreadyConnected is what stops the
// measurement's dial arms passing without a dial. Options.Connect reports an
// already-connected peer with a sentinel rather than a bare nil, because only
// it can tell the two apart.
func TestConnectCountersDistinguishDialFromAlreadyConnected(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name                     string
		err                      error
		dialled, already, failed float64
	}{
		{"a dial", nil, 1, 0, 0},
		{"already connected", providers.ErrAlreadyConnected, 0, 1, 0},
		{"a failure", errNotInAddressBook, 0, 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			n, c := newNetwork(), &clock{t: midWindow(1000)}
			a, b := newNode(t, 1), newNode(t, 1)

			pa := newService(t, n, a, c, nil)
			reader := newService(t, n, b, c, func(context.Context, *bzz.Address) error {
				return tc.err
			})
			k := swarm.RandAddress(t).Bytes()
			if err := pa.Announce(context.Background(), k, batch); err != nil {
				t.Fatal(err)
			}

			// wait for the discovery to record an outcome before closing,
			// for the reason given in TestDiscoverSurvivesCallerCancel
			done := &adder{}
			reader.Discover(context.Background(), k, done)
			deadline := time.Now().Add(10 * time.Second)
			for {
				c := reader.Counters()
				if c.Dialled+c.AlreadyConnected+c.Failed > 0 || time.Now().After(deadline) {
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			_ = reader.Close()

			got := reader.Counters()
			if got.Dialled != tc.dialled || got.AlreadyConnected != tc.already || got.Failed != tc.failed {
				t.Fatalf("dialled=%v already=%v failed=%v, want %v %v %v",
					got.Dialled, got.AlreadyConnected, got.Failed,
					tc.dialled, tc.already, tc.failed)
			}
		})
	}
}
