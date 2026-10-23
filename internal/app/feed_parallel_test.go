package app

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/schedulercycle"
)

func TestFeedGroupsBoundConcurrencyAndDoNotBlockOtherUpstreams(t *testing.T) {
	feeds := []schedulercycle.FeedRef{
		{SiteID: "a", FeedID: "a1", URL: "https://same.test/one"},
		{SiteID: "alias", FeedID: "a2", URL: "https://same.test/two"},
		{SiteID: "alias", FeedID: "a3", URL: "https://alternate.test/rss"},
		{SiteID: "b", FeedID: "b1", URL: "https://independent.test/rss"},
	}
	blocked, other, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var active, peak atomic.Int32
	var mu sync.Mutex
	seen := map[string]int{}
	done := make(chan error, 1)
	go func() {
		done <- runFeedGroups(context.Background(), feeds, 2, func(ctx context.Context, feed schedulercycle.FeedRef) error {
			n := active.Add(1)
			defer active.Add(-1)
			for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
			}
			mu.Lock()
			seen[feed.FeedID]++
			mu.Unlock()
			switch feed.FeedID {
			case "a1":
				close(blocked)
				<-release
			case "b1":
				<-blocked
				close(other)
			default:
				select {
				case <-release:
				default:
					return fmt.Errorf("related feed ran concurrently")
				}
			}
			return nil
		})
	}()
	select {
	case <-other:
	case <-time.After(2 * time.Second):
		close(release)
		<-done
		t.Fatal("slow upstream blocked independent feed")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if peak.Load() > 2 {
		t.Fatalf("peak=%d", peak.Load())
	}
	for _, feed := range feeds {
		if seen[feed.FeedID] != 1 {
			t.Fatalf("poll count: %v", seen)
		}
	}
}

func TestFeedGroupsCanceledContextStartsNoPoll(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := runFeedGroups(ctx, []schedulercycle.FeedRef{{SiteID: "a", URL: "https://a.test"}}, 4, func(context.Context, schedulercycle.FeedRef) error { t.Error("canceled feed started"); return nil })
	if err == nil {
		t.Fatal("cancellation lost")
	}
}
