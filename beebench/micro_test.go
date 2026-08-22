package beebench

import (
	"crypto/rand"
	"encoding/binary"
	"runtime"
	"testing"

	"github.com/ethersphere/bee/v2/pkg/bmt"
	"github.com/ethersphere/bee/v2/pkg/keccak"
	"github.com/ethersphere/bee/v2/pkg/swarm"
)

// BatchRadiusItem: verbatim encoding from
// pkg/storer/internal/reserve/items.go (inlined; that package is internal).
type BatchRadiusItem struct {
	Bin       uint8
	BatchID   []byte
	StampHash []byte
	Address   swarm.Address
	BinID     uint64
}

func (b *BatchRadiusItem) Namespace() string { return "batchRadius" }

func (b *BatchRadiusItem) ID() string {
	return string(b.BatchID) + string(b.Bin) + b.Address.ByteString() + string(b.StampHash)
}

const batchRadiusItemSize = 1 + swarm.HashSize + swarm.HashSize + 8 + swarm.HashSize

func copyBytes(src []byte) []byte {
	if src == nil {
		return nil
	}
	return append(make([]byte, 0, len(src)), src...)
}

func (b *BatchRadiusItem) Unmarshal(buf []byte) error {
	i := 0
	b.Bin = buf[i]
	i += 1
	b.BatchID = copyBytes(buf[i : i+swarm.HashSize])
	i += swarm.HashSize
	b.Address = swarm.NewAddress(buf[i : i+swarm.HashSize]).Clone()
	i += swarm.HashSize
	b.BinID = binary.BigEndian.Uint64(buf[i : i+8])
	i += 8
	b.StampHash = copyBytes(buf[i : i+swarm.HashSize])
	return nil
}

// verbatim from pkg/storage/leveldbstore/store.go:24
const separator = "/"

func ldbKey(ns, id string) []byte { return []byte(ns + separator + id) }

// what a fixed-width binary encoding would cost instead
func binKey(dst []byte, ns byte, it *BatchRadiusItem) []byte {
	dst = append(dst, ns, it.Bin)
	dst = append(dst, it.BatchID...)
	dst = append(dst, it.Address.Bytes()...)
	dst = binary.BigEndian.AppendUint64(dst, it.BinID)
	return append(dst, it.StampHash...)
}

func mkItem() *BatchRadiusItem {
	b := make([]byte, 96)
	rand.Read(b)
	return &BatchRadiusItem{
		Bin: 7, BatchID: b[0:32],
		Address: swarm.NewAddress(b[32:64]), StampHash: b[64:96], BinID: 12345,
	}
}

func BenchmarkKey_Current(b *testing.B) {
	it := mkItem()
	b.ReportAllocs()
	for b.Loop() {
		_ = ldbKey(it.Namespace(), it.ID())
	}
}

func BenchmarkKey_Binary(b *testing.B) {
	it := mkItem()
	buf := make([]byte, 0, 128)
	b.ReportAllocs()
	for b.Loop() {
		_ = binKey(buf[:0], 1, it)
	}
}

func TestKeySize(t *testing.T) {
	it := mkItem()
	t.Logf("current key: %d bytes | binary key: %d bytes",
		len(ldbKey(it.Namespace(), it.ID())), len(binKey(nil, 1, it)))
}

func BenchmarkItem_Unmarshal(b *testing.B) {
	buf := make([]byte, batchRadiusItemSize)
	rand.Read(buf)
	dst := &BatchRadiusItem{}
	b.ReportAllocs()
	for b.Loop() {
		_ = dst.Unmarshal(buf)
	}
}

// BenchmarkTransformedAddressCAC_SIMD measures the same work with bee's SIMD
// BMT path opted in.
//
// This is the comparison issue #10 turns on. SIMD dispatch is compiled in only
// for linux/amd64 (pkg/bmt/dispatch_simd.go), requires AVX2 or AVX-512, AND is
// off by default behind the --use-simd-hashing flag. So a measurement taken on
// macOS, or on a VM with a masked CPU, describes a code path many production
// nodes may not run — and optimising it would be aimed at nothing.
func BenchmarkTransformedAddressCAC_SIMD(b *testing.B) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		b.Skipf("SIMD BMT is linux/amd64 only; this is %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	if !keccak.HasSIMD() {
		b.Skip("CPU exposes neither AVX2 nor AVX-512")
	}
	// Save and restore rather than forcing false, matching the pattern in
	// pkg/bmt/bmt_test.go. SIMDOptIn is global mutable state, so hardcoding the
	// restore value would clobber a caller that had legitimately enabled it.
	prev := bmt.SIMDOptIn()
	bmt.SetSIMDOptIn(true)
	b.Cleanup(func() { bmt.SetSIMDOptIn(prev) })
	b.Logf("SIMD enabled: batch width %d, avx512 %v", keccak.BatchWidth(), keccak.HasAVX512())

	anchor := make([]byte, 32)
	rand.Read(anchor)
	data := make([]byte, chunkLen)
	rand.Read(data)

	hasher := bmt.NewPrefixHasher(anchor)
	b.ReportAllocs()
	b.SetBytes(int64(chunkLen))
	for b.Loop() {
		hasher.Reset()
		hasher.SetHeader(data[:bmt.SpanSize])
		if _, err := hasher.Write(data[bmt.SpanSize:]); err != nil {
			b.Fatal(err)
		}
		_ = hasher.Sum(nil)
	}
}

// mirrors transformedAddressCAC() in pkg/storer/sample.go
func BenchmarkTransformedAddressCAC(b *testing.B) {
	anchor := make([]byte, 32)
	rand.Read(anchor)
	data := make([]byte, chunkLen)
	rand.Read(data)

	// Upstream replaced the closure form of bmt.NewHasher with a dedicated
	// constructor since this harness was written against 3e157a04, and added
	// SIMD dispatch (pkg/bmt/dispatch_simd.go). Mirrors pkg/storer/sample.go
	// as it stands at v2.8.1 — a benchmark of the old shape measures nothing
	// the sampler actually does.
	hasher := bmt.NewPrefixHasher(anchor)
	b.ReportAllocs()
	b.SetBytes(int64(chunkLen))
	for b.Loop() {
		hasher.Reset()
		hasher.SetHeader(data[:bmt.SpanSize])
		if _, err := hasher.Write(data[bmt.SpanSize:]); err != nil {
			b.Fatal(err)
		}
		_ = hasher.Sum(nil)
	}
}
