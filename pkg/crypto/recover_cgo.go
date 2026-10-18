// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build cgo && !gofuzz

package crypto

import (
	"crypto/ecdsa"
	"math/big"
	"sync/atomic"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/ethereum/go-ethereum/crypto/secp256k1"
)

// recoverFallbacks counts the calls that the decred path answered. Tests use
// it to check that valid signatures never need the fallback.
var recoverFallbacks atomic.Uint64

// recoverHash recovers the signer's public key with libsecp256k1 (the C
// library go-ethereum vendors), which is several times faster than decred's
// pure-Go code. The result is the same as recoverHashDecred for every input:
//
//   - v (the last signature byte) is decred's compact header. decred accepts
//     27 to 34: bit 2 only marks a compressed key and does not change the
//     point, and the recovery id is (v-27)&3. Any other v goes to decred.
//   - libsecp256k1 gets a fresh buffer; the caller's slice, often a stored
//     stamp or SOC signature, is never written.
//   - The key is returned on decred's curve (btcec.S256()), the object decred
//     returns, because ecdsa.PublicKey.Equal compares the curve.
//   - Any libsecp256k1 rejection returns decred's result, so the set of
//     rejected signatures and the error values are those of decred.
func recoverHash(signature, hash []byte) (*ecdsa.PublicKey, error) {
	v := signature[64]
	if v < 27 || v > 34 || len(hash) != 32 {
		recoverFallbacks.Add(1)
		return recoverHashDecred(signature, hash)
	}
	sig := make([]byte, 65)
	copy(sig, signature[:64])
	sig[64] = (v - 27) & 3

	pub, err := secp256k1.RecoverPubkey(hash, sig)
	if err != nil || len(pub) != 65 || pub[0] != 0x04 {
		recoverFallbacks.Add(1)
		return recoverHashDecred(signature, hash)
	}
	return &ecdsa.PublicKey{
		Curve: btcec.S256(),
		X:     new(big.Int).SetBytes(pub[1:33]),
		Y:     new(big.Int).SetBytes(pub[33:65]),
	}, nil
}
