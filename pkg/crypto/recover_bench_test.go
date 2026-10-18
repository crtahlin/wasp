// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package crypto_test

import (
	"testing"

	"github.com/ethersphere/bee/v2/pkg/crypto"
)

// BenchmarkRecover measures one public-key recovery as pullsync does it for
// every stamp and single-owner chunk. Run it once with CGO_ENABLED=1
// (libsecp256k1) and once with CGO_ENABLED=0 (decred), each in its own
// process, and compare across processes, never two sub-benchmarks of one.
func BenchmarkRecover(b *testing.B) {
	key, err := crypto.GenerateSecp256k1Key()
	if err != nil {
		b.Fatal(err)
	}
	data := make([]byte, 32)
	sig, err := crypto.NewDefaultSigner(key).Sign(data)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := crypto.Recover(sig, data); err != nil {
			b.Fatal(err)
		}
	}
}
