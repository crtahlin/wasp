package pebblestore_test

import (
	"fmt"
	"testing"

	"github.com/ethersphere/bee/v2/pkg/storage"
	"github.com/ethersphere/bee/v2/pkg/storage/pebblestore"
)

type probeItem struct {
	id  string
	buf []byte
}

func (p *probeItem) ID() string               { return p.id }
func (p *probeItem) Namespace() string        { return "probe" }
func (p *probeItem) Marshal() ([]byte, error) { return p.buf, nil }
func (p *probeItem) Unmarshal(b []byte) error { p.buf = b; return nil }
func (p *probeItem) Clone() storage.Item      { return &probeItem{id: p.id, buf: p.buf} }
func (p *probeItem) String() string           { return p.id }

func TestSSTablesAtBenchmarkScale(t *testing.T) {
	st, err := pebblestore.New(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	const n = 1_000_000
	val := make([]byte, 40)
	b := st.Batch(t.Context())
	for i := range n {
		if err := b.Put(&probeItem{id: fmt.Sprintf("key-%012d", i), buf: val}); err != nil {
			t.Fatal(err)
		}
		if i%10000 == 9999 {
			if err := b.Commit(); err != nil {
				t.Fatal(err)
			}
			b = st.Batch(t.Context())
		}
	}
	_ = b.Commit()

	m := st.DB().Metrics()
	total := int64(0)
	for i, l := range m.Levels {
		if l.NumFiles > 0 {
			t.Logf("level %d: %d files, %d bytes", i, l.NumFiles, l.Size)
		}
		total += l.NumFiles
	}
	t.Logf("sstables after %d entries: %d", n, total)
}
