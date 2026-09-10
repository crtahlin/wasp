// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package mock

import (
	"context"

	"github.com/ethersphere/bee/v2/pkg/storageincentives/staking"
)

type legacyStakeServiceMock struct {
	discover func(ctx context.Context) ([]staking.LegacyStakeStatus, error)
}

func (m *legacyStakeServiceMock) Discover(ctx context.Context) ([]staking.LegacyStakeStatus, error) {
	if m.discover != nil {
		return m.discover(ctx)
	}
	return nil, nil
}

// NewLegacyStakeService returns a mock LegacyStakeService whose Discover returns
// whatever the given function returns.
func NewLegacyStakeService(discover func(ctx context.Context) ([]staking.LegacyStakeStatus, error)) staking.LegacyStakeService {
	return &legacyStakeServiceMock{discover: discover}
}
