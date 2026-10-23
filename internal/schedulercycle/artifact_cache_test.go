package schedulercycle

import (
	"context"
	"errors"
	"fmt"
	"github.com/vertex-pro/vertex-pro/internal/matchpipeline"
	"github.com/vertex-pro/vertex-pro/internal/rsssource"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestArtifactCacheSharesSuccessfulAcquisitionAndCopiesBytes(t *testing.T) {
	var cache artifactCache
	var calls atomic.Int32
	source := rsssource.Source{SiteID: "s", SourceKey: "one", URL: "https://s.test/file"}
	feed := FeedRef{CredentialRef: "env:ONE"}
	size := int64(100)
	load := func() (matchpipeline.Item, error) {
		calls.Add(1)
		return matchpipeline.Item{Source: rsssource.Source{SizeBytes: &size}, IdentityKey: "hash", TorrentBody: []byte("original")}, nil
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			item, err := cache.get(context.Background(), source, feed, load)
			if err != nil || string(item.TorrentBody) != "original" {
				t.Errorf("cache failure: %v", err)
			}
			item.TorrentBody[0] = 'x'
			*item.Source.SizeBytes = 0
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("downloads=%d", calls.Load())
	}
	// Credential and source boundaries must not share cached bytes.
	feed.CredentialRef = "env:TWO"
	cache.get(context.Background(), source, feed, load)
	source.SourceKey = "two"
	cache.get(context.Background(), source, feed, load)
	if calls.Load() != 3 {
		t.Fatalf("boundaries shared: %d", calls.Load())
	}
	cache.mu.Lock()
	for k, entry := range cache.entries {
		entry.expires = time.Now().Add(-time.Second)
		cache.entries[k] = entry
	}
	cache.mu.Unlock()
	cache.get(context.Background(), source, feed, load)
	if calls.Load() != 4 {
		t.Fatal("expired entry reused")
	}
}

func TestArtifactCacheEvictsByEntryAndByteBounds(t *testing.T) {
	var cache artifactCache
	load := func() (matchpipeline.Item, error) { return matchpipeline.Item{TorrentBody: []byte("valid")}, nil }
	for i := 0; i < artifactCacheMaxEntries+1; i++ {
		cache.get(context.Background(), rsssource.Source{SiteID: "s", SourceKey: fmt.Sprint(i), URL: "https://s.test/t"}, FeedRef{}, load)
	}
	if len(cache.entries) != artifactCacheMaxEntries {
		t.Fatalf("entries=%d", len(cache.entries))
	}
	large := make([]byte, 16<<20)
	for i := 0; i < 5; i++ {
		cache.get(context.Background(), rsssource.Source{SiteID: "s", SourceKey: fmt.Sprint("large", i), URL: "https://s.test/t"}, FeedRef{}, func() (matchpipeline.Item, error) { return matchpipeline.Item{TorrentBody: large}, nil })
	}
	if cache.bytes > artifactCacheBudget || len(cache.entries) != 4 {
		t.Fatalf("bytes=%d entries=%d", cache.bytes, len(cache.entries))
	}
}

func TestArtifactCacheDoesNotCacheFailureAndCancelsWaiter(t *testing.T) {
	var cache artifactCache
	source := rsssource.Source{SiteID: "s", URL: "https://s.test/t"}
	feed := FeedRef{}
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		cache.get(context.Background(), source, feed, func() (matchpipeline.Item, error) {
			close(entered)
			<-release
			return matchpipeline.Item{}, errors.New("temporary")
		})
	}()
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := cache.get(ctx, source, feed, func() (matchpipeline.Item, error) { t.Error("second request"); return matchpipeline.Item{}, nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	close(release)
	<-done
	calls := 0
	cache.get(context.Background(), source, feed, func() (matchpipeline.Item, error) {
		calls++
		return matchpipeline.Item{TorrentBody: []byte("valid")}, nil
	})
	if calls != 1 {
		t.Fatal("failed response cached")
	}
}
