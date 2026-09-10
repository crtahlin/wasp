// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package mock

import (
	"context"

	"github.com/ethersphere/bee/v2/pkg/storageincentives/staking"
)

type legacyStakeServiceMock struct {
	discover   func(ctx context.Context) ([]staking.LegacyStakeStatus, error)
	recover    func(ctx context.Context, id string, mode staking.RecoverMode) (staking.RecoverResult, error)
	recoverAll func(ctx context.Context, mode staking.RecoverMode) ([]staking.RecoverResult, error)
	status     func(ctx context.Context, id string) (staking.RecoverState, error)
}

func (m *legacyStakeServiceMock) Discover(ctx context.Context) ([]staking.LegacyStakeStatus, error) {
	if m.discover != nil {
		return m.discover(ctx)
	}
	return nil, nil
}

func (m *legacyStakeServiceMock) Recover(ctx context.Context, id string, mode staking.RecoverMode) (staking.RecoverResult, error) {
	if m.recover != nil {
		return m.recover(ctx, id, mode)
	}
	return staking.RecoverResult{}, nil
}

func (m *legacyStakeServiceMock) RecoverAll(ctx context.Context, mode staking.RecoverMode) ([]staking.RecoverResult, error) {
	if m.recoverAll != nil {
		return m.recoverAll(ctx, mode)
	}
	return nil, nil
}

func (m *legacyStakeServiceMock) Status(ctx context.Context, id string) (staking.RecoverState, error) {
	if m.status != nil {
		return m.status(ctx, id)
	}
	return staking.RecoverState{}, nil
}

// LegacyStakeOption configures the mock legacy stake service.
type LegacyStakeOption func(*legacyStakeServiceMock)

// NewLegacyStakeService returns a mock LegacyStakeService.
func NewLegacyStakeService(opts ...LegacyStakeOption) staking.LegacyStakeService {
	m := &legacyStakeServiceMock{}
	for _, o := range opts {
		o(m)
	}
	return m
}

func WithDiscover(f func(ctx context.Context) ([]staking.LegacyStakeStatus, error)) LegacyStakeOption {
	return func(m *legacyStakeServiceMock) { m.discover = f }
}

func WithRecover(f func(ctx context.Context, id string, mode staking.RecoverMode) (staking.RecoverResult, error)) LegacyStakeOption {
	return func(m *legacyStakeServiceMock) { m.recover = f }
}

func WithRecoverAll(f func(ctx context.Context, mode staking.RecoverMode) ([]staking.RecoverResult, error)) LegacyStakeOption {
	return func(m *legacyStakeServiceMock) { m.recoverAll = f }
}

func WithStatus(f func(ctx context.Context, id string) (staking.RecoverState, error)) LegacyStakeOption {
	return func(m *legacyStakeServiceMock) { m.status = f }
}
