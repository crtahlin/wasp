package beebench

import (
	"context"
	"fmt"
	"testing"

	"github.com/ethersphere/bee/v2/pkg/sharky"
)

var concLevels = []int{1, 4, 8, 16, 32, 64, 128, 256}

// TestReadConcurrencyWarm isolates *software* overhead: every location is
// paged in before measuring, so both paths hit the page cache and the only
// difference is sharky's actor-per-shard serialisation vs direct pread.
func TestReadConcurrencyWarm(t *testing.T) {
	dir := corpusDir(t)
	const ops = 200_000
	ls := randomLocs(ops, 42)
	ctx := context.Background()

	files := openShards(t, dir, false)
	sk := newSharky(t, dir)

	// Pre-warm: touch every location twice so both paths start fully resident.
	for i := 0; i < 2; i++ {
		run(ls, 64, func(l loc, buf []byte) {
			_, _ = files[l.shard].ReadAt(buf[:chunkLen], int64(l.slot)*slotSize)
		})
	}

	fmt.Printf("\n=== warm / page-cache resident (software overhead only) ===\n")
	fmt.Printf("%-6s %14s %14s %8s\n", "conc", "sharky ops/s", "pread ops/s", "speedup")
	for _, c := range concLevels {
		skRate := run(ls, c, func(l loc, buf []byte) {
			_ = sk.Read(ctx, sharky.Location{Shard: l.shard, Slot: l.slot, Length: chunkLen}, buf)
		})
		prRate := run(ls, c, func(l loc, buf []byte) {
			_, _ = files[l.shard].ReadAt(buf[:chunkLen], int64(l.slot)*slotSize)
		})
		fmt.Printf("%-6d %14.0f %14.0f %7.2fx\n", c, skRate, prRate, prRate/skRate)
	}
}

// TestReadConcurrencyContended measures the case that actually matters in a
// live node: reads competing with a concurrent write stream on the same
// shards. sharky funnels both through one goroutine per shard, so reads
// queue behind writes; pread does not.
func TestReadConcurrencyContended(t *testing.T) {
	dir := corpusDir(t)
	const ops = 100_000
	ls := randomLocs(ops, 7)
	ctx := context.Background()

	files := openShards(t, dir, false)
	sk := newSharky(t, dir)

	run(ls, 64, func(l loc, buf []byte) {
		_, _ = files[l.shard].ReadAt(buf[:chunkLen], int64(l.slot)*slotSize)
	})

	fmt.Printf("\n=== warm + concurrent write load (8 writers) ===\n")
	fmt.Printf("%-6s %14s %14s %8s\n", "conc", "sharky ops/s", "pread ops/s", "speedup")
	for _, c := range concLevels {
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
