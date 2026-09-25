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

// dialLog is a Connect that records every address it is asked to dial and
// succeeds only for the addresses ok accepts.
type dialLog struct {
	mu     sync.Mutex
	dialed []*bzz.Address
	ok     func(*bzz.Address) bool
}

func (d *dialLog) connect(ctx context.Context, addr *bzz.Address) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	d.mu.Lock()
	d.dialed = append(d.dialed, addr)
	d.mu.Unlock()
	if d.ok != nil && !d.ok(addr) {
		return false, errors.New("dial failed")
	}
	return false, nil
}

func (d *dialLog) list() []*bzz.Address {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]*bzz.Address(nil), d.dialed...)
}

var errUnknownPeer = errors.New("not in the address book")

func notInAddressBook(swarm.Address) (*bzz.Address, error) {
	return nil, errUnknownPeer
}

// waitRun waits for a hint run to end, or fails the test.
func waitRun(t *testing.T, run *providers.HintRun) {
	t.Helper()
	select {
	case <-run.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("the hinted connect run never ended")
	}
}

// announced returns a content key announced by each of the given providers.
func announced(t *testing.T, n *network, c *clock, nodes ...node) []byte {
	t.Helper()
	k := swarm.RandAddress(t).Bytes()
	for _, nd := range nodes {
		p := newService(t, n, nd, c, nil)
		if err := p.Announce(context.Background(), k, batch); err != nil {
			t.Fatal(err)
		}
	}
	return k
}

// TestConnectHintsDialsRecordWhenUnknown: a named provider missing from the
// address book is dialled at the address in its verified record. Before #499
// nothing was dialled at all, and the download answered 404.
func TestConnectHintsDialsRecordWhenUnknown(t *testing.T) {
	t.Parallel()

	n, c := newNetwork(), &clock{t: midWindow(1000)}
	a, b := newNode(t, 1), newNode(t, 1)
	k := announced(t, n, c, a)

	dials := &dialLog{}
	reader := newServiceResolving(t, n, b, c, dials.connect, notInAddressBook)
	run := reader.ConnectHints(context.Background(), []swarm.Address{a.addr.Overlay}, k)
	waitRun(t, run)

	got := dials.list()
	if len(got) != 1 || !got[0].Equal(a.addr) {
		t.Fatalf("dialed %v, want the provider's record address", got)
	}
	if o := run.Outcome(); o.Connected != 1 || o.NoAddress != 0 || o.DialFailed != 0 {
		t.Fatalf("outcome %+v, want one connected", o)
	}
	if got := reader.Counters(t).HintedRecordDials; got != 1 {
		t.Fatalf("record dials %v, want 1", got)
	}
}

// TestConnectHintsFallsBackAfterStaleDial: an address book entry that no
// longer works, as after the provider's public IP changed, is followed by a
// dial at the record's address.
func TestConnectHintsFallsBackAfterStaleDial(t *testing.T) {
	t.Parallel()

	n, c := newNetwork(), &clock{t: midWindow(1000)}
	a, b := newNode(t, 1), newNode(t, 1)
	k := announced(t, n, c, a)

	stale := &bzz.Address{Overlay: a.addr.Overlay}
	dials := &dialLog{ok: func(addr *bzz.Address) bool { return addr.Equal(a.addr) }}
	reader := newServiceResolving(t, n, b, c, dials.connect, func(swarm.Address) (*bzz.Address, error) { return stale, nil })
	run := reader.ConnectHints(context.Background(), []swarm.Address{a.addr.Overlay}, k)
	waitRun(t, run)

	got := dials.list()
	if len(got) != 2 || got[0] != stale || !got[1].Equal(a.addr) {
		t.Fatalf("dialed %v, want the stale address and then the record's", got)
	}
	if o := run.Outcome(); o.Connected != 1 || o.DialFailed != 0 {
		t.Fatalf("outcome %+v, want one connected", o)
	}
}

