// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package reserve_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/ethersphere/bee/v2/pkg/cac"
	"github.com/ethersphere/bee/v2/pkg/crypto"
	"github.com/ethersphere/bee/v2/pkg/log"
	"github.com/ethersphere/bee/v2/pkg/postage"
	postagetesting "github.com/ethersphere/bee/v2/pkg/postage/testing"
	"github.com/ethersphere/bee/v2/pkg/sharky"
	"github.com/ethersphere/bee/v2/pkg/soc"
	"github.com/ethersphere/bee/v2/pkg/storage"
	"github.com/ethersphere/bee/v2/pkg/storage/leveldbstore"
	"github.com/ethersphere/bee/v2/pkg/storer/internal/chunkstore"
	"github.com/ethersphere/bee/v2/pkg/storer/internal/reserve"
	"github.com/ethersphere/bee/v2/pkg/storer/internal/transaction"
	"github.com/ethersphere/bee/v2/pkg/swarm"
	kademlia "github.com/ethersphere/bee/v2/pkg/topology/mock"
)

type verifiedTestFS struct{ dir string }

func (d verifiedTestFS) Open(path string) (fs.File, error) {
	return os.OpenFile(filepath.Join(d.dir, path), os.O_RDWR|os.O_CREATE, 0o600)
}

// verifiedTestStore is a reserve over real storage (leveldb index store and
// sharky), so the retrieval index, its reference counts and the sharky
// locations are part of what is compared.
type verifiedTestStore struct {
	r     *reserve.Reserve
	st    transaction.Storage
	index *leveldbstore.Store
}

func newVerifiedTestStore(t *testing.T, baseAddr swarm.Address) *verifiedTestStore {
	t.Helper()
	sh, err := sharky.New(verifiedTestFS{t.TempDir()}, 4, swarm.SocMaxChunkSize)
	if err != nil {
		t.Fatal(err)
	}
	index, _, err := leveldbstore.New("", nil)
	if err != nil {
		t.Fatal(err)
	}
	st := transaction.NewStorage(sh, index)
	t.Cleanup(func() { _ = st.Close() })
	r, err := reserve.New(baseAddr, st, 0, kademlia.NewTopologyDriver(), log.Noop)
	if err != nil {
		t.Fatal(err)
	}
	return &verifiedTestStore{r: r, st: st, index: index}
}

// dump returns every key and value in the index store, in key order. In a
// retrieval index entry the timestamp (wall clock) and the sharky location
// (sharky picks a shard per write) differ between two stores whatever the
// code does, so both are set to zero; the address and the reference count
// are kept, and the stored data is compared separately. The epoch item holds
// the wall-clock second reserve.New ran in, so two stores created across a
// second boundary differ there too; its value is set to zero.
func (s *verifiedTestStore) dump(t *testing.T) [][2][]byte {
	t.Helper()
	var out [][2][]byte
	it := s.index.DB().NewIterator(nil, nil)
	defer it.Release()
	retrievalPrefix := []byte(chunkstore.RetrievalIndexItem{}.Namespace() + "/")
	epochPrefix := []byte(new(reserve.EpochItem).Namespace() + "/")
	for it.Next() {
		value := bytes.Clone(it.Value())
		if bytes.HasPrefix(it.Key(), epochPrefix) {
			value = make([]byte, len(value))
		}
		if bytes.HasPrefix(it.Key(), retrievalPrefix) {
			item := new(chunkstore.RetrievalIndexItem)
			if err := item.Unmarshal(value); err != nil {
				t.Fatal(err)
			}
			item.Timestamp = 0
			item.Location = sharky.Location{}
			var err error
			if value, err = item.Marshal(); err != nil {
				t.Fatal(err)
			}
		}
		out = append(out, [2][]byte{bytes.Clone(it.Key()), value})
	}
	if err := it.Error(); err != nil {
		t.Fatal(err)
	}
	return out
}

