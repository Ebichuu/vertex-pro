package snapshot

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/syncmerge"
)

func TestSnapshotWaitsForWriterAndRechecksCoordinates(t *testing.T) {
	for _, advance := range []bool{false, true} {
		t.Run(fmt.Sprintf("advance=%t", advance), func(t *testing.T) {
			store, database, root := newTestStore(t)
			defer database.Close()
			insertSyncState(t, database, "qb-1")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			writer, err := database.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer writer.Close()
			if _, err := writer.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
				t.Fatal(err)
			}
			defer writer.ExecContext(context.Background(), "ROLLBACK")
			if advance {
				// This newer coordinate is invisible until the held writer commits.
				if _, err := writer.ExecContext(ctx, `UPDATE sync_instance_state SET sync_generation=1,snapshot_revision=2 WHERE instance_id='qb-1'`); err != nil {
					t.Fatal(err)
				}
			}
			result := make(chan error, 1)
			done := make(chan struct{})
			go func() {
				defer close(done)
				_, err := store.Save(ctx, "qb-1", 1, 1, syncmerge.Snapshot{RID: 1, Torrents: map[string]syncmerge.Torrent{}}, true)
				result <- err
			}()
			// Drain the save before fixture cleanup, including assertion failures.
			t.Cleanup(func() { <-done })
			ticker := time.NewTicker(time.Millisecond)
			defer ticker.Stop()
			for {
				files, err := filepath.Glob(filepath.Join(root, "qb-1", "1-1-*.json"))
				if err != nil {
					t.Fatal(err)
				}
				if len(files) == 1 {
					break // Durable file preparation must not need the writer lock.
				}
				select {
				case err := <-result:
					t.Fatalf("save completed before writer release: %v", err)
				case <-ctx.Done():
					t.Fatal("snapshot was not prepared while the writer was held")
				case <-ticker.C:
				}
			}
			select {
			case err := <-result:
				t.Fatalf("save failed instead of waiting for the writer: %v", err)
			case <-time.After(50 * time.Millisecond):
			}
			var count int
			if err := database.QueryRowContext(ctx, `SELECT count(*) FROM sync_snapshots`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("reference published before writer release: count=%d err=%v", count, err)
			}
			if _, err := writer.ExecContext(ctx, "COMMIT"); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-result:
				if advance {
					if err == nil || !strings.Contains(err.Error(), "not newer") {
						t.Fatalf("save did not recheck the committed coordinates: %v", err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("save did not finish after writer release")
			}
			if advance {
				var revision uint64
				if err := database.QueryRowContext(ctx, `SELECT snapshot_revision,(SELECT count(*) FROM sync_snapshots) FROM sync_instance_state WHERE instance_id='qb-1'`).Scan(&revision, &count); err != nil || revision != 2 || count != 0 {
					t.Fatalf("stale reference accepted: revision=%d count=%d err=%v", revision, count, err)
				}
			} else {
				loaded, record, err := store.LoadLatest(ctx, "qb-1")
				if err != nil || record.Revision != 1 || loaded.RID != 1 {
					t.Fatalf("committed snapshot mismatch: %+v %+v %v", loaded, record, err)
				}
			}
		})
	}
}

func TestSnapshotsPersistAcrossConcurrentInstances(t *testing.T) {
	store, database, _ := newTestStore(t)
	defer database.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ids := []string{"qb-1", "qb-2", "qb-3"}
	for _, id := range ids {
		insertSyncState(t, database, id)
	}
	const rounds = 20
	for revision := uint64(1); revision <= rounds; revision++ {
		start := make(chan struct{})
		results := make(chan error, len(ids))
		var workers sync.WaitGroup
		for _, id := range ids {
			workers.Go(func() {
				<-start
				_, err := store.Save(ctx, id, 1, revision, syncmerge.Snapshot{RID: revision, Torrents: map[string]syncmerge.Torrent{}}, true)
				results <- err
			})
		}
		close(start)
		workers.Wait()
		close(results)
		var failures []error
		for err := range results {
			failures = append(failures, err)
		}
		if err := errors.Join(failures...); err != nil {
			t.Fatalf("concurrent snapshot round %d: %v", revision, err)
		}
	}
	for _, id := range ids {
		loaded, record, err := store.LoadLatest(ctx, id)
		if err != nil || record.Generation != 1 || record.Revision != rounds || loaded.RID != rounds {
			t.Fatalf("latest snapshot %s: %+v %+v %v", id, loaded, record, err)
		}
	}
}
