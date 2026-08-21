package beebench

import (
	"context"
	"fmt"
	"math/rand"
	"testing"

	"github.com/ethersphere/bee/v2/pkg/sharky"
)

// TestBigCorpusConcurrency repeats the sharky-vs-pread sweep against the
// 67 GB corpus, i.e. device-bound rather than page-cache-bound. This answers
// whether sharky's software ceiling actually binds on real hardware.
func TestBigCorpusConcurrency(t *testing.T) {
	dir := bigCorpusDir(t)
	files := openShards(t, dir, false)
	sk := newSharky(t, dir)
	ctx := context.Background()

	const ops = 400_000
	rng := rand.New(rand.NewSource(99))
	ls := make([]loc, ops)
	for i := range ls {
		ls[i] = loc{shard: uint8(rng.Intn(shardCnt)), slot: uint32(rng.Intn(perShardBig))}
	}

	fmt.Printf("\n=== 67 GB corpus (device-bound), no write load ===\n")
	fmt.Printf("%-6s %14s %14s %8s\n", "conc", "sharky ops/s", "pread ops/s", "speedup")
	for _, c := range []int{8, 32, 64, 128, 256} {
		skRate := run(ls, c, func(l loc, buf []byte) {
			_ = sk.Read(ctx, sharky.Location{Shard: l.shard, Slot: l.slot, Length: chunkLen}, buf)
		})
		prRate := run(ls, c, func(l loc, buf []byte) {
			_, _ = files[l.shard].ReadAt(buf[:chunkLen], int64(l.slot)*slotSize)
		})
		fmt.Printf("%-6d %14.0f %14.0f %7.2fx\n", c, skRate, prRate, prRate/skRate)
	}

	fmt.Printf("\n=== 67 GB corpus + concurrent write load (8 writers) ===\n")
	fmt.Printf("%-6s %14s %14s %8s\n", "conc", "sharky ops/s", "pread ops/s", "speedup")
	for _, c := range []int{8, 32, 64, 128} {
		stop := startWriters(t, sk, 8)
		skRate := run(ls, c, func(l loc, buf []byte) {
			_ = sk.Read(ctx, sharky.Location{Shard: l.shard, Slot: l.slot, Length: chunkLen}, buf)
		})
		stop()
		stop = startWriters(t, sk, 8)
		prRate := run(ls, c, func(l loc, buf []byte) {
			_, _ = files[l.shard].ReadAt(buf[:chunkLen], int64(l.slot)*slotSize)
		})
		stop()
		fmt.Printf("%-6d %14.0f %14.0f %7.2fx\n", c, skRate, prRate, prRate/skRate)
	}
}
