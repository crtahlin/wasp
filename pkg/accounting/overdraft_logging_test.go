// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package accounting_test

import (
	"bytes"
	"context"
	"errors"
	"math/big"
	"regexp"
	"strings"
	"testing"

	"github.com/ethersphere/bee/v2/pkg/accounting"
	"github.com/ethersphere/bee/v2/pkg/log"
	p2pmock "github.com/ethersphere/bee/v2/pkg/p2p/mock"
	"github.com/ethersphere/bee/v2/pkg/statestore/mock"
	"github.com/ethersphere/bee/v2/pkg/swarm"
)

const overdraftMsg = "credit refused, would overdraw"

// captureLogger builds a logger that writes to buf with V-levels enabled.
//
// WithVerbosity is not optional here. Without it the logger sits at the
// default VerbosityDebug, which is 0, and Debug gates on verbosity >= l.v, so
// nothing registered at V(2) is emitted and any test asserting on the captured
// content passes vacuously. TestAccountingOverdraftSilentBelowV2 exists partly
// to catch that.
func captureLogger(name string, buf *bytes.Buffer) log.Logger {
	return log.NewLogger(name, log.WithSink(buf), log.WithVerbosity(log.VerbosityAll)).Build()
}

// logField pulls one field out of a captured log line. The sink quotes string
// values and leaves numbers and bools bare, so both forms are accepted.
func logField(t *testing.T, line, key string) string {
	t.Helper()

	m := regexp.MustCompile(`"` + regexp.QuoteMeta(key) + `"=(?:"([^"]*)"|(\S+))`).FindStringSubmatch(line)
	if m == nil {
		t.Fatalf("field %q not found in log line: %s", key, line)
	}
	if m[1] != "" {
		return m[1]
	}
	return m[2]
}

func logFieldInt(t *testing.T, line, key string) *big.Int {
	t.Helper()

	v, ok := new(big.Int).SetString(logField(t, line, key), 10)
	if !ok {
		t.Fatalf("field %q is not an integer in: %s", key, line)
	}
	return v
}

// overdraftingAccounting returns an accounting instance, a connected peer, and
// a price guaranteed to cross the overdraft limit.
func overdraftingAccounting(t *testing.T, logger log.Logger) (*accounting.Accounting, swarm.Address, uint64) {
	t.Helper()

	store := mock.NewStateStore()
	t.Cleanup(func() { store.Close() })

	acc, err := accounting.NewAccounting(testPaymentThreshold, testPaymentTolerance, testPaymentEarly, logger, store, &pricingMock{}, big.NewInt(testRefreshRate), testLightFactor, p2pmock.New())
	if err != nil {
		t.Fatal(err)
	}

	peer, err := swarm.ParseHexAddress("00112233")
	if err != nil {
		t.Fatal(err)
	}
	acc.Connect(peer, true)

	// The limit is the announced threshold plus at most one refresh rate.
	return acc, peer, testPaymentThreshold.Uint64() + 2*uint64(testRefreshRate) + 1
}

// TestAccountingOverdraftLogsRefusal is #353's first requirement: a refused
// credit emits exactly one line at V(2), carrying the two values the gate
// actually compared.
func TestAccountingOverdraftLogsRefusal(t *testing.T) {
	t.Parallel()

	buf := new(bytes.Buffer)
	acc, peer, price := overdraftingAccounting(t, captureLogger(t.Name(), buf))

	_, err := acc.PrepareCredit(context.Background(), peer, price, true)
	if !errors.Is(err, accounting.ErrOverdraft) {
		t.Fatalf("expected overdraft error, got %v", err)
	}

	out := buf.String()
	if n := strings.Count(out, overdraftMsg); n != 1 {
		t.Fatalf("expected exactly one refusal line, got %d in: %s", n, out)
	}

	// The gate is increasedExpectedDebt > paymentThreshold + refreshDue. For a
	// peer that has never refreshed, the elapsed term saturates at its cap, so
	// refreshDue is one full refresh rate.
	wantDebt := new(big.Int).SetUint64(price)
	wantLimit := new(big.Int).Add(testPaymentThreshold, big.NewInt(testRefreshRate))

	if got := logFieldInt(t, out, "expected_debt"); got.Cmp(wantDebt) != 0 {
		t.Errorf("expected_debt: got %v, want %v", got, wantDebt)
	}
	if got := logFieldInt(t, out, "overdraft_limit"); got.Cmp(wantLimit) != 0 {
		t.Errorf("overdraft_limit: got %v, want %v", got, wantLimit)
	}
	if logFieldInt(t, out, "expected_debt").Cmp(logFieldInt(t, out, "overdraft_limit")) <= 0 {
		t.Error("the logged debt does not exceed the logged limit, so the line does not explain the refusal")
	}
}

