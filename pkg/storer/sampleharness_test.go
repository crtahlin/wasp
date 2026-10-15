// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build samplerharness

package storer_test

// An offline harness that runs the reserve sampler as it is now and as it was
// before the port of Bee v2.8.3's sampler changes on a real node's data
// directory, with pinned inputs, and writes every item of both samples. It is
// built only with the samplerharness tag, so it is not part of any normal
// test run.
//
// SAMPLER_DATA_DIR is the node's data directory (the one holding statestore/
// and localstore/), or its localstore/ directly. The harness refuses to run
// when it finds no store there, when the store's shard count is not the
// node's, or when the reserve is empty, rather than creating or sampling an
// empty store. The binary uses the node's 32 sharky shards, not the 4 the
// package's other tests use (testSharkyShards, set by the build tag).
//
// Run it only on a COPY of a stopped node's data directory: opening the store
// runs the store migrations, which includes Bee v2.8.3's rogue-chunk
// migration on a directory that has not had it. The reserve worker is not
// started, so nothing else changes the copy.
//
//	go test -c -tags samplerharness -o sampler-harness ./pkg/storer/
//	SAMPLER_DATA_DIR=/path/to/copy \
//	SAMPLER_ANCHOR=<64 hex> SAMPLER_DEPTH=<committed depth> \
//	SAMPLER_CONSENSUS_TIME=<ns> SAMPLER_MIN_BATCH_BALANCE=<decimal or empty> \
//	SAMPLER_ORDER=merged,premerge SAMPLER_OUT=/path/result.json \
//	./sampler-harness -test.run TestSamplerHarness -test.v -test.timeout 2h
//
// The inputs come from a real round's "reserve sampler finished" log line and
// the round's anchor. The result holds, per run, the items (chunk address,
// transformed address, SHA-256 of the data, stamp), the sample hash, whether
// every item's data re-hashes to its addresses, the duration and the stats,
// and whether the two samplers agreed.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ethersphere/bee/v2/pkg/bmt"
	"github.com/ethersphere/bee/v2/pkg/log"
	"github.com/ethersphere/bee/v2/pkg/node"
	"github.com/ethersphere/bee/v2/pkg/postage"
	"github.com/ethersphere/bee/v2/pkg/postage/batchstore"
	"github.com/ethersphere/bee/v2/pkg/storage"
	"github.com/ethersphere/bee/v2/pkg/storer"
	"github.com/ethersphere/bee/v2/pkg/swarm"
)

type harnessRadiusSetter struct{}

func (harnessRadiusSetter) SetStorageRadius(uint8) {}

type harnessItem struct {
	ChunkAddress       string `json:"chunkAddress"`
	TransformedAddress string `json:"transformedAddress"`
	DataSHA256         string `json:"dataSha256"`
	BatchID            string `json:"batchId"`
	Index              string `json:"index"`
	Timestamp          string `json:"timestamp"`
	Signature          string `json:"signature"`
}

type harnessRun struct {
	Sampler         string             `json:"sampler"`
	Started         time.Time          `json:"started"`
	DurationSeconds float64            `json:"durationSeconds"`
	Error           string             `json:"error,omitempty"`
	Items           []harnessItem      `json:"items"`
	Hash            string             `json:"hash"`
	Rehash          bool               `json:"rehashOk"`
	Stats           storer.SampleStats `json:"stats"`
}

func harnessEnv(t *testing.T, name string) string {
	t.Helper()
	return strings.TrimSpace(os.Getenv(name))
}

