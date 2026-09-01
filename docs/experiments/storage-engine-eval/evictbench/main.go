// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Command evictbench measures how the two storage engines behave under mass
// eviction, the operation a bee reserve performs when a large postage batch
// expires or the storage radius rises. Both cases delete millions of index
// entries at once (about three index deletes per chunk in the reserve's
// removeChunk, plus a sharky slot release that is identical on both engines and
// so is not measured here). A burst of deletes writes tombstones that slow every
// read until compaction reclaims them, and goleveldb and Pebble reclaim
// differently. This is the axis the steady-state numbers hide.
//
// Mode "evict" runs the battery for one engine: populate a store, read it
// (a full scan plus random gets, the engine's share of the reserve-sample read),
// delete a fraction in the reserve's delete shape, read again immediately while
// tombstones are thickest, then watch the on-disk size and read time recover as
// background compaction runs. Mode "writefull" measures writes into an
// already-populated store, the shape a radius decrease produces.
//
// It never touches a running node's data: it creates its own store in a scratch
// directory that must be empty and must not look like a bee datadir, and removes
// it on exit unless -keep is set. Point it at a scratch directory on the same
// disk as the node, never the node datadir.
package main

import (
	"context"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/cockroachdb/pebble"
	"github.com/cockroachdb/pebble/bloom"
	"github.com/ethersphere/bee/v2/pkg/storage"
	"github.com/ethersphere/bee/v2/pkg/storage/leveldbstore"
	"github.com/ethersphere/bee/v2/pkg/storage/pebblestore"
	"github.com/syndtr/goleveldb/leveldb/opt"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "evictbench:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		mode       = flag.String("mode", "evict", "evict or writefull")
		engine     = flag.String("engine", "leveldb", "storage engine: leveldb or pebble")
		path       = flag.String("path", "", "scratch directory; must be empty and not a bee datadir")
		chunks     = flag.Int("chunks", 2000000, "chunks to populate")
		entries    = flag.Int("entries-per-chunk", 3, "index entries per chunk (reserve deletes ~3)")
		valueSize  = flag.Int("value-size", 40, "bytes per index entry value")
		evictEvery = flag.Int("evict-every", 4, "evict one chunk in this many (4 = 25%)")
		extra      = flag.Int("extra-chunks", 500000, "writefull mode: chunks to add after populate")
		gets       = flag.Int("gets", 20000, "random gets timed in each read phase")
		settleN    = flag.Int("settle-samples", 8, "post-delete recovery samples")
		settleGap  = flag.Int("settle-gap-s", 20, "seconds between recovery samples")
		cacheBytes = flag.Int("cache-bytes", 64<<20, "block cache capacity")
		bufBytes   = flag.Int("buffer-bytes", 64<<20, "write buffer / memtable size")
		l0Trigger  = flag.Int("l0-trigger", 4, "pebble L0CompactionThreshold")
		openFiles  = flag.Int("open-files", 512, "open files limit")
		seed       = flag.Uint64("seed", 1, "deterministic key seed")
		keep       = flag.Bool("keep", false, "keep the scratch store")
	)
	flag.Parse()

	if *path == "" {
		return fmt.Errorf("-path is required (a scratch directory, never the node datadir)")
	}
	if err := guardScratchPath(*path); err != nil {
		return err
	}

	st, closeStore, err := openStore(*engine, *path, *cacheBytes, *bufBytes, *l0Trigger, *openFiles)
	if err != nil {
		return err
	}
	defer func() {
		_ = closeStore()
		if !*keep {
			_ = os.RemoveAll(*path)
		}
	}()

	ctx := context.Background()
	value := make([]byte, *valueSize)
	for i := range value {
		value[i] = byte(i)
	}

	// Populate.
	t := time.Now()
	if err := populate(ctx, st, 0, *chunks, *entries, *seed, value); err != nil {
		return err
	}
	emit("populate", *engine, *mode, map[string]any{
		"chunks": *chunks, "elapsed_s": round(time.Since(t).Seconds(), 2),
		"size_bytes": dirSize(*path), "l0": level0(st),
	})

	if *mode == "writefull" {
		t = time.Now()
		commits := make([]time.Duration, 0, *extra)
		if err := populateTimed(ctx, st, *chunks, *chunks+*extra, *entries, *seed, value, &commits); err != nil {
			return err
		}
		el := time.Since(t).Seconds()
		p50, p99, mx := pcts(commits)
		emit("writefull", *engine, *mode, map[string]any{
			"extra_chunks": *extra, "elapsed_s": round(el, 2),
			"chunks_per_s":  round(float64(*extra)/el, 0),
			"commit_p50_ms": p50, "commit_p99_ms": p99, "commit_max_ms": mx,
			"size_bytes": dirSize(*path), "l0": level0(st),
		})
		return nil
	}

	// Read baseline.
	readPhase(ctx, st, "baseline", *engine, *chunks, *entries, *evictEvery, *gets, *seed, value)

	// Mass delete: evict one chunk in evictEvery (a single large batch expiry).
	t = time.Now()
	deletes, commits := 0, make([]time.Duration, 0, *chunks / *evictEvery)
	for c := 0; c < *chunks; c++ {
		if c%*evictEvery != 0 {
			continue
		}
		b := st.Batch(ctx)
		for e := 0; e < *entries; e++ {
			if err := b.Delete(&kvItem{ns: nsName(e), id: detID(*seed, uint64(c), uint64(e))}); err != nil {
				return fmt.Errorf("delete: %w", err)
			}
			deletes++
		}
		ct := time.Now()
		if err := b.Commit(); err != nil {
			return fmt.Errorf("delete commit: %w", err)
		}
		commits = append(commits, time.Since(ct))
	}
	el := time.Since(t).Seconds()
	p50, p99, mx := pcts(commits)
	evicted := len(commits)
	emit("delete", *engine, *mode, map[string]any{
		"evicted_chunks": evicted, "index_deletes": deletes, "elapsed_s": round(el, 2),
		"deletes_per_s": round(float64(deletes)/el, 0),
		"commit_p50_ms": p50, "commit_p99_ms": p99, "commit_max_ms": mx,
		"size_bytes": dirSize(*path), "l0": level0(st),
	})

	// Read immediately after delete, tombstones thickest.
	readPhase(ctx, st, "post-delete", *engine, *chunks, *entries, *evictEvery, *gets, *seed, value)

	// Watch recovery as background compaction runs.
	for i := 1; i <= *settleN; i++ {
		time.Sleep(time.Duration(*settleGap) * time.Second)
		readPhaseTagged(ctx, st, fmt.Sprintf("settle_t%ds", i**settleGap), *engine, *chunks,
			*entries, *evictEvery, *gets, *seed, value, dirSize(*path), level0(st))
	}
	return nil
}