// TestAccountingNoOverdraftLineOnSuccess is #353's second requirement.
func TestAccountingNoOverdraftLineOnSuccess(t *testing.T) {
	t.Parallel()

	buf := new(bytes.Buffer)
	acc, peer, _ := overdraftingAccounting(t, captureLogger(t.Name(), buf))

	if _, err := acc.PrepareCredit(context.Background(), peer, testPrice, true); err != nil {
		t.Fatalf("expected the credit to be prepared, got %v", err)
	}

	if strings.Contains(buf.String(), overdraftMsg) {
		t.Fatalf("a successful credit emitted a refusal line: %s", buf.String())
	}
}

// TestAccountingOverdraftSilentBelowV2 is #353's third requirement. It is also
// the guard on the other tests: if a capture logger is ever built without
// WithVerbosity, the assertions on content elsewhere would pass vacuously.
func TestAccountingOverdraftSilentBelowV2(t *testing.T) {
	t.Parallel()

	buf := new(bytes.Buffer)
	quiet := log.NewLogger(t.Name(), log.WithSink(buf)).Build()
	acc, peer, price := overdraftingAccounting(t, quiet)

	if _, err := acc.PrepareCredit(context.Background(), peer, price, true); !errors.Is(err, accounting.ErrOverdraft) {
		t.Fatalf("expected overdraft error, got %v", err)
	}
	if _, err := acc.PrepareCredit(context.Background(), peer, testPrice, true); err != nil {
		t.Fatalf("expected the credit to be prepared, got %v", err)
	}

	if strings.Contains(buf.String(), overdraftMsg) {
		t.Fatalf("a logger below V(2) emitted the refusal line: %s", buf.String())
	}
}

// TestAccountingOverdraftLoggingIsBehaviourNeutral is #353's fourth
// requirement. It makes "no behaviour change" a test rather than a claim.
//
// It asserts the concrete expected outcome of each case rather than only that
// the two instances agree. Two instances running identical code agree by
// construction, so an "are they equal" test cannot fail, and would pass for a
// regression that affected both symmetrically.
func TestAccountingOverdraftLoggingIsBehaviourNeutral(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name         string
		overDraw     bool
		wantAction   bool
		wantOverdraw bool
	}{
		{"refused", true, false, true},
		{"allowed", false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			buf := new(bytes.Buffer)
			loud, loudPeer, over := overdraftingAccounting(t, captureLogger(t.Name(), buf))
			quiet, quietPeer, _ := overdraftingAccounting(t, log.Noop)

			price := testPrice
			if tc.overDraw {
				price = over
			}

			for _, run := range []struct {
				label string
				acc   *accounting.Accounting
				peer  swarm.Address
			}{
				{"with logging", loud, loudPeer},
				{"without logging", quiet, quietPeer},
			} {
				action, err := run.acc.PrepareCredit(context.Background(), run.peer, price, true)

				if got := errors.Is(err, accounting.ErrOverdraft); got != tc.wantOverdraw {
					t.Fatalf("%s: overdraft %v, want %v (err %v)", run.label, got, tc.wantOverdraw, err)
				}
				if !tc.wantOverdraw && err != nil {
					t.Fatalf("%s: unexpected error %v", run.label, err)
				}
				if got := action != nil; got != tc.wantAction {
					t.Fatalf("%s: action present %v, want %v", run.label, got, tc.wantAction)
				}
			}
		})
	}
}

// TestAccountingOverdraftTermsReconcile is #353's fifth requirement. It
// asserts the identity the logged terms must satisfy rather than a post-settle
// balance: settle dispatches its work to goroutines and writes no balance
// itself, so asserting a post-settle value would only test whether a goroutine
// happened to win a race.
func TestAccountingOverdraftTermsReconcile(t *testing.T) {
	t.Parallel()

	buf := new(bytes.Buffer)
	acc, peer, price := overdraftingAccounting(t, captureLogger(t.Name(), buf))

	if _, err := acc.PrepareCredit(context.Background(), peer, price, true); !errors.Is(err, accounting.ErrOverdraft) {
		t.Fatalf("expected overdraft error, got %v", err)
	}

	out := buf.String()

	// increasedExpectedDebt = max(-balance, 0) + reservedBalance + price + surplusBalance
	debtTerm := new(big.Int).Neg(logFieldInt(t, out, "settled_balance"))
	if debtTerm.Sign() < 0 {
		debtTerm.SetInt64(0)
	}
	want := new(big.Int).Add(debtTerm, logFieldInt(t, out, "reserved_balance"))
	want.Add(want, logFieldInt(t, out, "price"))
	want.Add(want, logFieldInt(t, out, "surplus_balance"))

	if got := logFieldInt(t, out, "expected_debt"); got.Cmp(want) != 0 {
		t.Fatalf("logged terms do not reconcile: expected_debt %v, terms sum to %v, in: %s", got, want, out)
	}

	// settle_called must be present and parse as a bool, since it is the
	// field the #343 question turns on and is derivable from nothing else.
	if v := logField(t, out, "settle_called"); v != "true" && v != "false" {
		t.Fatalf("settle_called is not a bool: %q", v)
	}
}

