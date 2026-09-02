package rsssourcedb

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/vertex-pro/vertex-pro/internal/db"
	"github.com/vertex-pro/vertex-pro/internal/rsssource"
)

func storeDatabase(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.Open(context.Background(), db.Config{Path: filepath.Join(t.TempDir(), "rsssourcedb.db"), MaxOpenConns: 1, MaxIdleConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := db.DefaultRunner()
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Apply(context.Background(), database); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO sites(site_id, name, base_url, enabled, created_at, updated_at) VALUES ('site-1','Site','https://site.example',1,strftime('%Y-%m-%dT%H:%M:%SZ','now'),strftime('%Y-%m-%dT%H:%M:%SZ','now'))`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func TestDedupePersistsAcrossRestarts(t *testing.T) {
	database := storeDatabase(t)
	store, err := New(database)
	if err != nil {
		t.Fatal(err)
	}
	dedupe, err := rsssource.New(store)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	size := int64(1024)
	entries := []rsssource.Entry{
		{GUID: "guid-1", URL: "https://site.example/a.torrent", Title: "A", SizeBytes: &size},
		{GUID: "guid-2", URL: "https://site.example/b.torrent", Title: "B"},
	}
	first, err := dedupe.Apply(ctx, "site-1", entries)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.New) != 2 || first.Duplicates != 0 {
		t.Fatalf("first=%+v", first)
	}

	// A fresh store over the same database must keep deduplication state.
	reopened, err := New(database)
	if err != nil {
		t.Fatal(err)
	}
	reopenedDedupe, err := rsssource.New(reopened)
	if err != nil {
		t.Fatal(err)
	}
	second, err := reopenedDedupe.Apply(ctx, "site-1", entries)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.New) != 0 || len(second.Updated) != 0 || second.Duplicates != 2 {
		t.Fatalf("second=%+v", second)
	}

	// A changed title must surface as an update, not a duplicate.
	changed := []rsssource.Entry{{GUID: "guid-1", URL: "https://site.example/a.torrent", Title: "A v2", SizeBytes: &size}}
	third, err := reopenedDedupe.Apply(ctx, "site-1", changed)
	if err != nil {
		t.Fatal(err)
	}
	if len(third.Updated) != 1 || third.Duplicates != 0 {
		t.Fatalf("third=%+v", third)
	}
	loaded, err := reopened.Load(ctx, "site-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 2 || loaded[0].Title != "A v2" {
		t.Fatalf("loaded=%+v", loaded)
	}
}
