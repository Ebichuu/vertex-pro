package racejobstore

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/racejob"
)

func TestJobArtifactAtomicImmutableAndVerified(t *testing.T) {
	ctx := context.Background()
	database := storeDatabase(t)
	store, _ := New(database)
	creator, _ := racejob.New(store, nil)
	input := racejob.Input{SourceID: "s", IdentityKey: "hash", ParticipationDeadline: time.Now().Add(time.Hour), ObservationDeadline: time.Now().Add(2 * time.Hour), TorrentBody: []byte("original torrent")}
	if _, err := database.Exec(`CREATE TRIGGER fail_artifact BEFORE INSERT ON job_torrent_artifacts BEGIN SELECT RAISE(ABORT,'injected'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := creator.Create(ctx, input); err == nil {
		t.Fatal("expected atomic save failure")
	}
	var count int
	if err := database.QueryRow(`SELECT count(*) FROM race_jobs`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial job: %d %v", count, err)
	}
	if _, err := database.Exec(`DROP TRIGGER fail_artifact`); err != nil {
		t.Fatal(err)
	}
	result, err := creator.Create(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Job.TorrentBody) != 0 {
		t.Fatal("creation outcome retains artifact body")
	}
	duplicate, err := creator.Create(ctx, input)
	if err != nil || !duplicate.Duplicate || len(duplicate.Job.TorrentBody) != 0 {
		t.Fatalf("duplicate outcome: duplicate=%v body bytes=%d err=%v", duplicate.Duplicate, len(duplicate.Job.TorrentBody), err)
	}
	body, found, err := LoadArtifact(ctx, database, result.Job.ID)
	if err != nil || !found || !bytes.Equal(body, input.TorrentBody) {
		t.Fatalf("artifact: found=%v err=%v", found, err)
	}
	if err := SaveLegacyArtifact(ctx, database, result.Job.ID, []byte("replacement"), time.Now()); err == nil {
		t.Fatal("artifact replaced")
	}
	body, _, err = LoadArtifact(ctx, database, result.Job.ID)
	if err != nil || !bytes.Equal(body, input.TorrentBody) {
		t.Fatal("failed replacement changed artifact")
	}
	if _, err := database.Exec(`UPDATE job_torrent_artifacts SET body=? WHERE job_id=?`, []byte("corrupt"), result.Job.ID); err != nil {
		t.Fatal(err)
	}
	if _, found, err := LoadArtifact(ctx, database, result.Job.ID); err == nil || !found {
		t.Fatal("corruption not detected")
	}
}
