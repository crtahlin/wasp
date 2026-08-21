package beebench

import (
	"context"
	"sync"
	"testing"

	"github.com/ethersphere/bee/v2/pkg/sharky"
)

// startWriters runs n goroutines writing chunks through sharky until stopped.
// Slots are released immediately so the store does not grow.
func startWriters(tb testing.TB, sk *sharky.Store, n int) func() {
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			buf := make([]byte, chunkLen)
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}
				l, err := sk.Write(ctx, buf)
				if err != nil {
					return
				}
				_ = sk.Release(ctx, l)
			}
		}()
	}
	return func() { cancel(); wg.Wait() }
}