func TestSamplerHarness(t *testing.T) {
	dir := harnessEnv(t, "SAMPLER_DATA_DIR")
	if dir == "" {
		t.Skip("SAMPLER_DATA_DIR not set")
	}
	anchor, err := hex.DecodeString(harnessEnv(t, "SAMPLER_ANCHOR"))
	if err != nil || len(anchor) != swarm.HashSize {
		t.Fatalf("SAMPLER_ANCHOR: want 64 hex characters (%v)", err)
	}
	depth, err := strconv.ParseUint(harnessEnv(t, "SAMPLER_DEPTH"), 10, 8)
	if err != nil {
		t.Fatalf("SAMPLER_DEPTH: %v", err)
	}
	consensus, err := strconv.ParseUint(harnessEnv(t, "SAMPLER_CONSENSUS_TIME"), 10, 64)
	if err != nil {
		t.Fatalf("SAMPLER_CONSENSUS_TIME: %v", err)
	}
	var minBalance *big.Int
	if v := harnessEnv(t, "SAMPLER_MIN_BATCH_BALANCE"); v != "" {
		var ok bool
		if minBalance, ok = new(big.Int).SetString(v, 10); !ok {
			t.Fatalf("SAMPLER_MIN_BATCH_BALANCE: not a decimal number: %q", v)
		}
	}
	order := strings.Split(harnessEnv(t, "SAMPLER_ORDER"), ",")
	if len(order) == 1 && order[0] == "" {
		order = []string{"merged", "premerge"}
	}

	logger := log.NewLogger("sampler-harness", log.WithSink(os.Stderr), log.WithVerbosity(log.VerbosityInfo))
	ctx := context.Background()
	db, closeStore, err := openHarnessStore(ctx, logger, dir, harnessEnv(t, "SAMPLER_STORAGE_ENGINE"))
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore()

	result := struct {
		Anchor         string       `json:"anchor"`
		Depth          uint64       `json:"depth"`
		ConsensusTime  uint64       `json:"consensusTime"`
		MinBatchValue  string       `json:"minBatchBalance"`
		StorageRadius  uint8        `json:"storageRadius"`
		Runs           []harnessRun `json:"runs"`
		SamplersAgreed bool         `json:"samplersAgreed"`
	}{
		Anchor:        hex.EncodeToString(anchor),
		Depth:         depth,
		ConsensusTime: consensus,
		StorageRadius: db.StorageRadius(),
	}
	if minBalance != nil {
		result.MinBatchValue = minBalance.String()
	}

	for _, which := range order {
		run := harnessRun{Sampler: which, Started: time.Now().UTC()}
		var sample storer.Sample
		start := time.Now()
		switch which {
		case "merged":
			sample, err = db.ReserveSample(ctx, anchor, uint8(depth), consensus, minBalance)
		case "premerge":
			sample, err = db.ReserveSamplePreMerge(ctx, anchor, uint8(depth), consensus, minBalance)
		default:
			t.Fatalf("SAMPLER_ORDER: unknown sampler %q", which)
		}
		run.DurationSeconds = time.Since(start).Seconds()
		if err != nil {
			run.Error = err.Error()
		}
		run.Stats = sample.Stats
		run.Rehash = true
		for _, it := range sample.Items {
			sum := sha256.Sum256(it.ChunkData)
			run.Items = append(run.Items, harnessItem{
				ChunkAddress:       it.ChunkAddress.String(),
				TransformedAddress: it.TransformedAddress.String(),
				DataSHA256:         hex.EncodeToString(sum[:]),
				BatchID:            hex.EncodeToString(it.Stamp.BatchID()),
				Index:              hex.EncodeToString(it.Stamp.Index()),
				Timestamp:          hex.EncodeToString(it.Stamp.Timestamp()),
				Signature:          hex.EncodeToString(it.Stamp.Sig()),
			})
			ch := swarm.NewChunk(it.ChunkAddress, it.ChunkData)
			typ := storage.ChunkType(ch)
			taddr, terr := storer.TransformedAddress(bmt.NewPrefixHasher(anchor), ch, typ)
			if typ == swarm.ChunkTypeUnspecified || terr != nil || !taddr.Equal(it.TransformedAddress) {
				run.Rehash = false
			}
		}
		if len(sample.Items) > 0 {
			run.Hash = sampleHash(t, sample.Items).String()
		}
		result.Runs = append(result.Runs, run)
		logger.Info("sampler harness run", "sampler", which, "duration", run.DurationSeconds, "items", len(run.Items), "hash", run.Hash, "rehash_ok", run.Rehash, "error", run.Error)
	}

	result.SamplersAgreed = true
	for _, r := range result.Runs[1:] {
		a, b := result.Runs[0], r
		if a.Error != "" || b.Error != "" || a.Hash != b.Hash || len(a.Items) != len(b.Items) {
			result.SamplersAgreed = false
			continue
		}
		for i := range a.Items {
			if a.Items[i] != b.Items[i] {
				result.SamplersAgreed = false
			}
		}
	}

	out, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if path := harnessEnv(t, "SAMPLER_OUT"); path != "" {
		if err := os.WriteFile(path, out, 0o600); err != nil {
			t.Fatal(err)
		}
	} else {
		if _, err := os.Stdout.Write(append(out, '\n')); err != nil {
			t.Fatal(err)
		}
	}
	if !result.SamplersAgreed {
		t.Error("the samplers did not agree")
	}
}