func populate(ctx context.Context, st storage.BatchStore, from, to, entries int, seed uint64, value []byte) error {
	return populateTimed(ctx, st, from, to, entries, seed, value, nil)
}

func populateTimed(ctx context.Context, st storage.BatchStore, from, to, entries int, seed uint64, value []byte, commits *[]time.Duration) error {
	for c := from; c < to; c++ {
		b := st.Batch(ctx)
		for e := 0; e < entries; e++ {
			it := &kvItem{ns: nsName(e), id: detID(seed, uint64(c), uint64(e)), val: value}
			if err := b.Put(it); err != nil {
				return fmt.Errorf("put: %w", err)
			}
		}
		var ct time.Time
		if commits != nil {
			ct = time.Now()
		}
		if err := b.Commit(); err != nil {
			return fmt.Errorf("commit: %w", err)
		}
		if commits != nil {
			*commits = append(*commits, time.Since(ct))
		}
	}
	return nil
}

func readPhase(ctx context.Context, st storage.BatchStore, tag, engine string, chunks, entries, evictEvery, gets int, seed uint64, value []byte) {
	readPhaseTagged(ctx, st, tag, engine, chunks, entries, evictEvery, gets, seed, value, -1, level0(st))
}

// readPhaseTagged runs a full scan of every namespace (the engine's share of the
// reserve-sample read) plus a set of random gets on surviving keys, and reports
// the timings. sizeBytes < 0 is omitted.
func readPhaseTagged(ctx context.Context, st storage.BatchStore, tag, engine string, chunks, entries, evictEvery, gets int, seed uint64, value []byte, sizeBytes int64, l0 int) {
	scanStart := time.Now()
	scanned := 0
	for e := 0; e < entries; e++ {
		ns := nsName(e)
		_ = st.Iterate(storage.Query{
			Factory:      func() storage.Item { return &kvItem{ns: ns} },
			ItemProperty: storage.QueryItemID,
		}, func(storage.Result) (bool, error) {
			scanned++
			return false, nil
		})
	}
	scanMS := float64(time.Since(scanStart).Microseconds()) / 1000

	// Random gets on surviving chunks (those not evicted).
	lat := make([]time.Duration, 0, gets)
	hits := 0
	step := uint64(2862933555777941757)
	x := seed
	for i := 0; i < gets; i++ {
		x += step
		c := int(mix(x) % uint64(chunks))
		if evictEvery > 0 && c%evictEvery == 0 {
			c++ // skip evicted chunk
		}
		it := &kvItem{ns: nsName(0), id: detID(seed, uint64(c), 0)}
		gt := time.Now()
		err := st.Get(it)
		lat = append(lat, time.Since(gt))
		if err == nil {
			hits++
		}
	}
	gp50, gp99, gmx := pcts(lat)
	m := map[string]any{
		"scan_ms": round(scanMS, 1), "scanned": scanned,
		"get_p50_ms": gp50, "get_p99_ms": gp99, "get_max_ms": gmx, "get_hits": hits,
		"l0": l0,
	}
	if sizeBytes >= 0 {
		m["size_bytes"] = sizeBytes
	}
	emit("read:"+tag, engine, "evict", m)
}

