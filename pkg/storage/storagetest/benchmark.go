// Copyright 2022 The Swarm Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package storagetest

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"math/rand"
	"runtime"
	"testing"
	"time"

	postagetesting "github.com/ethersphere/bee/v2/pkg/postage/testing"
	storage "github.com/ethersphere/bee/v2/pkg/storage"
	"github.com/ethersphere/bee/v2/pkg/swarm"
)

var (
	valueSize        = flag.Int("value_size", 100, "Size of each value")
	compressionRatio = flag.Float64("compression_ratio", 0.5, "")
	maxConcurrency   = flag.Int("max_concurrency", 2048, "Max concurrency in concurrent benchmark")
	batchSize        = flag.Int("batch_size", 1000, "Max number of records that would trigger commit")

	// datasetSize is how many entries a benchmark's setup phase writes, and
	// how many distinct entries a measured loop works through before it moves
	// on to the next block.
	//
	// It used to be b.N. That no longer means anything: b.Loop owns the
	// iteration count and reports it only after the loop has finished, so b.N
	// reads as 1 everywhere setup runs. Sizing from b.N therefore did not
	// scale the dataset with the run, it collapsed it to a single entry.
	//
	// A fixed size is a deliberate change of what these benchmarks measure:
	// the dataset no longer grows with -benchtime. Raise this to measure
	// against a store too large to sit in the page cache. At the default,
	// 100000 entries of 100 bytes is roughly 12MB.
	datasetSize = flag.Int("dataset_size", 100000, "Number of entries written by a benchmark's setup phase")
)

var keyLen = 16

const (
	hitKeyFormat     = "1%015d"
	missingKeyFormat = "0%015d"
)

func randomBytes(r *rand.Rand, n int) []byte {
	b := make([]byte, n)
	for i := range n {
		b[i] = ' ' + byte(r.Intn('~'-' '+1))
	}
	return b
}

func compressibleBytes(r *rand.Rand, ratio float64, valueSize int) []byte {
	m := maxInt(int(float64(valueSize)*ratio), 1)
	p := randomBytes(r, m)
	b := make([]byte, 0, valueSize+valueSize%m)
	for len(b) < valueSize {
		b = append(b, p...)
	}
	return b[:valueSize]
}

type randomValueGenerator struct {
	b []byte
	k int
}

func (g *randomValueGenerator) Value(i int) []byte {
	i = (i * g.k) % len(g.b)
	return g.b[i : i+g.k]
}

func makeRandomValueGenerator(r *rand.Rand, ratio float64, valueSize int) randomValueGenerator {
	b := compressibleBytes(r, ratio, valueSize)
	maxVal := maxInt(valueSize, 1024*1024)
	for len(b) < maxVal {
		b = append(b, compressibleBytes(r, ratio, valueSize)...)
	}
	return randomValueGenerator{b: b, k: valueSize}
}

type entryGenerator interface {
	keyGenerator
	Value(i int) []byte
}

type pairedEntryGenerator struct {
	keyGenerator
	randomValueGenerator
}

type startAtEntryGenerator struct {
	entryGenerator
	start int
}

var _ entryGenerator = (*startAtEntryGenerator)(nil)

func (g *startAtEntryGenerator) NKey() int {
	return g.entryGenerator.NKey() - g.start
}

func (g *startAtEntryGenerator) Key(i int) []byte {
	return g.entryGenerator.Key(g.start + i)
}

func newStartAtEntryGenerator(start int, g entryGenerator) entryGenerator {
	return &startAtEntryGenerator{start: start, entryGenerator: g}
}

func newSequentialKeys(size int, start int, keyFormat string) [][]byte {
	keys := make([][]byte, size)
	buffer := make([]byte, size*keyLen)
	for i := range size {
		begin, end := i*keyLen, (i+1)*keyLen
		key := buffer[begin:begin:end]
		_, _ = fmt.Fprintf(bytes.NewBuffer(key), keyFormat, start+i)
		keys[i] = buffer[begin:end:end]
	}
	return keys
}

func newRandomKeys(n int, format string) [][]byte {
	r := rand.New(rand.NewSource(time.Now().Unix()))
	keys := make([][]byte, n)
	buffer := make([]byte, n*keyLen)
	for i := range n {
		begin, end := i*keyLen, (i+1)*keyLen
		key := buffer[begin:begin:end]
		_, _ = fmt.Fprintf(bytes.NewBuffer(key), format, r.Intn(n))
		keys[i] = buffer[begin:end:end]
	}
	return keys
}

