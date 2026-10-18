// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build cgo && !gofuzz

package crypto

import (
	"bytes"
	"crypto/rand"
	"math/big"
	"os"
	"strconv"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
	btcecdsa "github.com/btcsuite/btcd/btcec/v2/ecdsa"
	"github.com/ethereum/go-ethereum/crypto/secp256k1"
)

// These tests compare the cgo recoverHash (libsecp256k1 with a decred
// fallback) with recoverHashDecred, the code every wasp build used before.
// The requirement is identical results: the same key on the same curve when
// both accept, the same error when both reject. The two outcomes that can
// break it are "both accept with different keys" and "libsecp256k1 accepts
// where decred rejects"; both show up here as a mismatch.

var (
	curveN = btcec.S256().Params().N
	curveP = btcec.S256().Params().P
)

// equivalenceCount is the number of random signatures per random test. CI
// runs 10,000; WASP_RECOVER_EQUIV_N=1000000 is the full run the spec asks
// for before merge and in the scheduled job.
func equivalenceCount(t *testing.T) int {
	t.Helper()
	if s := os.Getenv("WASP_RECOVER_EQUIV_N"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n <= 0 {
			t.Fatalf("WASP_RECOVER_EQUIV_N=%q is not a positive number", s)
		}
		return n
	}
	if testing.Short() {
		return 1000
	}
	return 10000
}

type outcome int

const (
	bothAccept outcome = iota
	bothReject
)

// compare runs both implementations on (sig, hash) and fails the test on any
// difference. It also checks that neither touched its input slices.
func compare(t *testing.T, sig, hash []byte) outcome {
	t.Helper()
	sigCopy, hashCopy := bytes.Clone(sig), bytes.Clone(hash)

	got, gotErr := recoverHash(sig, hash)
	if !bytes.Equal(sig, sigCopy) || !bytes.Equal(hash, hashCopy) {
		t.Fatalf("recoverHash changed its input: sig %x -> %x", sigCopy, sig)
	}
	want, wantErr := recoverHashDecred(sig, hash)
	if !bytes.Equal(sig, sigCopy) || !bytes.Equal(hash, hashCopy) {
		t.Fatalf("recoverHashDecred changed its input: sig %x -> %x", sigCopy, sig)
	}

	switch {
	case gotErr == nil && wantErr == nil:
		if got.Curve != btcec.S256() || want.Curve != btcec.S256() {
			t.Fatalf("curve differs from btcec.S256(): got %T, want %T (sig %x hash %x)", got.Curve, want.Curve, sig, hash)
		}
		//nolint:staticcheck // SA1019: comparing the coordinates is the point of the test; secp256k1 keys have no crypto/ecdh form
		if got.X.Cmp(want.X) != 0 || got.Y.Cmp(want.Y) != 0 {
			t.Fatalf("both accept, different keys: sig %x hash %x", sig, hash)
		}
		if !got.Equal(want) {
			t.Fatalf("ecdsa.PublicKey.Equal is false: sig %x hash %x", sig, hash)
		}
		return bothAccept
	case gotErr != nil && wantErr != nil:
		if gotErr.Error() != wantErr.Error() {
			t.Fatalf("both reject, different errors: %q vs %q (sig %x hash %x)", gotErr, wantErr, sig, hash)
		}
		return bothReject
	case gotErr == nil:
		t.Fatalf("recoverHash accepts where decred rejects (%v): sig %x hash %x", wantErr, sig, hash)
	default:
		t.Fatalf("recoverHash rejects (%v) where decred accepts: sig %x hash %x", gotErr, sig, hash)
	}
	return bothReject
}

// libsecpAccepts reports whether libsecp256k1 alone (no fallback) accepts the
// signature, to count how often the fallback decides a result.
func libsecpAccepts(sig, hash []byte) bool {
	v := sig[64]
	if v < 27 || v > 34 || len(hash) != 32 {
		return false
	}
	buf := bytes.Clone(sig)
	buf[64] = (v - 27) & 3
	_, err := secp256k1.RecoverPubkey(hash, buf)
	return err == nil
}

