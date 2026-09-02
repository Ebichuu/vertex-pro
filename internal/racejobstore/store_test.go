package racejobstore

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
	"github.com/vertex-pro/vertex-pro/internal/racejob"
)

func storeDatabase(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.Open(context.Background(), db.Config{Path: filepath.Join(t.TempDir(), "racejobstore.db"), MaxOpenConns: 1, MaxIdleConns: 1})
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
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func TestCreatorDeduplicatesAcrossRestarts(t *testing.T) {
	database := storeDatabase(t)
	store, err := New(database)
	if err != nil {
		t.Fatal(err)
	}
	creator, err := racejob.New(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	input := racejob.Input{
		SourceID: "source-1", IdentityKey: "identity-key",
		ParticipationDeadline: time.Date(2026, 9, 3, 11, 0, 0, 0, time.UTC),
		ObservationDeadline:   time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC),
	}
	first, err := creator.Create(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Created || first.Duplicate {
		t.Fatalf("first=%+v", first)
	}

	// A fresh store instance over the same database must see the persisted
	// job: dedupe survives a restart.
	reopened, err := New(database)
	if err != nil {
		t.Fatal(err)
	}
	reopenedCreator, err := racejob.New(reopened, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := reopenedCreator.Create(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Duplicate || second.Job.ID != first.Job.ID {
		t.Fatalf("second=%+v", second)
	}
	other, err := reopenedCreator.Create(context.Background(), racejob.Input{SourceID: "source-1", IdentityKey: "other-identity", ParticipationDeadline: input.ParticipationDeadline, ObservationDeadline: input.ObservationDeadline})
	if err == nil {
		t.Fatalf("identity conflict not detected: %+v", other)
	}
}

func TestSaveUpdatesMutableStateWithoutRewritingIdentity(t *testing.T) {
	database := storeDatabase(t)
	store, err := New(database)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Date(2026, 9, 3, 11, 0, 0, 0, time.UTC)
	job := racejob.Job{
		ID: "job-1", SourceID: "source-1", IdentityKey: "identity-key",
		JobState: "DISCOVERED", RuleState: "PENDING", TorrentState: "PENDING", PlacementState: "PENDING", ObservationState: "NOT_STARTED",
		ParticipationDeadline: now, ObservationDeadline: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now,
	}
	if err := store.Save(ctx, job); err != nil {
		t.Fatal(err)
	}
	job.JobState = "READY"
	job.RuleState = "MATCHED"
	job.TorrentState = "READY"
	job.PlacementState = "PREPARED"
	job.PreparationRevision = 2
	if err := store.Save(ctx, job); err != nil {
		t.Fatal(err)
	}
	loaded, found, err := store.LoadBySource(ctx, "source-1")
	if err != nil || !found {
		t.Fatalf("found=%t err=%v", found, err)
	}
	if loaded.JobState != "READY" || loaded.RuleState != "MATCHED" || loaded.PreparationRevision != 2 {
		t.Fatalf("loaded=%+v", loaded)
	}
	if loaded.IdentityKey != "identity-key" || loaded.SourceID != "source-1" {
		t.Fatalf("identity rewritten: %+v", loaded)
	}
}
