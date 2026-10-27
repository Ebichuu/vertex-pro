package syncwatcher

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
	"github.com/vertex-pro/vertex-pro/internal/snapshot"
	"github.com/vertex-pro/vertex-pro/internal/syncfixture"
	"github.com/vertex-pro/vertex-pro/internal/syncmerge"
)

type fakeFetcher struct {
	mu      sync.Mutex
	deltas  []syncmerge.Delta
	started chan struct{}
	release chan struct{}
	calls   []uint64
}

func (fetcher *fakeFetcher) Fetch(ctx context.Context, rid uint64) (syncmerge.Delta, error) {
	fetcher.mu.Lock()
	fetcher.calls = append(fetcher.calls, rid)
	if fetcher.started != nil {
		closeOnce(fetcher.started)
	}
	release := fetcher.release
	var delta syncmerge.Delta
	if len(fetcher.deltas) > 0 {
		delta = fetcher.deltas[0]
		fetcher.deltas = fetcher.deltas[1:]
	}
	fetcher.mu.Unlock()
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return syncmerge.Delta{}, ctx.Err()
		}
	}
	return delta, nil
}

func TestWatcherBuildsBaselineMergesDeltaAndBroadcastsWaiter(t *testing.T) {
	store, database := newWatcherStore(t)
	defer database.Close()
	insertState(t, database, "qb-1")
	fetcher := &fakeFetcher{deltas: []syncmerge.Delta{
		{RID: 0, FullUpdate: true, Torrents: syncfixture.Deltas(map[string]map[string]any{"hash-1": {"name": "one"}})},
		{RID: 2, Torrents: syncfixture.Deltas(map[string]map[string]any{"hash-1": {"state": "downloading"}})},
		{RID: 0, FullUpdate: true, Torrents: syncfixture.Deltas(map[string]map[string]any{"hash-2": {"name": "reset"}})},
	}}
	watcher, err := New("qb-1", fetcher, store)
	if err != nil {
		t.Fatal(err)
	}
	first, err := watcher.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("baseline error = %v", err)
	}
	if first.Generation != 1 || first.Revision != 1 || first.Snapshot.RID != 0 {
		t.Fatalf("baseline event = %+v", first)
	}
	waiter, err := watcher.RegisterWaiter(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	second, err := watcher.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("delta error = %v", err)
	}
	if second.Generation != 1 || second.Revision != 2 || second.Snapshot.Torrents["hash-1"].Name != "one" {
		t.Fatalf("delta event = %+v", second)
	}
	select {
	case event := <-waiter.Events:
		if event.Revision != 2 || event.Snapshot.Torrents["hash-1"].State != "downloading" {
			t.Fatalf("waiter event = %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("waiter did not receive snapshot event")
	}
	third, err := watcher.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("reset error = %v", err)
	}
	if third.Generation != 2 || third.Revision != 3 || third.Snapshot.Torrents["hash-2"].ID != "hash-2" {
		t.Fatalf("reset event = %+v", third)
	}
	if _, err := watcher.RegisterWaiter(1, 2); err == nil {
		t.Fatal("stale waiter accepted")
	}
	fetcher.mu.Lock()
	calls := append([]uint64(nil), fetcher.calls...)
	fetcher.mu.Unlock()
	if len(calls) != 3 || calls[0] != 0 || calls[1] != 0 || calls[2] != 2 {
		t.Fatalf("fetch RIDs = %v", calls)
	}
}

func TestWatcherRejectsConcurrentRunAndClosesWaiters(t *testing.T) {
	store, database := newWatcherStore(t)
	defer database.Close()
	insertState(t, database, "qb-1")
	started := make(chan struct{})
	fetcher := &fakeFetcher{started: started, release: make(chan struct{}), deltas: []syncmerge.Delta{{RID: 0, FullUpdate: true, Torrents: map[string]syncmerge.TorrentDelta{}}}}
	watcher, err := New("qb-1", fetcher, store)
	if err != nil {
		t.Fatal(err)
	}
	waiter, err := watcher.RegisterWaiter(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := watcher.RunOnce(context.Background())
		result <- err
	}()
	<-started
	if _, err := watcher.RunOnce(context.Background()); err != ErrInFlight {
		t.Fatalf("concurrent run error = %v, want %v", err, ErrInFlight)
	}
	watcher.Close()
	if _, ok := <-waiter.Events; ok {
		t.Fatal("closed watcher waiter channel still open")
	}
	close(fetcher.release)
	if err := <-result; err != nil {
		t.Fatalf("first run error = %v", err)
	}
	if _, err := watcher.RunOnce(context.Background()); err != ErrClosed {
		t.Fatalf("closed run error = %v, want %v", err, ErrClosed)
	}
}

