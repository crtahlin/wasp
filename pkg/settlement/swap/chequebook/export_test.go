// Copyright 2021 The Swarm Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.
package chequebook

import "time"

var (
	LastIssuedChequeKey   = lastIssuedChequeKey
	LastReceivedChequeKey = lastReceivedChequeKey
	CashoutActionKey      = cashoutActionKey
	IssuerKey             = issuerKey

	ChequeLiquidityValidity = chequeLiquidityValidity
)

// SetChequeStoreTimeNow sets the clock a cheque store reads, so that a test can
// move past the period for which liquidity values are reused.
func SetChequeStoreTimeNow(c ChequeStore, now func() time.Time) {
	c.(*chequeStore).timeNow = now
}
