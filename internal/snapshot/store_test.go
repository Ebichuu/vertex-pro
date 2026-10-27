package snapshot

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vertex-pro/vertex-pro/internal/db"
	"github.com/vertex-pro/vertex-pro/internal/syncmerge"
)

func TestStoreSavesAndLoadsAtomicSnapshotReference(t *testing.T) {
	store, database, root := newTestStore(t)
	defer database.Close()
	insertSyncState(t, database, "qb-1")

	value := int64(42)
	want := syncmerge.Snapshot{RID: 7, Torrents: map[string]*syncmerge.Torrent{"hash-1": {ID: "hash-1", Name: "one", Known: syncmerge.FieldName | syncmerge.FieldAmountLeft, AmountLeftState: "KNOWN", AmountLeft: &value}}}
	record, err := store.Save(context.Background(), "qb-1", 1, 1, want, true)
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if record.Digest == "" || record.Path == "" || !record.Complete {
		t.Fatalf("record = %+v", record)
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(record.Path))); err != nil {
		t.Fatalf("snapshot file stat error = %v", err)
	}
	got, loaded, err := store.Load(context.Background(), "qb-1", 1, 1)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.Digest != record.Digest || got.RID != want.RID || got.Torrents["hash-1"].Name != "one" || !got.Torrents["hash-1"].Has(syncmerge.FieldName) {
		t.Fatalf("loaded = %+v/%+v, want %+v/%+v", got, loaded, want, record)
	}
}

func TestStoreRejectsStaleCoordinatesAndMissingBaseline(t *testing.T) {
	store, database, _ := newTestStore(t)
	defer database.Close()
	if _, err := store.Save(context.Background(), "qb-1", 1, 1, syncmerge.Snapshot{Torrents: map[string]*syncmerge.Torrent{}}, true); err == nil || !strings.Contains(err.Error(), "read sync instance state") {
		t.Fatalf("missing state error = %v", err)
	}
	insertSyncState(t, database, "qb-1")
	snapshot := syncmerge.Snapshot{RID: 1, Torrents: map[string]*syncmerge.Torrent{}}
	if _, err := store.Save(context.Background(), "qb-1", 1, 1, snapshot, true); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save(context.Background(), "qb-1", 1, 1, snapshot, true); err == nil || !strings.Contains(err.Error(), "not newer") {
		t.Fatalf("stale save error = %v", err)
	}
}

func TestStoreRejectsCorruptedFileAndMissingReference(t *testing.T) {
	store, database, root := newTestStore(t)
	defer database.Close()
	insertSyncState(t, database, "qb-1")
	record, err := store.Save(context.Background(), "qb-1", 1, 1, syncmerge.Snapshot{RID: 3, Torrents: map[string]*syncmerge.Torrent{}}, true)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, filepath.FromSlash(record.Path))
	if err := os.WriteFile(path, []byte("corrupt"), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Load(context.Background(), "qb-1", 1, 1); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("corrupt load error = %v", err)
	}
	if _, _, err := store.Load(context.Background(), "qb-1", 9, 9); err == nil {
		t.Fatalf("missing reference error = nil")
	}
}

func TestStoreRejectsPathTraversalAndInvalidSnapshot(t *testing.T) {
	store, database, _ := newTestStore(t)
	defer database.Close()
	for _, instanceID := range []string{"", "../escape", "qb/1", "qb 1"} {
		if _, err := store.Save(context.Background(), instanceID, 1, 1, syncmerge.Snapshot{Torrents: map[string]*syncmerge.Torrent{}}, true); err == nil {
			t.Fatalf("instance ID %q accepted", instanceID)
		}
	}
	insertSyncState(t, database, "qb-1")
	if _, err := store.Save(context.Background(), "qb-1", 1, 1, syncmerge.Snapshot{}, true); err == nil {
		t.Fatal("nil torrents accepted")
	}
}

func TestStoreIgnoresCrashWindowOrphans(t *testing.T) {
	store, database, root := newTestStore(t)
	defer database.Close()
	insertSyncState(t, database, "qb-1")
	directory := filepath.Join(root, "qb-1")
	if err := os.MkdirAll(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".tmp-snapshot-interrupted", "1-1-orphan.json"} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(`{"RID":1,"Torrents":{}}`), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := store.Load(context.Background(), "qb-1", 1, 1); err == nil || !strings.Contains(err.Error(), "read snapshot reference") {
		t.Fatalf("orphan load error = %v", err)
	}
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM sync_snapshots WHERE instance_id = 'qb-1'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("snapshot references = %d, want 0", count)
	}
}

func TestStoreRejectsUnsafeDatabasePathAndIncompleteSnapshot(t *testing.T) {
	store, database, _ := newTestStore(t)
	defer database.Close()
	insertSyncState(t, database, "qb-1")
	snapshot := syncmerge.Snapshot{RID: 1, Torrents: map[string]*syncmerge.Torrent{}}
	if _, err := store.Save(context.Background(), "qb-1", 1, 1, snapshot, false); err == nil || !strings.Contains(err.Error(), "complete") {
		t.Fatalf("incomplete save error = %v", err)
	}
	if _, err := store.Save(context.Background(), "qb-1", 1, 1, snapshot, true); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE sync_snapshots SET snapshot_path = '../escape.json' WHERE instance_id = 'qb-1'`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Load(context.Background(), "qb-1", 1, 1); err == nil || !strings.Contains(err.Error(), "invalid snapshot path") {
		t.Fatalf("unsafe path error = %v", err)
	}
}
func newTestStore(t *testing.T) (Store, *sql.DB, string) {
	t.Helper()
	database := openDatabase(t)
	root := filepath.Join(t.TempDir(), "snapshots")
	store, err := New(database, root)
	if err != nil {
		database.Close()
		t.Fatal(err)
	}
	return store, database, root
}

func openDatabase(t *testing.T) *sql.DB {
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
	return database
}

func insertSyncState(t *testing.T, database *sql.DB, instanceID string) {
	t.Helper()
	if _, err := database.Exec(`INSERT INTO sync_instance_state(instance_id, sync_state, sync_generation, rid, snapshot_revision, snapshot_digest, updated_at) VALUES (?, 'RECONCILING', 0, 0, 0, '', '2026-08-25T00:00:00Z')`, instanceID); err != nil {
		t.Fatal(err)
	}
}
