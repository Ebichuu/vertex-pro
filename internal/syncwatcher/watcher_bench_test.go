package syncwatcher

import (
	"context"
	"fmt"
	"testing"

	"github.com/vertex-pro/vertex-pro/internal/syncfixture"
	"github.com/vertex-pro/vertex-pro/internal/syncmerge"
)

// tickFetcher returns a full baseline first, then rotates through
// precomputed incremental deltas with a monotonically increasing RID, so the
// benchmark measures the watcher rather than fixture construction.
type tickFetcher struct {
	full   syncmerge.Delta
	deltas []syncmerge.Delta
	rid    uint64
}

func newTickFetcher(n int) *tickFetcher {
	fetcher := &tickFetcher{full: syncfixture.FullDelta(n, 1)}
	for i := 0; i < 16; i++ {
		fetcher.deltas = append(fetcher.deltas, syncfixture.IncrementalDelta(n, n/20, uint64(i+2)))
	}
	return fetcher
}

func (fetcher *tickFetcher) Fetch(_ context.Context, rid uint64) (syncmerge.Delta, error) {
	fetcher.rid++
	if rid == 0 {
		delta := fetcher.full
		delta.RID = fetcher.rid
		return delta, nil
	}
	delta := fetcher.deltas[int(fetcher.rid)%len(fetcher.deltas)]
	delta.RID = fetcher.rid
	return delta, nil
}

// One background sync tick end to end: fetch (pre-decoded), merge, persist
// the revision and publish the event. Fetch decoding is measured separately.
func BenchmarkRunOnceIncremental(b *testing.B) {
	for _, n := range []int{1_000, 10_000} {
		b.Run(fmt.Sprintf("torrents=%d", n), func(b *testing.B) {
			store, database := newWatcherStore(b)
			defer database.Close()
			insertState(b, database, "qb-1")
			watcher, err := New("qb-1", newTickFetcher(n), store)
			if err != nil {
				b.Fatal(err)
			}
			if _, err := watcher.RunOnce(context.Background()); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if _, err := watcher.RunOnce(context.Background()); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
