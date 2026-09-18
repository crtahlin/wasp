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
func overdraftingAccounting(t *testing.T, logger log.Logger) (accounting.Interface, swarm.Address, uint64) {
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
func TestAccountingOverdraftLoggingIsBehaviourNeutral(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		overDraw bool
	}{
		{"refused", true},
		{"allowed", false},
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

			actionLoud, errLoud := loud.PrepareCredit(context.Background(), loudPeer, price, true)
			actionQuiet, errQuiet := quiet.PrepareCredit(context.Background(), quietPeer, price, true)

			switch {
			case errLoud == nil && errQuiet == nil:
			case errLoud != nil && errQuiet != nil && errors.Is(errLoud, accounting.ErrOverdraft) == errors.Is(errQuiet, accounting.ErrOverdraft):
			default:
				t.Fatalf("errors differ with logging on and off: %v against %v", errLoud, errQuiet)
			}

			if (actionLoud == nil) != (actionQuiet == nil) {
				t.Fatalf("returned action differs with logging on and off: %v against %v", actionLoud, actionQuiet)
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

	// settle_triggered must be present and parse as a bool, since it is the
	// field the #343 question turns on and is derivable from nothing else.
	if v := logField(t, out, "settle_triggered"); v != "true" && v != "false" {
		t.Fatalf("settle_triggered is not a bool: %q", v)
	}
}
