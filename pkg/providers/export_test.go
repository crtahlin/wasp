// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package providers

import (
	"context"
	"time"
)

// SetNow replaces the service's clock.
func (s *Service) SetNow(f func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = f
}

// RunOnce runs one pass of the announcement loop.
func (s *Service) RunOnce(ctx context.Context) {
	s.runOnce(ctx)
}

var WindowStart = windowStart
