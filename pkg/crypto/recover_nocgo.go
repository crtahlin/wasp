// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build !cgo || gofuzz

package crypto

import "crypto/ecdsa"

// recoverHash recovers the signer's public key. Without cgo it is decred's
// RecoverCompact, as before.
func recoverHash(signature, hash []byte) (*ecdsa.PublicKey, error) {
	return recoverHashDecred(signature, hash)
}
