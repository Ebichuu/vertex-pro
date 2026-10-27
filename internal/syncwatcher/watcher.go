package syncwatcher

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"

	"github.com/vertex-pro/vertex-pro/internal/snapshot"
	"github.com/vertex-pro/vertex-pro/internal/syncmerge"
)

var ErrInFlight = errors.New("sync request already in flight")
var ErrClosed = errors.New("sync watcher is closed")

type Fetcher interface {
	Fetch(ctx context.Context, rid uint64) (syncmerge.Delta, error)
}

type Event struct {
	InstanceID string
	Generation uint64
	Revision   uint64
	// Digest is the committed snapshot_digest of this revision.
	Digest   string
	Snapshot syncmerge.Snapshot
}

type Waiter struct {
	ID         string
	Generation uint64
	Revision   uint64
	Events     <-chan Event
	Cancel     func()
}

type Watcher struct {
	mu         sync.Mutex
	instanceID string
	fetcher    Fetcher
	store      snapshot.Store
	generation uint64
	revision   uint64
	digest     string
	resumed    bool
	current    syncmerge.Snapshot
	record     snapshot.Record
	hasCurrent bool
	inFlight   bool
	closed     bool
	nextWaiter uint64
	waiters    map[uint64]waiterEntry
}

type waiterEntry struct {
	generation uint64
	revision   uint64
	channel    chan Event
}

func New(instanceID string, fetcher Fetcher, store snapshot.Store) (*Watcher, error) {
	if instanceID == "" {
		return nil, errors.New("instance ID is required")
	}
	if fetcher == nil {
		return nil, errors.New("fetcher is required")
	}
	return &Watcher{instanceID: instanceID, fetcher: fetcher, store: store, waiters: make(map[uint64]waiterEntry)}, nil
}

func (watcher *Watcher) RunOnce(ctx context.Context) (Event, error) {
	watcher.mu.Lock()
	if watcher.closed {
		watcher.mu.Unlock()
		return Event{}, ErrClosed
	}
	if watcher.inFlight {
		watcher.mu.Unlock()
		return Event{}, ErrInFlight
	}
	watcher.inFlight = true
	current := watcher.current
	hasCurrent := watcher.hasCurrent
	resumed := watcher.resumed
	watcher.mu.Unlock()
	defer func() {
		watcher.mu.Lock()
		watcher.inFlight = false
		watcher.mu.Unlock()
	}()

	// Snapshots are not reloaded after a restart: resume the committed
	// coordinates and re-baseline from rid=0, which always starts a new generation.
	if !resumed {
		generation, revision, digest, err := watcher.store.Coordinates(ctx, watcher.instanceID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return Event{}, fmt.Errorf("load sync coordinates: %w", err)
		}
		watcher.mu.Lock()
		watcher.generation, watcher.revision, watcher.digest, watcher.resumed = generation, revision, digest, true
		watcher.mu.Unlock()
	}
	rid := uint64(0)
	if hasCurrent {
		rid = current.RID
	}
	delta, err := watcher.fetcher.Fetch(ctx, rid)
	if err != nil {
		return Event{}, fmt.Errorf("fetch sync delta: %w", err)
	}
	result, err := syncmerge.Merge(current, delta)
	if err != nil {
		return Event{}, fmt.Errorf("merge sync delta: %w", err)
	}
	watcher.mu.Lock()
	generation, revision, previousDigest := watcher.generation, watcher.revision, watcher.digest
	watcher.mu.Unlock()
	if result.Ignored {
		return Event{InstanceID: watcher.instanceID, Generation: generation, Revision: revision, Digest: previousDigest, Snapshot: result.Snapshot}, nil
	}
	if generation == 0 || result.Reset {
		generation++
	}
	revision++
	digest := chainDigest(watcher.instanceID, generation, revision, result.Snapshot.RID, previousDigest, delta.BodyDigest)
	record, err := watcher.store.Commit(ctx, watcher.instanceID, generation, revision, result.Snapshot.RID, digest)
	if err != nil {
		return Event{}, fmt.Errorf("persist sync snapshot: %w", err)
	}
	event := Event{InstanceID: watcher.instanceID, Generation: generation, Revision: revision, Digest: digest, Snapshot: result.Snapshot}
	watcher.mu.Lock()
	watcher.current, watcher.record, watcher.hasCurrent = result.Snapshot, record, true
	watcher.generation, watcher.revision, watcher.digest = generation, revision, digest
	watcher.broadcastLocked(event)
	watcher.mu.Unlock()
	return event, nil
}

// Current returns the last committed snapshot and its record. The snapshot is
// shared and must not be mutated.
func (watcher *Watcher) Current() (syncmerge.Snapshot, snapshot.Record, bool) {
	watcher.mu.Lock()
	defer watcher.mu.Unlock()
	return watcher.current, watcher.record, watcher.hasCurrent
}

// chainDigest identifies a revision without re-encoding the snapshot: it binds
// the coordinates, the previous revision's digest and the raw response bytes.
// Every revision has a new RID, so (as with the former content hash) no two
// revisions share a digest.
func chainDigest(instanceID string, generation, revision, rid uint64, previous, body string) string {
	hasher := sha256.New()
	fmt.Fprintf(hasher, "vertex-sync-v2\x00%s\x00%d\x00%d\x00%d\x00%s\x00%s", instanceID, generation, revision, rid, previous, body)
	return hex.EncodeToString(hasher.Sum(nil))
}

func (watcher *Watcher) RegisterWaiter(generation, revision uint64) (Waiter, error) {
	watcher.mu.Lock()
	defer watcher.mu.Unlock()
	if watcher.closed {
		return Waiter{}, ErrClosed
	}
	if watcher.hasCurrent && (generation < watcher.generation || (generation == watcher.generation && revision < watcher.revision)) {
		return Waiter{}, errors.New("waiter source snapshot is stale")
	}
	watcher.nextWaiter++
	id := watcher.nextWaiter
	channel := make(chan Event, 1)
	watcher.waiters[id] = waiterEntry{generation: generation, revision: revision, channel: channel}
	return Waiter{ID: fmt.Sprintf("waiter-%d", id), Generation: generation, Revision: revision, Events: channel, Cancel: func() { watcher.cancelWaiter(id) }}, nil
}

func (watcher *Watcher) Close() {
	watcher.mu.Lock()
	defer watcher.mu.Unlock()
	if watcher.closed {
		return
	}
	watcher.closed = true
	for id, entry := range watcher.waiters {
		close(entry.channel)
		delete(watcher.waiters, id)
	}
}

func (watcher *Watcher) cancelWaiter(id uint64) {
	watcher.mu.Lock()
	defer watcher.mu.Unlock()
	if entry, ok := watcher.waiters[id]; ok {
		close(entry.channel)
		delete(watcher.waiters, id)
	}
}

func (watcher *Watcher) broadcastLocked(event Event) {
	for id, entry := range watcher.waiters {
		if event.Generation < entry.generation || (event.Generation == entry.generation && event.Revision <= entry.revision) {
			continue
		}
		select {
		case entry.channel <- event:
		default:
		}
		close(entry.channel)
		delete(watcher.waiters, id)
	}
}
