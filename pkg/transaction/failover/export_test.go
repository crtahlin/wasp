// Copyright 2026 The Swarm Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package failover

// SetActive forces the active endpoint, so a test can arrange a state that
// would otherwise need a real failure to reach.
func SetActive(b *Backend, i int) { b.active.Store(int32(i)) }

// IsTransportFailure exposes the classifier, which is the piece that decides
// whether an error means "no answer" or "an answer you did not like".
var IsTransportFailure = isTransportFailure
