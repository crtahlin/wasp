// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package node

import (
	"fmt"
	"math/big"
)

// providerCreditSettings validates the two per-peer provider credit settings
// and returns them, both zero when the feature is off (#327).
//
// The validation is here rather than spread through the caller because every
// one of these refusals is a configuration an operator would otherwise have to
// discover from the absence of an effect.
func providerCreditSettings(o *Options, paymentThreshold *big.Int) (threshold, budget *big.Int, err error) {
	threshold, ok := new(big.Int).SetString(o.ProvidersPaymentThreshold, 10)
	if !ok {
		return nil, nil, fmt.Errorf("invalid providers-payment-threshold: %s", o.ProvidersPaymentThreshold)
	}
	budget, ok = new(big.Int).SetString(o.ProvidersCreditBudget, 10)
	if !ok {
		return nil, nil, fmt.Errorf("invalid providers-credit-budget: %s", o.ProvidersCreditBudget)
	}

	if threshold.Sign() == 0 {
		if budget.Sign() != 0 {
			return nil, nil, fmt.Errorf("providers-credit-budget is set but providers-payment-threshold is not, so nothing would ever be granted")
		}
		return new(big.Int), new(big.Int), nil
	}

	if threshold.Cmp(paymentThreshold) < 0 {
		return nil, nil, fmt.Errorf("providers-payment-threshold %s is below payment-threshold %s, so it would lower what a peer is granted rather than raise it", threshold, paymentThreshold)
	}
	if threshold.Cmp(big.NewInt(maxPaymentThreshold)) > 0 {
		return nil, nil, fmt.Errorf("providers-payment-threshold %s is above the maximum generally accepted value %d", threshold, maxPaymentThreshold)
	}

	// A budget smaller than one grant admits nobody, so the feature would look
	// configured and do nothing.
	oneGrant := new(big.Int).Sub(threshold, paymentThreshold)
	if budget.Cmp(oneGrant) < 0 {
		return nil, nil, fmt.Errorf("providers-credit-budget %s is smaller than one grant, %s, so no peer could ever be admitted", budget, oneGrant)
	}

	return threshold, budget, nil
}
