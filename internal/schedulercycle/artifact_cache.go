package schedulercycle

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"sync"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/matchpipeline"
	"github.com/vertex-pro/vertex-pro/internal/rssfetch"
	"github.com/vertex-pro/vertex-pro/internal/rsssource"
)

const artifactCacheBudget = 64 << 20
const artifactCacheMaxEntries = 128
const artifactCacheTTL = 10 * time.Minute

type cachedArtifact struct {
	item    matchpipeline.Item
	expires time.Time
}
type artifactFlight struct {
	done chan struct{}
	item matchpipeline.Item
	err  error
}
type artifactCache struct {
	mu      sync.Mutex
	entries map[[32]byte]cachedArtifact
	flights map[[32]byte]*artifactFlight
	bytes   int
}

// Cache boundaries include the exact URL, site and credential context. Keys
// contain no printable URLs. Errors are not cached and canceled waiters return
// promptly. Validation/parsing happens before an entry is eligible for reuse.
func (cache *artifactCache) get(ctx context.Context, source rsssource.Source, feed FeedRef, load func() (matchpipeline.Item, error)) (matchpipeline.Item, error) {
	if err := ctx.Err(); err != nil {
		return matchpipeline.Item{}, err
	}
	raw, _ := json.Marshal([]string{source.SiteID, source.SourceKey, source.URL, feed.CredentialRef, feed.CredentialOrigin})
	key := sha256.Sum256(raw)
	cache.mu.Lock()
	if cache.entries == nil {
		cache.entries = map[[32]byte]cachedArtifact{}
		cache.flights = map[[32]byte]*artifactFlight{}
	}
	now := time.Now()
	for k, entry := range cache.entries {
		if !now.Before(entry.expires) {
			delete(cache.entries, k)
			cache.bytes -= len(entry.item.TorrentBody)
		}
	}
	if entry, ok := cache.entries[key]; ok {
		cache.mu.Unlock()
		return cloneArtifact(entry.item), nil
	}
	if flight, ok := cache.flights[key]; ok {
		cache.mu.Unlock()
		if rssfetch.NonblockingArtifacts(ctx) {
			return matchpipeline.Item{}, &rssfetch.ArtifactDeferred{RetryAt: time.Now().Add(250 * time.Millisecond)}
		}
		select {
		case <-ctx.Done():
			return matchpipeline.Item{}, ctx.Err()
		case <-flight.done:
			return cloneArtifact(flight.item), flight.err
		}
	}
	flight := &artifactFlight{done: make(chan struct{})}
	cache.flights[key] = flight
	cache.mu.Unlock()
	item, err := load()
	cache.mu.Lock()
	flight.item, flight.err = cloneArtifact(item), err
	if err == nil && len(item.TorrentBody) <= artifactCacheBudget {
		for cache.bytes+len(item.TorrentBody) > artifactCacheBudget || len(cache.entries) >= artifactCacheMaxEntries {
			var oldest [32]byte
			var expiry time.Time
			for k, entry := range cache.entries {
				if expiry.IsZero() || entry.expires.Before(expiry) {
					oldest, expiry = k, entry.expires
				}
			}
			cache.bytes -= len(cache.entries[oldest].item.TorrentBody)
			delete(cache.entries, oldest)
		}
		cache.entries[key] = cachedArtifact{item: cloneArtifact(item), expires: time.Now().Add(artifactCacheTTL)}
		cache.bytes += len(item.TorrentBody)
	}
	delete(cache.flights, key)
	close(flight.done)
	cache.mu.Unlock()
	return item, err
}

func cloneArtifact(item matchpipeline.Item) matchpipeline.Item {
	item.TorrentBody = append([]byte(nil), item.TorrentBody...)
	if item.Source.SizeBytes != nil {
		size := *item.Source.SizeBytes
		item.Source.SizeBytes = &size
	}
	return item
}