// TestConnectHintsOnlyNamedProviders: a hint names the providers to use; the
// lookup may find others, and they are not dialled.
func TestConnectHintsOnlyNamedProviders(t *testing.T) {
	t.Parallel()

	n, c := newNetwork(), &clock{t: midWindow(1000)}
	a, other, b := newNode(t, 1), newNode(t, 1), newNode(t, 1)
	k := announced(t, n, c, a, other)

	dials := &dialLog{}
	reader := newServiceResolving(t, n, b, c, dials.connect, notInAddressBook)
	waitRun(t, reader.ConnectHints(context.Background(), []swarm.Address{a.addr.Overlay}, k))

	for _, d := range dials.list() {
		if d.Overlay.Equal(other.addr.Overlay) {
			t.Fatal("dialed a provider the hint did not name")
		}
	}
	if len(dials.list()) != 1 {
		t.Fatalf("dialed %v, want only the named provider", dials.list())
	}
}

// TestConnectHintsConnectedNeedsNoLookup: a provider reachable from the
// address book is used at once, with no lookup.
func TestConnectHintsConnectedNeedsNoLookup(t *testing.T) {
	t.Parallel()

	n, c := newNetwork(), &clock{t: midWindow(1000)}
	a, b := newNode(t, 1), newNode(t, 1)
	k := announced(t, n, c, a)

	dials := &dialLog{}
	reader := newServiceResolving(t, n, b, c, dials.connect, func(swarm.Address) (*bzz.Address, error) { return a.addr, nil })
	run := reader.ConnectHints(context.Background(), []swarm.Address{a.addr.Overlay}, k)
	waitRun(t, run)

	counts := reader.Counters(t)
	if counts.LookupsCompleted+counts.LookupsServedFromCache != 0 {
		t.Fatalf("looked up a provider the address book already reached: %+v", counts)
	}
	if o := run.Outcome(); o.Connected != 1 {
		t.Fatalf("outcome %+v, want one connected", o)
	}
}

// TestConnectHintsWithoutKeyUsesAddressBookOnly: /chunks and /feeds carry no
// content key, so there is nothing to look up; an unknown provider is
// reported as having no address, and nothing is dialled.
func TestConnectHintsWithoutKeyUsesAddressBookOnly(t *testing.T) {
	t.Parallel()

	n, c := newNetwork(), &clock{t: midWindow(1000)}
	a, b := newNode(t, 1), newNode(t, 1)
	announced(t, n, c, a)

	dials := &dialLog{}
	reader := newServiceResolving(t, n, b, c, dials.connect, notInAddressBook)
	run := reader.ConnectHints(context.Background(), []swarm.Address{a.addr.Overlay}, nil)
	waitRun(t, run)

	if len(dials.list()) != 0 {
		t.Fatalf("dialed %v with no content key", dials.list())
	}
	if o := run.Outcome(); o.NoAddress != 1 || o.Connected != 0 {
		t.Fatalf("outcome %+v, want one with no address", o)
	}
	counts := reader.Counters(t)
	if counts.LookupsCompleted+counts.LookupsServedFromCache != 0 {
		t.Fatalf("looked up with no content key: %+v", counts)
	}
}

// TestConnectHintsOneLookupPerRun: several named providers that all need
// their records share one lookup.
func TestConnectHintsOneLookupPerRun(t *testing.T) {
	t.Parallel()

	n, c := newNetwork(), &clock{t: midWindow(1000)}
	a, a2, b := newNode(t, 1), newNode(t, 1), newNode(t, 1)
	k := announced(t, n, c, a, a2)

	// Fail every dial so the run tries both providers rather than ending
	// Done at the first connection.
	dials := &dialLog{ok: func(*bzz.Address) bool { return false }}
	reader := newServiceResolving(t, n, b, c, dials.connect, notInAddressBook)
	run := reader.ConnectHints(context.Background(), []swarm.Address{a.addr.Overlay, a2.addr.Overlay}, k)
	waitRun(t, run)

	counts := reader.Counters(t)
	if got := counts.LookupsCompleted + counts.LookupsServedFromCache; got != 1 {
		t.Fatalf("lookups %v, want 1 for the whole run", got)
	}
	if len(dials.list()) != 2 {
		t.Fatalf("dialed %v, want both named providers", dials.list())
	}
	if o := run.Outcome(); o.DialFailed != 2 {
		t.Fatalf("outcome %+v, want two that could not be dialled", o)
	}
}

