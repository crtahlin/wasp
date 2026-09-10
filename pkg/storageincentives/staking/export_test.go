// Copyright 2021 The Swarm Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package staking

var Erc20ABI = erc20ABI

// NewLegacyStakeServiceWithFactory exposes the factory-injected constructor so a
// test can supply mock staking clients instead of real on-chain ones.
var NewLegacyStakeServiceWithFactory = newLegacyStakeService

// ContractFactory is the test-visible alias of the client factory type.
type ContractFactory = contractFactory