func TestWatcherRestoresPersistedRevisionAfterRestart(t *testing.T) {
	store, database := newWatcherStore(t)
	defer database.Close()
	insertState(t, database, "qb-1")
	firstFetcher := &fakeFetcher{deltas: []syncmerge.Delta{{RID: 0, FullUpdate: true, Torrents: map[string]syncmerge.TorrentDelta{}}}}
	firstWatcher, err := New("qb-1", firstFetcher, store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := firstWatcher.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Snapshots are memory-only: a restart keeps the committed coordinates
	// monotonic and re-baselines from rid=0 as a new generation.
	restartedFetcher := &fakeFetcher{deltas: []syncmerge.Delta{{RID: 1, FullUpdate: true, Torrents: map[string]syncmerge.TorrentDelta{}}}}
	restarted, err := New("qb-1", restartedFetcher, store)
	if err != nil {
		t.Fatal(err)
	}
	event, err := restarted.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("restart run error = %v", err)
	}
	if event.Generation != 2 || event.Revision != 2 || event.Digest == "" {
		t.Fatalf("restart event = %+v", event)
	}
	if len(restartedFetcher.calls) != 1 || restartedFetcher.calls[0] != 0 {
		t.Fatalf("restart fetch RIDs = %v", restartedFetcher.calls)
	}
	if _, record, ok := restarted.Current(); !ok || record.Generation != 2 || record.Revision != 2 || record.Digest != event.Digest || record.Path != "" {
		t.Fatalf("current record = %+v ok=%v", record, ok)
	}
	if _, _, err := store.LoadLatest(context.Background(), "qb-1"); !errors.Is(err, snapshot.ErrNotPersisted) {
		t.Fatalf("LoadLatest of memory-only revision error = %v", err)
	}
}
func TestWatcherIgnoresDuplicateRIDWithoutWritingRevision(t *testing.T) {
	store, database := newWatcherStore(t)
	defer database.Close()
	insertState(t, database, "qb-1")
	fetcher := &fakeFetcher{deltas: []syncmerge.Delta{{RID: 0, FullUpdate: true, Torrents: map[string]syncmerge.TorrentDelta{}}, {RID: 1, Torrents: map[string]syncmerge.TorrentDelta{}}, {RID: 1, Torrents: map[string]syncmerge.TorrentDelta{}}}}
	watcher, err := New("qb-1", fetcher, store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := watcher.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := watcher.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	second, err := watcher.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if second.Revision != 2 || len(second.Snapshot.Torrents) != 0 {
		t.Fatalf("duplicate event = %+v", second)
	}
}

func newWatcherStore(t testing.TB) (snapshot.Store, *sql.DB) {
	t.Helper()
	database, err := db.Open(context.Background(), db.Config{Path: filepath.Join(t.TempDir(), "vertex-pro.db")})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := db.DefaultRunner()
	if err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err := runner.Apply(context.Background(), database); err != nil {
		database.Close()
		t.Fatal(err)
	}
	store, err := snapshot.New(database, filepath.Join(t.TempDir(), "snapshots"))
	if err != nil {
		database.Close()
		t.Fatal(err)
	}
	return store, database
}

func insertState(t testing.TB, database *sql.DB, instanceID string) {
	t.Helper()
	if _, err := database.Exec(`INSERT INTO sync_instance_state(instance_id, sync_state, sync_generation, rid, snapshot_revision, snapshot_digest, updated_at) VALUES (?, 'RECONCILING', 0, 0, 0, '', '2026-08-25T00:00:00Z')`, instanceID); err != nil {
		t.Fatal(err)
	}
}

func closeOnce(channel chan struct{}) {
	select {
	case <-channel:
	default:
		close(channel)
	}
}
