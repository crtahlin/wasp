// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package crypto

import (
	"crypto/ecdsa"

	btcecdsa "github.com/btcsuite/btcd/btcec/v2/ecdsa"
)

// recoverHashDecred recovers the public key that produced the 65-byte
// Ethereum-format signature (r || s || v) over hash, using decred's
// RecoverCompact. v is passed to decred unchanged as its compact-signature
// header, which accepts 27 to 34. This is the reference implementation: the
// only path in builds without cgo, the fallback of the cgo path on every
// rejection, and the oracle the equivalence tests compare against.
// signature must be 65 bytes long; the callers check it.
func recoverHashDecred(signature, hash []byte) (*ecdsa.PublicKey, error) {
	// Convert to btcec input format with 'recovery id' v at the beginning.
	btcsig := make([]byte, 65)
	btcsig[0] = signature[64]
	copy(btcsig[1:], signature)

	pbk, _, err := btcecdsa.RecoverCompact(btcsig, hash)
	if err != nil {
		return nil, err
	}
	return pbk.ToECDSA(), err
}
