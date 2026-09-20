// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package node

import (
	"github.com/ethersphere/bee/v2/pkg/accounting"
	"github.com/ethersphere/bee/v2/pkg/log"
)

// accrualSetter is the part of accounting a refresh-allowance-accrual setting
// applies to. An interface so that the wiring can be tested: without one,
// deleting the call below leaves the setting parsed, validated, logged and
// inert, which is exactly the failure ParseAccrualMode exists to prevent.
type accrualSetter interface {
	SetAccrualMode(accounting.AccrualMode)
}

// applyAccrualMode parses the refresh-allowance-accrual setting and applies it.
//
// An unknown value is an error rather than a silent fall back to the default.
// See issue #359.
func applyAccrualMode(acc accrualSetter, setting string, logger log.Logger) error {
	mode, err := accounting.ParseAccrualMode(setting)
	if err != nil {
		return err
	}
	acc.SetAccrualMode(mode)
	if mode == accounting.AccrualContinuous {
		logger.Info("refresh allowance accrues continuously inside the first second",
			"setting", setting)
	}
	return nil
}
