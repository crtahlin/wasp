package beebench

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"
)

// Corpus large enough to defeat the 48 GB page cache, so reads reach the device.
const perShardBig = 500_000 // 32 * 500k * 4201 = 67.2 GB

func bigCorpusDir(tb testing.TB) string {
	dir := filepath.Join(os.TempDir(), "beebench-corpus-big")
	marker := filepath.Join(dir, ".done")
	if _, err := os.Stat(marker); err == nil {
		return dir
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		tb.Fatal(err)
	}
	tb.Logf("building BIG corpus: %.1f GB (exceeds RAM so reads miss cache)",
		float64(shardCnt)*perShardBig*slotSize/1e9)
	start := time.Now()
	var wg sync.WaitGroup
	for s := 0; s < shardCnt; s++ {
		wg.Add(1)
		go func(s int) {
			defer wg.Done()
			f, err := os.OpenFile(filepath.Join(dir, fmt.Sprintf("shard_%03d", s)),
				os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
			if err != nil {
				panic(err)
			}
			defer f.Close()
			rng := rand.New(rand.NewSource(int64(s)))
			buf := make([]byte, slotSize*256)
			rng.Read(buf)
			for off := 0; off < perShardBig; off += 256 {
				if _, err := f.WriteAt(buf, int64(off)*slotSize); err != nil {
					panic(err)
				}
				if off%51200 == 0 {
					f.Sync() // keep dirty pages bounded
				}
			}
			f.Sync()
			ff, _ := os.OpenFile(filepath.Join(dir, fmt.Sprintf("free_%03d", s)), os.O_RDWR|os.O_CREATE, 0o644)
			ff.Close()
		}(s)
	}
	wg.Wait()
	os.WriteFile(marker, []byte("ok"), 0o644)
	tb.Logf("BIG corpus built in %s", time.Since(start).Round(time.Second))
	return dir
}

// TestReadOrder compares reading a set of locations in bin order (what the
// sampler does today) against the same set sorted by physical (shard, slot).
func TestReadOrder(t *testing.T) {
	dir := bigCorpusDir(t)
	files := openShards(t, dir, false)
	const conc = 64

	fmt.Printf("\n=== random vs physical-order reads, %.0f GB corpus, conc=%d ===\n",
		float64(shardCnt)*perShardBig*slotSize/1e9, conc)
	fmt.Printf("%-10s %10s %14s %14s %8s\n", "density", "reads", "random ops/s", "sorted ops/s", "speedup")

	for _, density := range []float64{0.01, 0.05, 0.20} {
		n := int(float64(shardCnt*perShardBig) * density)
		rng := rand.New(rand.NewSource(int64(n)))
		ls := make([]loc, n)
		for i := range ls {
			ls[i] = loc{shard: uint8(rng.Intn(shardCnt)), slot: uint32(rng.Intn(perShardBig))}
		}

		read := func(l loc, buf []byte) {
			_, _ = files[l.shard].ReadAt(buf[:chunkLen], int64(l.slot)*slotSize)
		}

		randRate := run(ls, conc, read)

		sorted := make([]loc, len(ls))
		copy(sorted, ls)
		sort.Slice(sorted, func(i, j int) bool {
			if sorted[i].shard != sorted[j].shard {
				return sorted[i].shard < sorted[j].shard
			}
			return sorted[i].slot < sorted[j].slot
		})
		sortRate := run(sorted, conc, read)

		fmt.Printf("%-10.0f%% %10d %14.0f %14.0f %7.2fx\n",
			density*100, n, randRate, sortRate, sortRate/randRate)
	}
}