func newFullRandomKeys(size int, start int, format string) [][]byte {
	keys := newSequentialKeys(size, start, format)
	r := rand.New(rand.NewSource(time.Now().Unix()))
	for i := range size {
		j := r.Intn(size)
		keys[i], keys[j] = keys[j], keys[i]
	}
	return keys
}

func newFullRandomEntryGenerator(start, size int) entryGenerator {
	r := rand.New(rand.NewSource(time.Now().Unix()))
	return &pairedEntryGenerator{
		keyGenerator:         newFullRandomKeyGenerator(start, size),
		randomValueGenerator: makeRandomValueGenerator(r, *compressionRatio, *valueSize),
	}
}

func newSequentialEntryGenerator(start, size int) entryGenerator {
	r := rand.New(rand.NewSource(time.Now().Unix()))
	return &pairedEntryGenerator{
		keyGenerator:         &predefinedKeyGenerator{keys: newSequentialKeys(size, start, hitKeyFormat)},
		randomValueGenerator: makeRandomValueGenerator(r, *compressionRatio, *valueSize),
	}
}

// entryBlocks feeds a measured loop from a block of entries prepared ahead of
// the iterations that use it.
//
// A measured loop can run for many more iterations than any dataset prepared
// before it holds, and b.Loop will not say how many in advance. Generating
// keys inside the loop would put the cost of fmt.Fprintf into every reported
// ns/op, and reusing one block would turn a write benchmark into an overwrite
// benchmark and a delete benchmark into a delete-missing benchmark. So the
// loop walks a block, and when it runs off the end the next block of distinct
// keys is built - and, for the delete benchmarks, written into the store -
// with the timer stopped. Only the operation under test is ever measured.
type entryBlocks struct {
	b      *testing.B
	newGen func(start, size int) entryGenerator
	// refill runs on every new block, with the timer stopped. The delete
	// benchmarks use it to put the block into the store first.
	refill func(entryGenerator)
	size   int
	start  int
	i      int
	g      entryGenerator
}

// newEntryBlocks prepares the first block. Call it before the measured loop:
// work done before the first b.Loop call is not measured.
func newEntryBlocks(b *testing.B, newGen func(start, size int) entryGenerator, refill func(entryGenerator)) *entryBlocks {
	b.Helper()

	eb := &entryBlocks{b: b, newGen: newGen, refill: refill, size: *datasetSize}
	eb.roll()
	return eb
}

// roll replaces the current block. The timer must be stopped, or not yet
// started, when it is called.
func (eb *entryBlocks) roll() {
	eb.g = eb.newGen(eb.start, eb.size)
	eb.start += eb.size
	eb.i = 0
	if eb.refill != nil {
		eb.refill(eb.g)
	}
}

// next returns the key and value for the next measured iteration.
func (eb *entryBlocks) next() (key, value []byte) {
	if eb.i == eb.size {
		eb.b.StopTimer()
		eb.roll()
		eb.b.StartTimer()
	}
	i := eb.i
	eb.i++
	return eb.g.Key(i), eb.g.Value(i)
}

type keyGenerator interface {
	NKey() int
	Key(i int) []byte
}

type reversedKeyGenerator struct {
	keyGenerator
}

var _ keyGenerator = (*reversedKeyGenerator)(nil)

func (g *reversedKeyGenerator) Key(i int) []byte {
	return g.keyGenerator.Key(g.NKey() - i - 1)
}

func newReversedKeyGenerator(g keyGenerator) keyGenerator {
	return &reversedKeyGenerator{keyGenerator: g}
}

type roundKeyGenerator struct {
	keyGenerator
}

var _ keyGenerator = (*roundKeyGenerator)(nil)

func (g *roundKeyGenerator) Key(i int) []byte {
	index := i % g.NKey()
	return g.keyGenerator.Key(index)
}

func newRoundKeyGenerator(g keyGenerator) keyGenerator {
	return &roundKeyGenerator{keyGenerator: g}
}

type predefinedKeyGenerator struct {
	keys [][]byte
}

func (g *predefinedKeyGenerator) NKey() int {
	return len(g.keys)
}

func (g *predefinedKeyGenerator) Key(i int) []byte {
	if i >= len(g.keys) {
		return g.keys[0]
	}
	return g.keys[i]
}

func newRandomKeyGenerator(n int) keyGenerator {
	return &predefinedKeyGenerator{keys: newRandomKeys(n, hitKeyFormat)}
}