// TestConnectHintsUnannouncedHasNoAddress: content that was never announced
// has no record, so a named provider missing from the address book is
// reported as having no address, which is what the 404 message names.
func TestConnectHintsUnannouncedHasNoAddress(t *testing.T) {
	t.Parallel()

	n, c := newNetwork(), &clock{t: midWindow(1000)}
	a, b := newNode(t, 1), newNode(t, 1)

	dials := &dialLog{}
	reader := newServiceResolving(t, n, b, c, dials.connect, notInAddressBook)
	run := reader.ConnectHints(context.Background(), []swarm.Address{a.addr.Overlay}, swarm.RandAddress(t).Bytes())
	waitRun(t, run)

	if len(dials.list()) != 0 {
		t.Fatalf("dialed %v for content nobody announced", dials.list())
	}
	if o := run.Outcome(); o.NoAddress != 1 {
		t.Fatalf("outcome %+v, want one with no address", o)
	}
}

// TestConnectHintsDoneAtFirstConnection: the download waits only for the
// first named provider, not for every dial, so a slow second dial does not
// delay it.
func TestConnectHintsDoneAtFirstConnection(t *testing.T) {
	t.Parallel()

	n, c := newNetwork(), &clock{t: midWindow(1000)}
	a, slow, b := newNode(t, 1), newNode(t, 1), newNode(t, 1)

	release := make(chan struct{})
	defer close(release)
	connect := func(ctx context.Context, addr *bzz.Address) (bool, error) {
		if addr.Overlay.Equal(slow.addr.Overlay) {
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
		return false, ctx.Err()
	}
	resolve := func(o swarm.Address) (*bzz.Address, error) {
		if o.Equal(a.addr.Overlay) {
			return a.addr, nil
		}
		return slow.addr, nil
	}
	reader := newServiceResolving(t, n, b, c, connect, resolve)
	run := reader.ConnectHints(context.Background(), []swarm.Address{a.addr.Overlay, slow.addr.Overlay}, nil)

	select {
	case <-run.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Done waited for every dial, not the first connection")
	}
}

// TestConnectHintsReachableSecondNotDelayed: a provider named second that the
// address book reaches is connected, and Done closed, while the dial to a dead
// address named first is still running.
func TestConnectHintsReachableSecondNotDelayed(t *testing.T) {
	t.Parallel()

	n, c := newNetwork(), &clock{t: midWindow(1000)}
	dead, ok, b := newNode(t, 1), newNode(t, 1), newNode(t, 1)

	release := make(chan struct{})
	defer close(release)
	connect := func(ctx context.Context, addr *bzz.Address) (bool, error) {
		if addr.Overlay.Equal(dead.addr.Overlay) {
			select {
			case <-release:
			case <-ctx.Done():
			}
			return false, errors.New("dial failed")
		}
		return false, ctx.Err()
	}
	resolve := func(o swarm.Address) (*bzz.Address, error) {
		if o.Equal(dead.addr.Overlay) {
			return dead.addr, nil
		}
		return ok.addr, nil
	}
	reader := newServiceResolving(t, n, b, c, connect, resolve)
	run := reader.ConnectHints(context.Background(), []swarm.Address{dead.addr.Overlay, ok.addr.Overlay}, swarm.RandAddress(t).Bytes())

	select {
	case <-run.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("a reachable provider waited for the dial to one named ahead of it")
	}
}

// TestConnectHintsNoRedialOfSameAddress: a record that names the address the
// address book dial just failed on is not dialled again.
func TestConnectHintsNoRedialOfSameAddress(t *testing.T) {
	t.Parallel()

	n, c := newNetwork(), &clock{t: midWindow(1000)}
	a, b := newNode(t, 1), newNode(t, 1)
	k := announced(t, n, c, a)

	dials := &dialLog{ok: func(*bzz.Address) bool { return false }}
	reader := newServiceResolving(t, n, b, c, dials.connect, func(swarm.Address) (*bzz.Address, error) { return a.addr, nil })
	run := reader.ConnectHints(context.Background(), []swarm.Address{a.addr.Overlay}, k)
	waitRun(t, run)

	if got := dials.list(); len(got) != 1 {
		t.Fatalf("dialed %v, want the one address once", got)
	}
	if o := run.Outcome(); o.DialFailed != 1 {
		t.Fatalf("outcome %+v, want one that could not be dialled", o)
	}
}