// harnessStoreDirs finds the node's data directory and its localstore from
// dir, which may be either. It requires the localstore's index and sharky
// directories to exist, so that a wrong path fails instead of opening, and
// so creating, a new empty store.
func harnessStoreDirs(dir string) (dataDir, localstoreDir string, err error) {
	isStore := func(d string) bool {
		for _, sub := range []string{"indexstore", "sharky"} {
			if fi, err := os.Stat(filepath.Join(d, sub)); err != nil || !fi.IsDir() {
				return false
			}
		}
		return true
	}
	switch {
	case isStore(filepath.Join(dir, "localstore")):
		return dir, filepath.Join(dir, "localstore"), nil
	case isStore(dir):
		return filepath.Dir(dir), dir, nil
	default:
		return "", "", fmt.Errorf("no localstore with indexstore/ and sharky/ in %s or %s", dir, filepath.Join(dir, "localstore"))
	}
}

// checkHarnessShards refuses a store whose sharky shard count is not the
// one the binary opens stores with. Opening with fewer shards fails every
// read from the others, and sharky's recovery after an unclean stop can
// delete their index entries.
func checkHarnessShards(localstoreDir string) error {
	shards, err := filepath.Glob(filepath.Join(localstoreDir, "sharky", "shard_[0-9][0-9][0-9]"))
	if err != nil {
		return err
	}
	if want := storer.SharkyShardLimit(); len(shards) != want {
		return fmt.Errorf("store has %d sharky shards, this binary opens stores with %d; refusing to open it", len(shards), want)
	}
	return nil
}

// openHarnessStore opens a node's store as the node does, after the checks
// above, and refuses an empty reserve.
func openHarnessStore(ctx context.Context, logger log.Logger, dir, engine string) (*storer.DB, func(), error) {
	dataDir, localstoreDir, err := harnessStoreDirs(dir)
	if err != nil {
		return nil, nil, err
	}
	if err := checkHarnessShards(localstoreDir); err != nil {
		return nil, nil, err
	}
	stateStore, _, err := node.InitStateStore(logger, dataDir, 1_000_000)
	if err != nil {
		return nil, nil, fmt.Errorf("state store: %w", err)
	}
	bs, err := batchstore.New(stateStore, func([]byte) error { return nil }, storer.DefaultReserveCapacity, logger)
	if err != nil {
		_ = stateStore.Close()
		return nil, nil, fmt.Errorf("batch store: %w", err)
	}
	db, err := storer.New(ctx, localstoreDir, &storer.Options{
		Logger:          logger,
		RadiusSetter:    harnessRadiusSetter{},
		Batchstore:      bs,
		ValidStamp:      postage.ValidStamp(bs),
		ReserveCapacity: storer.DefaultReserveCapacity,
		CacheCapacity:   1_000_000,
		StorageEngine:   engine,
	})
	if err != nil {
		_ = stateStore.Close()
		return nil, nil, fmt.Errorf("localstore: %w", err)
	}
	closeAll := func() {
		_ = db.Close()
		_ = stateStore.Close()
	}
	if n := db.ReserveSize(); n == 0 {
		closeAll()
		return nil, nil, fmt.Errorf("the reserve in %s is empty; refusing to sample it", localstoreDir)
	}
	logger.Info("sampler harness opened store", "localstore", localstoreDir, "reserve_size", db.ReserveSize(), "shards", storer.SharkyShardLimit())
	return db, closeAll, nil
}
