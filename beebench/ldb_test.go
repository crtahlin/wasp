package beebench

import (
	"encoding/binary"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ethersphere/bee/v2/pkg/storage"
	"github.com/ethersphere/bee/v2/pkg/storage/leveldbstore"
	"github.com/ethersphere/bee/v2/pkg/swarm"
	"github.com/syndtr/goleveldb/leveldb/filter"
	"github.com/syndtr/goleveldb/leveldb/opt"
)

// RetrievalIndexItem shape from pkg/storer/internal/chunkstore/chunkstore.go
type retrItem struct {
	Address   swarm.Address
	Timestamp uint64
	Loc       [7]byte
	RefCnt    uint32
}

func (r *retrItem) Namespace() string { return "retrievalIdx" }
func (r *retrItem) ID() string        { return r.Address.ByteString() }
func (r *retrItem) String() string    { return r.Namespace() + "/" + r.ID() }
func (r *retrItem) Clone() storage.Item {
	c := *r
	c.Address = r.Address.Clone()
	return &c
}
func (r *retrItem) Marshal() ([]byte, error) {
	b := make([]byte, 8+7+4)
	binary.LittleEndian.PutUint64(b[:8], r.Timestamp)
	copy(b[8:15], r.Loc[:])
	binary.LittleEndian.PutUint32(b[15:], r.RefCnt)
	return b, nil
}
func (r *retrItem) Unmarshal(b []byte) error {
	if len(b) != 19 {
		return fmt.Errorf("bad size %d", len(b))
	}
	r.Timestamp = binary.LittleEndian.Uint64(b[:8])
	copy(r.Loc[:], b[8:15])
	r.RefCnt = binary.LittleEndian.Uint32(b[15:])
	return nil
}

const ldbEntries = 4 << 20 // 4,194,304 == DefaultReserveCapacity

func addrOf(i int) swarm.Address {
	b := make([]byte, 32)
	binary.BigEndian.PutUint64(b[:8], uint64(i)*0x9E3779B97F4A7C15)
	binary.BigEndian.PutUint64(b[8:16], uint64(i)*0xC2B2AE3D27D4EB4F)
	binary.BigEndian.PutUint64(b[16:24], uint64(i))
	return swarm.NewAddress(b)
}

func buildLdb(tb testing.TB) string {
	dir := filepath.Join(os.TempDir(), "beebench-ldb")
	if _, err := os.Stat(filepath.Join(dir, "CURRENT")); err == nil {
		return dir
	}
	os.MkdirAll(dir, 0o755)
	// Upstream added a second return value since this harness was written
	// against 3e157a04: it reports whether the previous shutdown was unclean.
	// Irrelevant for a freshly built benchmark corpus, so it is discarded.
	st, _, err := leveldbstore.New(dir, &opt.Options{
		BlockCacheCapacity: 64 << 20, WriteBuffer: 64 << 20,
		CompactionL0Trigger: 8, Filter: filter.NewBloomFilter(64),
	})
	if err != nil {
		tb.Fatal(err)
	}
	tb.Logf("populating leveldb with %d retrievalIdx entries", ldbEntries)
	start := time.Now()
	b := st.Batch(tb.Context())
	for i := 0; i < ldbEntries; i++ {
		_ = b.Put(&retrItem{Address: addrOf(i), Timestamp: uint64(i), RefCnt: 1})
		if i%50_000 == 0 {
			b.Commit()
			b = st.Batch(tb.Context())
		}
	}
	b.Commit()
	st.Close()
	var sz int64
	filepath.Walk(dir, func(_ string, fi os.FileInfo, _ error) error {
		if fi != nil && !fi.IsDir() {
			sz += fi.Size()
		}
		return nil
	})
	tb.Logf("populated in %s, on-disk size %.0f MB", time.Since(start).Round(time.Second), float64(sz)/1e6)
	return dir
}

func TestLdbBlockCache(t *testing.T) {
	dir := buildLdb(t)
	const ops = 300_000
	const conc = 32

	fmt.Printf("\n=== retrievalIdx Get throughput vs block cache size ===\n")
	fmt.Printf("%-16s %14s %12s\n", "block cache", "gets/s", "vs 32MB")
	var base float64
	for _, mb := range []int{32, 128, 512, 2048} {
		// Upstream added a second return value since this harness was written
		// against 3e157a04: it reports whether the previous shutdown was unclean.
		// Irrelevant for a freshly built benchmark corpus, so it is discarded.
		st, _, err := leveldbstore.New(dir, &opt.Options{
			BlockCacheCapacity: mb << 20, WriteBuffer: 32 << 20,
			CompactionL0Trigger: 8, Filter: filter.NewBloomFilter(64),
			ReadOnly: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		rng := rand.New(rand.NewSource(int64(mb)))
		idx := make([]int, ops)
		for i := range idx {
			idx[i] = rng.Intn(ldbEntries)
		}
		var wg sync.WaitGroup
		per := ops / conc
		start := time.Now()
		for c := 0; c < conc; c++ {
			wg.Add(1)
			go func(c int) {
				defer wg.Done()
				it := &retrItem{}
				for _, i := range idx[c*per : (c+1)*per] {
					it.Address = addrOf(i)
					_ = st.Get(it)
				}
			}(c)
		}
		wg.Wait()
		rate := float64(ops) / time.Since(start).Seconds()
		if base == 0 {
			base = rate
		}
		fmt.Printf("%-13d MB %14.0f %11.2fx\n", mb, rate, rate/base)
		st.Close()
	}
}