func guardScratchPath(p string) error {
	for _, marker := range []string{"keys", "localstore", "statestore", "CURRENT", ".storage-engine"} {
		if _, err := os.Stat(filepath.Join(p, marker)); err == nil {
			return fmt.Errorf("refusing to run: %q looks like a bee datadir (found %q)", p, marker)
		}
	}
	entries, err := os.ReadDir(p)
	if err == nil && len(entries) > 0 {
		return fmt.Errorf("refusing to run: scratch path %q is not empty", p)
	}
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("stat scratch path: %w", err)
	}
	return nil
}

func openStore(engine, path string, cacheBytes, bufBytes, l0Trigger, openFiles int) (storage.BatchStore, func() error, error) {
	switch engine {
	case "leveldb":
		o := &opt.Options{
			BlockCacheCapacity:     cacheBytes,
			WriteBuffer:            bufBytes,
			OpenFilesCacheCapacity: openFiles,
			CompactionL0Trigger:    l0Trigger,
		}
		s, _, err := leveldbstore.New(path, o)
		if err != nil {
			return nil, nil, fmt.Errorf("open leveldb: %w", err)
		}
		return s, s.Close, nil
	case "pebble":
		o := &pebble.Options{
			Cache:                 pebble.NewCache(int64(cacheBytes)),
			MemTableSize:          uint64(bufBytes),
			L0CompactionThreshold: l0Trigger,
			MaxOpenFiles:          openFiles,
		}
		o.EnsureDefaults()
		for i := range o.Levels {
			o.Levels[i].FilterPolicy = bloom.FilterPolicy(10)
		}
		s, err := pebblestore.New(path, o)
		if err != nil {
			return nil, nil, fmt.Errorf("open pebble: %w", err)
		}
		return s, s.Close, nil
	default:
		return nil, nil, fmt.Errorf("unknown engine %q (want leveldb or pebble)", engine)
	}
}

func level0(st storage.BatchStore) int {
	if r, ok := st.(interface{ Level0Files() int }); ok {
		return r.Level0Files()
	}
	return -1
}

func dirSize(path string) int64 {
	var total int64
	_ = filepath.WalkDir(path, func(_ string, d fs.DirEntry, _ error) error {
		if d == nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

func pcts(d []time.Duration) (p50, p99, mx float64) {
	if len(d) == 0 {
		return 0, 0, 0
	}
	sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
	ms := func(x time.Duration) float64 { return float64(x.Microseconds()) / 1000 }
	return round(ms(d[len(d)*50/100]), 3), round(ms(d[len(d)*99/100]), 3), round(ms(d[len(d)-1]), 3)
}

func emit(phase, engine, mode string, kv map[string]any) {
	out := fmt.Sprintf("phase=%s engine=%s mode=%s", phase, engine, mode)
	keys := make([]string, 0, len(kv))
	for k := range kv {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out += fmt.Sprintf(" %s=%v", k, kv[k])
	}
	fmt.Fprintln(os.Stdout, out)
}

func round(f float64, dp int) float64 {
	p := 1.0
	for i := 0; i < dp; i++ {
		p *= 10
	}
	return float64(int64(f*p+0.5)) / p
}

func nsName(e int) string { return fmt.Sprintf("idx%d", e) }

// detID is a deterministic 32-hex-char id for (chunk, entry), so keys can be
// regenerated for deletes and gets without holding them all in memory.
func detID(seed, chunk, entry uint64) string {
	a := mix(seed ^ (chunk*0x9E3779B97F4A7C15 + entry))
	b := mix(a ^ 0xD1B54A32D192ED03)
	return fmt.Sprintf("%016x%016x", a, b)
}

// mix is splitmix64, a cheap deterministic bit mixer.
func mix(x uint64) uint64 {
	x += 0x9E3779B97F4A7C15
	x = (x ^ (x >> 30)) * 0xBF58476D1CE4E5B9
	x = (x ^ (x >> 27)) * 0x94D049BB133111EB
	return x ^ (x >> 31)
}

type kvItem struct {
	ns  string
	id  string
	val []byte
}

func (i *kvItem) Namespace() string        { return i.ns }
func (i *kvItem) ID() string               { return i.id }
func (i *kvItem) Marshal() ([]byte, error) { return i.val, nil }
func (i *kvItem) Unmarshal(b []byte) error { i.val = append([]byte(nil), b...); return nil }
func (i *kvItem) String() string           { return i.ns + "/" + i.id }

func (i *kvItem) Clone() storage.Item {
	if i == nil {
		return nil
	}
	return &kvItem{ns: i.ns, id: i.id, val: append([]byte(nil), i.val...)}
}
