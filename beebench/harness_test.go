package beebench

import (
	"fmt"
	"io/fs"
	"math/rand"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ethersphere/bee/v2/pkg/sharky"
)

// Mirrors sharky's own test helper.
type dirFS struct{ basedir string }

func (d *dirFS) Open(path string) (fs.File, error) {
	return os.OpenFile(filepath.Join(d.basedir, path), os.O_RDWR|os.O_CREATE, 0o644)
}

const (
	shardCnt    = 32                     // pkg/storer/storer.go:204
	slotSize    = 4201                   // swarm.SocMaxChunkSize
	chunkLen    = 4104                   // swarm.ChunkWithSpanSize (a CAC)
	totalChunks = 1 << 20                // 1,048,576 -> ~4.2 GB, quarter of a full reserve
	perShard    = totalChunks / shardCnt // 32768 slots per shard
)

// corpusDir builds (once) a sharky-layout corpus by writing the shard files
// directly. Reads only do ReadAt(buf, slot*slotSize) with no slot-validity
// check, so a hand-populated store is faithful for read benchmarking and far
// faster to construct than going through sharky.Write.
func corpusDir(tb testing.TB) string {
	dir := filepath.Join(os.TempDir(), "beebench-corpus")
	marker := filepath.Join(dir, ".done")
	if _, err := os.Stat(marker); err == nil {
		return dir
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		tb.Fatal(err)
	}

	tb.Logf("building corpus: %d chunks x %d B across %d shards (~%.1f GB)",
		totalChunks, slotSize, shardCnt, float64(totalChunks)*slotSize/1e9)
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
			// write in 64-slot batches
			buf := make([]byte, slotSize*64)
			for off := 0; off < perShard; off += 64 {
				rng.Read(buf)
				if _, err := f.WriteAt(buf, int64(off)*slotSize); err != nil {
					panic(err)
				}
			}
			// free_NNN so sharky.New is happy
			ff, err := os.OpenFile(filepath.Join(dir, fmt.Sprintf("free_%03d", s)),
				os.O_RDWR|os.O_CREATE, 0o644)
			if err != nil {
				panic(err)
			}
			ff.Close()
		}(s)
	}
	wg.Wait()

	if err := os.WriteFile(marker, []byte("ok"), 0o644); err != nil {
		tb.Fatal(err)
	}
	tb.Logf("corpus built in %s", time.Since(start).Round(time.Millisecond))
	return dir
}

// openShards opens the raw shard files for the direct-pread comparison.
// nocache sets F_NOCACHE (darwin's O_DIRECT-ish) to bypass the page cache.
func openShards(tb testing.TB, dir string, nocache bool) []*os.File {
	fs := make([]*os.File, shardCnt)
	for s := range fs {
		f, err := os.OpenFile(filepath.Join(dir, fmt.Sprintf("shard_%03d", s)), os.O_RDONLY, 0o644)
		if err != nil {
			tb.Fatal(err)
		}
		if nocache {
			if err := dropCache(f); err != nil {
				tb.Fatalf("drop cache: %v", err)
			}
		}
		fs[s] = f
	}
	tb.Cleanup(func() {
		for _, f := range fs {
			f.Close()
		}
	})
	return fs
}

type loc struct {
	shard uint8
	slot  uint32
}

func randomLocs(n int, seed int64) []loc {
	rng := rand.New(rand.NewSource(seed))
	ls := make([]loc, n)
	for i := range ls {
		ls[i] = loc{
			shard: uint8(rng.Intn(shardCnt)),
			slot:  uint32(rng.Intn(perShard)),
		}
	}
	return ls
}

// run executes fn over ops locations with the given goroutine count and
// returns achieved ops/sec.
func run(ls []loc, conc int, fn func(l loc, buf []byte)) float64 {
	var wg sync.WaitGroup
	per := len(ls) / conc
	start := time.Now()
	for c := 0; c < conc; c++ {
		wg.Add(1)
		go func(c int) {
			defer wg.Done()
			buf := make([]byte, slotSize)
			lo, hi := c*per, (c+1)*per
			if c == conc-1 {
				hi = len(ls)
			}
			for _, l := range ls[lo:hi] {
				fn(l, buf)
			}
		}(c)
	}
	wg.Wait()
	el := time.Since(start)
	return float64(len(ls)) / el.Seconds()
}

func newSharky(tb testing.TB, dir string) *sharky.Store {
	s, err := sharky.New(&dirFS{basedir: dir}, shardCnt, slotSize)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { s.Close() })
	return s
}