// verifiedStep is one reserve put of the equivalence scenario: the chunk,
// and the type pullsync or pushsync would pass for it.
type verifiedStep struct {
	name      string
	chunk     swarm.Chunk
	chunkType swarm.ChunkType
}

// copyChunk returns a new chunk with the same address, data and stamp, so the
// two stores never share a chunk value.
func copyChunk(ch swarm.Chunk) swarm.Chunk {
	return swarm.NewChunk(ch.Address(), bytes.Clone(ch.Data())).WithStamp(ch.Stamp())
}

func mustSOC(t *testing.T, signer crypto.Signer, id []byte, wrapped swarm.Chunk) swarm.Chunk {
	t.Helper()
	ch, err := soc.New(id, wrapped).Sign(signer)
	if err != nil {
		t.Fatal(err)
	}
	return ch
}

func mustCAC(t *testing.T, data []byte) swarm.Chunk {
	t.Helper()
	ch, err := cac.New(data)
	if err != nil {
		t.Fatal(err)
	}
	return ch
}

func randomData(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

// verifiedScenario covers every case Reserve.Put distinguishes by chunk type:
// a CAC, a SOC, a dispersed replica SOC, a SOC overwritten under the same
// stamp index with a newer and with an older timestamp, a different chunk at
// the same stamp index, a repeated put, and two invalid SOCs.
func verifiedScenario(t *testing.T) []verifiedStep {
	t.Helper()

	batch := postagetesting.MustNewBatch()
	key, err := crypto.GenerateSecp256k1Key()
	if err != nil {
		t.Fatal(err)
	}
	signer := crypto.NewDefaultSigner(key)
	replicasKey, err := crypto.DecodeSecp256k1PrivateKey(append([]byte{1}, make([]byte, 31)...))
	if err != nil {
		t.Fatal(err)
	}
	replicas := crypto.NewDefaultSigner(replicasKey)

	cacA := mustCAC(t, randomData(t, swarm.ChunkSize))
	cacB := mustCAC(t, randomData(t, swarm.ChunkSize))
	id := randomData(t, swarm.HashSize)
	soc1 := mustSOC(t, signer, id, mustCAC(t, []byte("first")))
	soc2 := mustSOC(t, signer, id, mustCAC(t, []byte("second")))
	soc3 := mustSOC(t, signer, id, mustCAC(t, []byte("third")))
	wrapped := mustCAC(t, randomData(t, swarm.ChunkSize))
	replica := mustSOC(t, replicas, append([]byte{1}, wrapped.Address().Bytes()[1:]...), wrapped)

	// a SOC sent under an address other than keccak(id, owner)
	other := mustSOC(t, signer, randomData(t, swarm.HashSize), mustCAC(t, []byte("other")))
	wrongAddress := swarm.NewChunk(cacA.Address(), other.Data())
	// a replica SOC whose id does not match the wrapped chunk address
	badReplica := mustSOC(t, replicas, bytes.Repeat([]byte{0xff}, swarm.HashSize), wrapped)

	stamp := func(index, ts uint64) *postage.Stamp {
		return postagetesting.MustNewFields(batch.ID, index, ts)
	}
	return []verifiedStep{
		{"cac", cacA.WithStamp(stamp(0, 1)), swarm.ChunkTypeContentAddressed},
		{"soc", soc1.WithStamp(stamp(1, 1)), swarm.ChunkTypeSingleOwner},
		{"dispersed replica soc", replica.WithStamp(stamp(2, 1)), swarm.ChunkTypeSingleOwner},
		{"soc overwrite with newer stamp", soc2.WithStamp(stamp(1, 2)), swarm.ChunkTypeSingleOwner},
		{"soc overwrite with older stamp", soc3.WithStamp(stamp(1, 0)), swarm.ChunkTypeSingleOwner},
		{"different chunk at the same stamp index", cacB.WithStamp(stamp(0, 2)), swarm.ChunkTypeContentAddressed},
		{"same chunk and stamp again", cacB.WithStamp(stamp(0, 2)), swarm.ChunkTypeContentAddressed},
		{"soc under a wrong address", wrongAddress.WithStamp(stamp(3, 1)), swarm.ChunkTypeSingleOwner},
		{"replica soc with a wrong id", badReplica.WithStamp(stamp(4, 1)), swarm.ChunkTypeSingleOwner},
	}
}

func errString(err error) string {
	if err == nil {
		return "<nil>"
	}
	return err.Error()
}

// TestPutVerifiedMatchesPut checks that storing chunks with the type a caller
// verified leaves the reserve byte for byte as storing them with Put, which
// computes the type itself: every index entry (batchRadius, chunkBin with its
// chunk type, stamp index, chunk stamp, retrieval index with reference counts
// and sharky locations), every result and error, the size and the stored
// data (wasp #810).
func TestPutVerifiedMatchesPut(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	baseAddr := swarm.RandAddress(t)
	steps := verifiedScenario(t)

	viaPut := newVerifiedTestStore(t, baseAddr)
	viaVerified := newVerifiedTestStore(t, baseAddr)

	errs := make([]error, len(steps))
	for i, s := range steps {
		errPut := viaPut.r.Put(ctx, copyChunk(s.chunk))
		errs[i] = errPut
		errVerified := viaVerified.r.PutVerified(ctx, copyChunk(s.chunk), s.chunkType)
		if errString(errPut) != errString(errVerified) {
			t.Fatalf("%s: Put returned %q, PutVerified %q", s.name, errString(errPut), errString(errVerified))
		}
		for _, target := range []error{storage.ErrOverwriteNewerChunk, storage.ErrInvalidChunk} {
			if errors.Is(errPut, target) != errors.Is(errVerified, target) {
				t.Fatalf("%s: errors.Is(%v) differs: Put %v, PutVerified %v", s.name, target, errPut, errVerified)
			}
		}
		if a, b := viaPut.r.Size(), viaVerified.r.Size(); a != b {
			t.Fatalf("%s: size %d after Put, %d after PutVerified", s.name, a, b)
		}
		if a, b := viaPut.dump(t), viaVerified.dump(t); !equalDumps(a, b) {
			t.Fatalf("%s: index stores differ:\n%s", s.name, diffDumps(a, b))
		}
	}

	// The scenario must reach the paths it names.
	for i, err := range errs {
		var want error
		switch i {
		case 4:
			want = storage.ErrOverwriteNewerChunk
		case 7, 8:
			want = storage.ErrInvalidChunk
		}
		if want == nil && err != nil || want != nil && !errors.Is(err, want) {
			t.Fatalf("%s: Put returned %v, want %v", steps[i].name, err, want)
		}
	}
	// The newer overwrite replaced the soc, and the different chunk
	// replaced the first cac.
	if a, b := viaPut.r.Size(), 3; a != b {
		t.Fatalf("reserve size %d, want %d", a, b)
	}

	for _, s := range steps[:7] {
		for _, st := range []*verifiedTestStore{viaPut, viaVerified} {
			got, err := st.st.ChunkStore().Get(ctx, s.chunk.Address())
			if err != nil {
				if errors.Is(err, storage.ErrNotFound) {
					continue
				}
				t.Fatal(err)
			}
			if !bytes.Equal(got.Data(), mustGetData(t, ctx, viaPut, s.chunk.Address())) {
				t.Fatalf("%s: stored data differs", s.name)
			}
		}
	}
	// The two invalid socs were passed as single owner chunks, which
	// pullsync and pushsync never do; the shadow check catches both, and
	// the reserve still rejects them exactly as Put does.
	if n := viaVerified.r.ChunkTypeShadowMismatches(); n != 2 {
		t.Fatalf("shadow check mismatches %d, want 2", n)
	}
}

func mustGetData(t *testing.T, ctx context.Context, s *verifiedTestStore, addr swarm.Address) []byte {
	t.Helper()
	ch, err := s.st.ChunkStore().Get(ctx, addr)
	if err != nil {
		t.Fatal(err)
	}
	return ch.Data()
}

func equalDumps(a, b [][2][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !bytes.Equal(a[i][0], b[i][0]) || !bytes.Equal(a[i][1], b[i][1]) {
			return false
		}
	}
	return true
}

func diffDumps(a, b [][2][]byte) string {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "%d entries vs %d entries\n", len(a), len(b))
	n := min(len(a), len(b))
	for i := range n {
		if !bytes.Equal(a[i][0], b[i][0]) || !bytes.Equal(a[i][1], b[i][1]) {
			fmt.Fprintf(&buf, "first difference at entry %d: key %q value %x vs key %q value %x\n", i, a[i][0], a[i][1], b[i][0], b[i][1])
			break
		}
	}
	return buf.String()
}

