package confirmcoordinator

import (
	"fmt"
	"testing"

	"github.com/vertex-pro/vertex-pro/internal/syncfixture"
	"github.com/vertex-pro/vertex-pro/internal/syncmerge"
)

// SnapshotDigest re-encodes the whole snapshot; it runs on every tick that has
// an active waiter or UNKNOWN chain unless the event already carries a digest.
func BenchmarkSnapshotDigest(b *testing.B) {
	for _, n := range []int{1_000, 10_000} {
		b.Run(fmt.Sprintf("torrents=%d", n), func(b *testing.B) {
			result, err := syncmerge.Merge(syncmerge.Snapshot{}, syncfixture.FullDelta(n, 1))
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				_ = SnapshotDigest(result.Snapshot)
			}
		})
	}
}
