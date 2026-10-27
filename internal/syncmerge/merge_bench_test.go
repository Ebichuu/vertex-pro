package syncmerge_test

import (
	"fmt"
	"testing"

	"github.com/vertex-pro/vertex-pro/internal/syncfixture"
	"github.com/vertex-pro/vertex-pro/internal/syncmerge"
)

var benchSizes = []int{1_000, 10_000, 100_000}

// One 10s tick: 5% of torrents report transfer-field changes.
func BenchmarkMergeIncremental(b *testing.B) {
	for _, n := range benchSizes {
		b.Run(fmt.Sprintf("torrents=%d", n), func(b *testing.B) {
			baseline, err := syncmerge.Merge(syncmerge.Snapshot{}, syncfixture.FullDelta(n, 1))
			if err != nil {
				b.Fatal(err)
			}
			delta := syncfixture.IncrementalDelta(n, n/20, 2)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if _, err := syncmerge.Merge(baseline.Snapshot, delta); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkMergeFull(b *testing.B) {
	for _, n := range benchSizes {
		b.Run(fmt.Sprintf("torrents=%d", n), func(b *testing.B) {
			delta := syncfixture.FullDelta(n, 1)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if _, err := syncmerge.Merge(syncmerge.Snapshot{}, delta); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
