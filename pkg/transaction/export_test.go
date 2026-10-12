// Copyright 2021 The Swarm Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package transaction

import "time"

var StoredTransactionKey = storedTransactionKey

var Bumped = bumped

// SetAmbiguousRebroadcastInterval shortens the re-broadcast interval for a
// test and returns a function that restores it.
func SetAmbiguousRebroadcastInterval(d time.Duration) func() {
	old := ambiguousRebroadcastOverride.Swap(int64(d))
	return func() { ambiguousRebroadcastOverride.Store(old) }
}