func randomBytes(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

// signCompact signs hash and returns the Ethereum-format signature
// (r || s || v) with decred's header as v: 27/28, or 31/32 when compressed.
func signCompact(t *testing.T, hash []byte, compressed bool) []byte {
	t.Helper()
	key, err := btcec.NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	c, err := btcecdsa.SignCompact(key, hash, compressed)
	if err != nil {
		t.Fatal(err)
	}
	sig := make([]byte, 65)
	copy(sig, c[1:])
	sig[64] = c[0]
	return sig
}

func scalar32(x *big.Int) []byte {
	b := make([]byte, 32)
	x.FillBytes(b)
	return b
}

func sigFrom(r, s *big.Int, v byte) []byte {
	sig := make([]byte, 65)
	copy(sig, scalar32(r))
	copy(sig[32:], scalar32(s))
	sig[64] = v
	return sig
}

// Not parallel: it reads recoverFallbacks, which other tests increment.
func TestRecoverEquivalenceRandom(t *testing.T) {
	n := equivalenceCount(t)
	var accepted, fallback int
	for i := 0; i < n; i++ {
		before := recoverFallbacks.Load()
		hash := randomBytes(t, 32)
		sig := signCompact(t, hash, i%2 == 1) // headers 27/28 and 31/32
		if compare(t, sig, hash) != bothAccept {
			t.Fatalf("a valid signature is rejected: %x", sig)
		}
		accepted++
		if !libsecpAccepts(sig, hash) || recoverFallbacks.Load() != before {
			fallback++
		}
		// The same r and s with the overflow bit set (headers 29/30, 33/34):
		// almost always a valid recovery of a different point.
		ov := bytes.Clone(sig)
		ov[64] += 2
		compare(t, ov, hash)
	}
	t.Logf("%d valid signatures (half with the compressed flag) and %d overflow-bit variants: 0 differences; %d valid ones decided by the decred fallback", accepted, n, fallback)
	if fallback != 0 {
		t.Fatalf("libsecp256k1 rejected %d valid signatures that decred accepts", fallback)
	}
}

// The signatures the node itself produces go through Signer.Sign.
func TestRecoverEquivalenceSigner(t *testing.T) {
	t.Parallel()
	n := equivalenceCount(t) / 10
	for i := 0; i < n; i++ {
		key, err := GenerateSecp256k1Key()
		if err != nil {
			t.Fatal(err)
		}
		data := randomBytes(t, 1+i%200)
		sig, err := NewDefaultSigner(key).Sign(data)
		if err != nil {
			t.Fatal(err)
		}
		hash, err := hashWithEthereumPrefix(data)
		if err != nil {
			t.Fatal(err)
		}
		if compare(t, sig, hash) != bothAccept {
			t.Fatalf("Signer.Sign signature rejected: %x", sig)
		}
		pub, err := Recover(sig, data)
		if err != nil || !pub.Equal(&key.PublicKey) {
			t.Fatalf("Recover does not return the signer's key (err %v)", err)
		}
	}
}

func TestRecoverEquivalenceEveryHeader(t *testing.T) {
	t.Parallel()
	var accept, reject int
	for i := 0; i < 200; i++ {
		hash := randomBytes(t, 32)
		sig := signCompact(t, hash, false)
		for v := 0; v < 256; v++ {
			s := bytes.Clone(sig)
			s[64] = byte(v)
			if compare(t, s, hash) == bothAccept {
				accept++
				if v < 27 || v > 34 {
					t.Fatalf("header %d accepted", v)
				}
			} else {
				reject++
			}
		}
	}
	t.Logf("200 signatures x 256 headers: %d both accept, %d both reject, 0 differences", accept, reject)
}

func TestRecoverEquivalenceEdgeVectors(t *testing.T) {
	t.Parallel()
	one := big.NewInt(1)
	nMinus1 := new(big.Int).Sub(curveN, one)
	nPlus1 := new(big.Int).Add(curveN, one)
	max256 := new(big.Int).Sub(new(big.Int).Lsh(one, 256), one)
	pMinusN := new(big.Int).Sub(curveP, curveN)
	values := []*big.Int{big.NewInt(0), one, nMinus1, curveN, nPlus1, max256, pMinusN, new(big.Int).Sub(pMinusN, one)}

	hash := randomBytes(t, 32)
	valid := signCompact(t, hash, false)
	r := new(big.Int).SetBytes(valid[:32])
	s := new(big.Int).SetBytes(valid[32:64])
	v := valid[64]

	count := 0
	// r and s each over the edge values, with every valid header.
	for _, x := range values {
		for h := byte(27); h <= 34; h++ {
			compare(t, sigFrom(x, s, h), hash)
			compare(t, sigFrom(r, x, h), hash)
			count += 2
		}
	}
	// High-S: s -> N-s with the recovery id's parity bit flipped gives the
	// same key in both implementations (neither rejects high-S).
	hs := sigFrom(r, new(big.Int).Sub(curveN, s), 27+((v-27)^1))
	if compare(t, hs, hash) != bothAccept {
		t.Fatal("high-S signature rejected")
	}
	lo, _ := recoverHashDecred(valid, hash)
	hi, _ := recoverHash(hs, hash)
	if !lo.Equal(hi) {
		t.Fatal("high-S signature recovers a different key")
	}
	count++
	// x not on the curve: the smallest r for which r^3+7 has no square root.
	for x := int64(1); x < 64; x++ {
		rx := big.NewInt(x)
		y2 := new(big.Int).Exp(rx, big.NewInt(3), curveP)
		y2.Add(y2, big.NewInt(7)).Mod(y2, curveP)
		if new(big.Int).ModSqrt(y2, curveP) == nil {
			if compare(t, sigFrom(rx, s, 27), hash) != bothReject {
				t.Fatalf("r=%d (not on the curve) accepted", x)
			}
			count++
			break
		}
	}
	// Hash edge values with a valid r and s.
	for _, hv := range []*big.Int{big.NewInt(0), curveN, max256, nMinus1} {
		for h := byte(27); h <= 30; h++ {
			compare(t, sigFrom(r, s, h), scalar32(hv))
			count++
		}
	}
	// A recovered point at infinity: R = kG, r = R.x, any s, e = s*k mod N
	// gives Q = r^-1 (sR - eG) = 0. Both must reject.
	for i := 0; i < 8; i++ {
		k := new(big.Int).SetBytes(randomBytes(t, 32))
		k.Mod(k, curveN)
		if k.Sign() == 0 {
			continue
		}
		rx, ry := btcec.S256().ScalarBaseMult(scalar32(k))
		if rx.Cmp(curveN) >= 0 {
			continue
		}
		si := new(big.Int).SetBytes(randomBytes(t, 32))
		si.Mod(si, curveN)
		if si.Sign() == 0 {
			continue
		}
		e := new(big.Int).Mul(si, k)
		e.Mod(e, curveN)
		recid := byte(ry.Bit(0))
		if compare(t, sigFrom(rx, si, 27+recid), scalar32(e)) != bothReject {
			t.Fatal("a point at infinity is accepted")
		}
		count++
	}
	t.Logf("%d edge vectors: 0 differences", count)
}

// FuzzRecoverEquivalence compares both implementations on arbitrary
// signatures and hashes. The fuzzer instruments Go code only, so it cannot
// steer inside libsecp256k1; the structured edge vectors above carry most of
// the weight. Run with CGO_ENABLED=1, or it does not build.
func FuzzRecoverEquivalence(f *testing.F) {
	h := make([]byte, 32)
	if _, err := rand.Read(h); err != nil {
		f.Fatal(err)
	}
	key, err := btcec.NewPrivateKey()
	if err != nil {
		f.Fatal(err)
	}
	for _, comp := range []bool{false, true} {
		c, err := btcecdsa.SignCompact(key, h, comp)
		if err != nil {
			f.Fatal(err)
		}
		sig := append(bytes.Clone(c[1:]), c[0])
		f.Add(sig, h)
		ov := bytes.Clone(sig)
		ov[64] += 2
		f.Add(ov, h)
	}
	f.Add(make([]byte, 65), make([]byte, 32))
	f.Add(sigFrom(curveN, big.NewInt(1), 27), h)
	f.Add(sigFrom(big.NewInt(1), curveN, 28), h)
	f.Add(sigFrom(new(big.Int).Sub(curveP, curveN), big.NewInt(5), 29), h)
	f.Add(bytes.Repeat([]byte{0xff}, 65), bytes.Repeat([]byte{0xff}, 32))

	f.Fuzz(func(t *testing.T, sig, hash []byte) {
		if len(sig) != 65 || len(hash) != 32 {
			return
		}
		compare(t, sig, hash)
	})
}