// TestPutVerifiedShadowCheckCorrectsWrongType checks that a wrong verified
// type is replaced by the computed one, counted, and leaves the reserve as
// Put would (wasp #810).
func TestPutVerifiedShadowCheckCorrectsWrongType(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	baseAddr := swarm.RandAddress(t)
	steps := verifiedScenario(t)

	viaPut := newVerifiedTestStore(t, baseAddr)
	viaWrong := newVerifiedTestStore(t, baseAddr)

	for _, s := range steps[:7] {
		wrongType := swarm.ChunkTypeSingleOwner
		if s.chunkType == swarm.ChunkTypeSingleOwner {
			wrongType = swarm.ChunkTypeContentAddressed
		}
		errPut := viaPut.r.Put(ctx, copyChunk(s.chunk))
		errWrong := viaWrong.r.PutVerified(ctx, copyChunk(s.chunk), wrongType)
		if errString(errPut) != errString(errWrong) {
			t.Fatalf("%s: Put returned %q, PutVerified with a wrong type %q", s.name, errString(errPut), errString(errWrong))
		}
		if a, b := viaPut.dump(t), viaWrong.dump(t); !equalDumps(a, b) {
			t.Fatalf("%s: index stores differ:\n%s", s.name, diffDumps(a, b))
		}
	}
	// The type is checked on every put that gets past the "already
	// stored" check: all of the first seven steps except the repeated put.
	if got, want := viaWrong.r.ChunkTypeShadowMismatches(), uint64(6); got != want {
		t.Fatalf("shadow check mismatches %d, want %d", got, want)
	}
}