// TestAccountingOverdraftTermsReconcileWithDebt is the case that matters for
// #353: a refusal on a peer this node already owes, so the settle branch is
// entered and the balance term is not zero.
//
// Without this, the reconciliation above runs with every term except the price
// at zero, so it degenerates to expected_debt == price, settle_called is only
// ever observed false, and the captured balance the change exists to preserve
// is covered by nothing.
func TestAccountingOverdraftTermsReconcileWithDebt(t *testing.T) {
	t.Parallel()

	buf := new(bytes.Buffer)
	acc, peer, price := overdraftingAccounting(t, captureLogger(t.Name(), buf))

	// settle dispatches to these, so they must exist or the branch panics on a
	// nil func the first time it is entered.
	acc.SetRefreshFunc(func(context.Context, swarm.Address, *big.Int) {})
	acc.SetPayFunc(func(context.Context, swarm.Address, *big.Int) {})

	// Put the peer into debt, so currentBalance < 0 and the settle branch can
	// be entered on the refusal below.
	const owed = uint64(9000)
	action, err := acc.PrepareCredit(context.Background(), peer, owed, true)
	if err != nil {
		t.Fatalf("expected the first credit to be prepared, got %v", err)
	}
	if err := action.Apply(); err != nil {
		t.Fatalf("applying the credit: %v", err)
	}

	buf.Reset()

	if _, err := acc.PrepareCredit(context.Background(), peer, price, true); !errors.Is(err, accounting.ErrOverdraft) {
		t.Fatalf("expected overdraft error, got %v", err)
	}

	out := buf.String()
	if n := strings.Count(out, overdraftMsg); n != 1 {
		t.Fatalf("expected exactly one refusal line, got %d in: %s", n, out)
	}

	// The balance term must actually be non-zero, or this test is the previous
	// one again.
	settled := logFieldInt(t, out, "settled_balance")
	wantSettled := new(big.Int).Neg(new(big.Int).SetUint64(owed))
	if settled.Cmp(wantSettled) != 0 {
		t.Fatalf("settled_balance is %v, want %v, so the logged balance does not reflect the applied credit", settled, wantSettled)
	}

	if v := logField(t, out, "settle_called"); v != "true" {
		t.Fatalf("settle_called is %q, so the settle branch was not entered and this test does not cover it", v)
	}

	// The identity must still hold with the debt term carrying weight.
	debtTerm := new(big.Int).Neg(settled)
	if debtTerm.Sign() < 0 {
		debtTerm.SetInt64(0)
	}
	want := new(big.Int).Add(debtTerm, logFieldInt(t, out, "reserved_balance"))
	want.Add(want, logFieldInt(t, out, "price"))
	want.Add(want, logFieldInt(t, out, "surplus_balance"))

	if got := logFieldInt(t, out, "expected_debt"); got.Cmp(want) != 0 {
		t.Fatalf("logged terms do not reconcile: expected_debt %v, terms sum to %v, in: %s", got, want, out)
	}
}

// TestAccountingOverdraftLineCarriesEveryField guards the fields no other test
// reads. They are promised to operators in DIFFERENCES.md and operators.md, so
// a rename or a drop should fail here rather than in a bench run.
func TestAccountingOverdraftLineCarriesEveryField(t *testing.T) {
	t.Parallel()

	buf := new(bytes.Buffer)
	acc, peer, price := overdraftingAccounting(t, captureLogger(t.Name(), buf))

	if _, err := acc.PrepareCredit(context.Background(), peer, price, true); !errors.Is(err, accounting.ErrOverdraft) {
		t.Fatalf("expected overdraft error, got %v", err)
	}

	out := buf.String()
	for _, key := range []string{
		"peer_address", "price", "expected_debt", "overdraft_limit",
		"payment_threshold", "refresh_due", "refresh_timestamp_ms",
		"elapsed_seconds", "settled_balance", "surplus_balance",
		"reserved_balance", "shadow_reserved_balance", "settle_called",
	} {
		if v := logField(t, out, key); v == "" {
			t.Errorf("field %q is present but empty in: %s", key, out)
		}
	}
}