func newRandomMissingKeyGenerator(n int) keyGenerator {
	return &predefinedKeyGenerator{keys: newRandomKeys(n, missingKeyFormat)}
}

func newFullRandomKeyGenerator(start, n int) keyGenerator {
	return &predefinedKeyGenerator{keys: newFullRandomKeys(n, start, hitKeyFormat)}
}

func newSequentialKeyGenerator(n int) keyGenerator {
	return &predefinedKeyGenerator{keys: newSequentialKeys(n, 0, hitKeyFormat)}
}

func maxInt(a int, b int) int {
	if a >= b {
		return a
	}
	return b
}

// doRead measures Get over the keys g produces.
//
// The loop runs for as many iterations as b.Loop decides, which is normally
// far more than the dataset holds, so g must be one that wraps: pass it
// through newRoundKeyGenerator, outermost, or the reads run off the end of the
// key set and fall back on its first key for ever after.
func doRead(b *testing.B, db storage.Store, g keyGenerator, allowNotFound bool) {
	b.Helper()

	for i := 0; b.Loop(); i++ {
		key := g.Key(i)
		item := &obj1{
			Id: string(key),
		}
		err := db.Get(item)
		switch {
		case err == nil:
		case allowNotFound && errors.Is(err, storage.ErrNotFound):
		default:
			b.Fatalf("%d: db get key[%s] error: %s\n", b.N, key, err)
		}
	}
}

type singularDBWriter struct {
	db storage.Store
}

func (w *singularDBWriter) Put(key, value []byte) error {
	item := &obj1{
		Id:  string(key),
		Buf: value,
	}
	return w.db.Put(item)
}

func (w *singularDBWriter) Delete(key []byte) error {
	item := &obj1{
		Id: string(key),
	}
	return w.db.Delete(item)
}

func newDBWriter(db storage.Store) *singularDBWriter {
	return &singularDBWriter{db: db}
}

// doWrite measures Put over an unbounded run of distinct keys.
func doWrite(b *testing.B, db storage.Store, eb *entryBlocks) {
	b.Helper()

	w := newDBWriter(db)
	for b.Loop() {
		key, value := eb.next()
		if err := w.Put(key, value); err != nil {
			b.Fatalf("write key '%s': %v", string(key), err)
		}
	}
}

// doDelete measures Delete of keys that are present. eb must have been given a
// refill that writes each block into db, or the loop would spend all but its
// first pass deleting keys that are already gone.
func doDelete(b *testing.B, db storage.Store, eb *entryBlocks) {
	b.Helper()

	w := newDBWriter(db)
	for b.Loop() {
		key, _ := eb.next()
		if err := w.Delete(key); err != nil {
			b.Fatalf("delete key '%s': %v", string(key), err)
		}
	}
}

// writeDataset writes every entry of g into db outside the measured loop.
//
// Setup cannot go through doWrite: b.Loop drives the one measured phase a
// benchmark is allowed, and calling it a second time fails with "B.Loop called
// with timer stopped".
func writeDataset(b *testing.B, db storage.Store, g entryGenerator) {
	b.Helper()

	w := newDBWriter(db)
	for i := range g.NKey() {
		if err := w.Put(g.Key(i), g.Value(i)); err != nil {
			b.Fatalf("write key '%s': %v", string(g.Key(i)), err)
		}
	}
}

func resetBenchmark(b *testing.B) {
	b.Helper()

	runtime.GC()
	b.ResetTimer()
}

// populate writes the fixed dataset that the read and iterate benchmarks work
// against.
func populate(b *testing.B, db storage.Store) {
	b.Helper()

	writeDataset(b, db, newFullRandomEntryGenerator(0, *datasetSize))
}

// chunk
// doDeleteChunk measures Delete of chunks that are present. eb must have been
// given a refill that writes each block, or this measures deleting chunks that
// were never there, which is a different and much cheaper operation.
func doDeleteChunk(b *testing.B, db storage.ChunkStore, eb *entryBlocks) {
	b.Helper()

	for b.Loop() {
		key, _ := eb.next()
		if err := db.Delete(context.Background(), chunkAddress(b, key)); err != nil {
			b.Fatalf("delete key '%s': %v", string(key), err)
		}
	}
}