// TestPutVerifiedShadowCheckSampling checks that, as on a node, only every
// ShadowCheckEvery-th verified type is rechecked.
func TestPutVerifiedShadowCheckSampling(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := newVerifiedTestStore(t, swarm.RandAddress(t))
	st.r.SetShadowAlways(false)

	batch := postagetesting.MustNewBatch()
	n := 2*reserve.ShadowCheckEvery + 3
	for i := range n {
		ch := mustCAC(t, randomData(t, 64)).WithStamp(postagetesting.MustNewFields(batch.ID, uint64(i), 1))
		// A wrong type: only the sampled calls notice it.
		if err := st.r.PutVerified(ctx, ch, swarm.ChunkTypeSingleOwner); err != nil {
			t.Fatal(err)
		}
	}
	if got := st.r.ChunkTypeShadowMismatches(); got != 2 {
		t.Fatalf("shadow check mismatches %d after %d calls, want 2", got, n)
	}
}

// TestPutVerifiedRejectsUnspecifiedType checks that PutVerified does not
// accept a type that would mean "compute it".
func TestPutVerifiedRejectsUnspecifiedType(t *testing.T) {
	t.Parallel()

	st := newVerifiedTestStore(t, swarm.RandAddress(t))
	ch := mustCAC(t, randomData(t, 64)).WithStamp(postagetesting.MustNewStamp())
	err := st.r.PutVerified(context.Background(), ch, swarm.ChunkTypeUnspecified)
	if !errors.Is(err, storage.ErrInvalidChunk) {
		t.Fatalf("got %v, want %v", err, storage.ErrInvalidChunk)
	}
	if st.r.Size() != 0 {
		t.Fatalf("size %d, want 0", st.r.Size())
	}
}
