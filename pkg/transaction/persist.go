// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package transaction

import (
	"context"
	"time"
)

type persistentCallsKey struct{}

// WithPersistentCalls marks ctx so that a backend able to route between
// endpoints bounds each endpoint attempt by attempt, goes round its endpoints
// again until ctx ends, and sends a signed transaction through every
// connected endpoint (#783). A backend with one endpoint ignores the mark.
//
// The storage lottery sets it on its commit, reveal and claim calls, which
// must not wait on one endpoint that accepted a request and never answers.
func WithPersistentCalls(ctx context.Context, attempt time.Duration) context.Context {
	return context.WithValue(ctx, persistentCallsKey{}, attempt)
}

// PersistentCalls reports the attempt bound set by WithPersistentCalls, and
// whether ctx carries one.
func PersistentCalls(ctx context.Context) (time.Duration, bool) {
	d, ok := ctx.Value(persistentCallsKey{}).(time.Duration)
	return d, ok && d > 0
}