// chunkAddress turns a benchmark key into the address a chunk is stored under.
//
// Every path that touches a chunk must build its address this way. They did
// not: the write path decoded the 16-character key into the first 8 bytes of a
// 32-byte buffer, while the read and delete paths parsed the same characters
// into an 8-byte address. No read could ever match a write, so the read
// benchmarks were timing a miss and reporting it under the name of a hit. See
// issue #160.
//
// The decoded bytes go at the front, leaving the tail zero, because a chunk
// store partitions on the address prefix. Right-aligning would give every key
// in the dataset the same 24-byte prefix and put the whole dataset in one bin,
// which is not what a node's store looks like.
func chunkAddress(b *testing.B, key []byte) swarm.Address {
	b.Helper()

	buf := make([]byte, swarm.HashSize)
	if _, err := hex.Decode(buf, key); err != nil {
		b.Fatalf("decode key %q: %v", string(key), err)
	}
	return swarm.NewAddress(buf)
}

// writeChunkDataset writes g's whole dataset. It does not call b.Loop, so it
// is safe to use as the setup phase of a benchmark that measures something
// else. Calling b.Loop twice in one benchmark panics.
func writeChunkDataset(b *testing.B, db storage.Putter, g entryGenerator) {
	b.Helper()

	for i := range g.NKey() {
		chunk := swarm.NewChunk(chunkAddress(b, g.Key(i)), g.Value(i)).
			WithStamp(postagetesting.MustNewStamp())
		if err := db.Put(context.Background(), chunk); err != nil {
			b.Fatalf("write key '%s': %v", string(g.Key(i)), err)
		}
	}
}

// populateChunks writes the fixed dataset the chunk read, iterate and delete
// benchmarks work against. The chunk counterpart of populate.
func populateChunks(b *testing.B, db storage.Putter) {
	b.Helper()

	writeChunkDataset(b, db, newFullRandomEntryGenerator(0, *datasetSize))
}

// doWriteChunk measures Put. It draws from rolling blocks rather than one
// fixed dataset: a measured loop can run for many more iterations than any
// dataset prepared before it, and reusing the same keys would make this an
// overwrite benchmark under the name of a write benchmark.
func doWriteChunk(b *testing.B, db storage.Putter, eb *entryBlocks) {
	b.Helper()

	for b.Loop() {
		key, value := eb.next()
		chunk := swarm.NewChunk(chunkAddress(b, key), value).
			WithStamp(postagetesting.MustNewStamp())
		if err := db.Put(context.Background(), chunk); err != nil {
			b.Fatalf("write key '%s': %v", string(key), err)
		}
	}
}

func doReadChunk(b *testing.B, db storage.ChunkStore, g keyGenerator, allowNotFound bool) {
	b.Helper()

	// Outside the timed loop, and before it, so the benchmark cannot report a
	// number at all unless it is reading what it claims to read. A chunk store
	// answering not-found is a legitimate answer rather than an error, so
	// without this a benchmark that looks up addresses nobody stored still
	// produces a plausible figure. That is exactly what issue #160 was: the
	// read benchmarks had been timing misses and calling them hits.
	if !allowNotFound {
		if _, err := db.Get(context.Background(), chunkAddress(b, g.Key(0))); err != nil {
			b.Fatalf("read benchmark cannot find its own data at key '%s': %v; "+
				"it would otherwise report the cost of a miss as a hit", string(g.Key(0)), err)
		}
	}

	for i := 0; b.Loop(); i++ {
		key := string(g.Key(i))
		_, err := db.Get(context.Background(), chunkAddress(b, g.Key(i)))
		switch {
		case err == nil:
		case allowNotFound && errors.Is(err, storage.ErrNotFound):
		default:
			b.Fatalf("%d: db get key[%s] error: %s\n", b.N, key, err)
		}
	}
}

// fixed size batch
type batchDBWriter struct {
	db    storage.Batcher
	batch storage.Batch
	max   int
	count int
}

func (w *batchDBWriter) commit(maxValue int) {
	if w.count >= maxValue {
		_ = w.batch.Commit()
		w.count = 0
		w.batch = w.db.Batch(context.Background())
	}
}

func (w *batchDBWriter) Put(key, value []byte) {
	item := &obj1{
		Id:  string(key),
		Buf: value,
	}
	_ = w.batch.Put(item)
	w.count++
	w.commit(w.max)
}

func (w *batchDBWriter) Delete(key []byte) {
	item := &obj1{
		Id: string(key),
	}
	_ = w.batch.Delete(item)
	w.count++
	w.commit(w.max)
}

func newBatchDBWriter(db storage.Batcher) *batchDBWriter {
	batch := db.Batch(context.Background())
	return &batchDBWriter{
		db:    db,
		batch: batch,
		max:   *batchSize,
	}
}
