package syncmerge_test

import (
	"fmt"
	"runtime"
	"testing"

	"github.com/vertex-pro/vertex-pro/internal/syncfixture"
	"github.com/vertex-pro/vertex-pro/internal/syncmerge"
)

var retained syncmerge.Snapshot

// Reports the live heap one full snapshot keeps per torrent: this is the
// resident cost of every instance's watcher between ticks. Only the
// heapB/torrent metric is meaningful; ns/op includes fixture setup and GC.
func BenchmarkSnapshotRetainedHeap(b *testing.B) {
	for _, n := range []int{10_000, 100_000} {
		b.Run(fmt.Sprintf("torrents=%d", n), func(b *testing.B) {
			var perTorrent float64
			for b.Loop() {
				retained = syncmerge.Snapshot{}
				gc()
				var before, after runtime.MemStats
				runtime.ReadMemStats(&before)
				delta := syncfixture.FullDelta(n, 1)
				result, err := syncmerge.Merge(syncmerge.Snapshot{}, delta)
				if err != nil {
					b.Fatal(err)
				}
				delta = syncmerge.Delta{}
				retained = result.Snapshot
				gc()
				runtime.ReadMemStats(&after)
				perTorrent = float64(after.HeapAlloc-before.HeapAlloc) / float64(n)
			}
			b.ReportMetric(perTorrent, "heapB/torrent")
		})
	}
}

// Two cycles also drop sync.Pool victim caches (e.g. encoding/json buffers
// used by fixture conversion), so only reachable data is measured.
func gc() {
	runtime.GC()
	runtime.GC()
}
