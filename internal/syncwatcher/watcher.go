package syncwatcher

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
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
	Snapshot   syncmerge.Snapshot
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
	current    syncmerge.Snapshot
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
	generation := watcher.generation
	watcher.mu.Unlock()
	defer func() {
		watcher.mu.Lock()
		watcher.inFlight = false
		watcher.mu.Unlock()
	}()

	if !hasCurrent {
		loaded, record, err := watcher.store.LoadLatest(ctx, watcher.instanceID)
		if err == nil {
			current, generation = loaded, record.Generation
			watcher.revision = record.Revision
			hasCurrent = true
		} else if !errors.Is(err, sql.ErrNoRows) && !containsNoReference(err) {
			return Event{}, fmt.Errorf("load current snapshot: %w", err)
		}
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
	if result.Ignored {
		return Event{InstanceID: watcher.instanceID, Generation: generation, Revision: watcher.revision, Snapshot: result.Snapshot}, nil
	}
	if generation == 0 || result.Reset {
		generation++
	}
	revision := watcher.revision + 1
	if hasCurrent && generation == watcher.generation && watcher.revision == 0 {
		revision = 1
	}
	if _, err := watcher.store.Save(ctx, watcher.instanceID, generation, revision, result.Snapshot, true); err != nil {
		return Event{}, fmt.Errorf("persist sync snapshot: %w", err)
	}
	event := Event{InstanceID: watcher.instanceID, Generation: generation, Revision: revision, Snapshot: result.Snapshot}
	watcher.mu.Lock()
	watcher.current, watcher.generation, watcher.revision, watcher.hasCurrent = result.Snapshot, generation, revision, true
	watcher.broadcastLocked(event)
	watcher.mu.Unlock()
	return event, nil
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

func containsNoReference(err error) bool {
	return err != nil && (errors.Is(err, sql.ErrNoRows) || strings.Contains(err.Error(), "read latest snapshot coordinates") || strings.Contains(err.Error(), "read snapshot reference"))
}
