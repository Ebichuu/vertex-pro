package racejob

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type fakeClock struct{ now time.Time }

func (clock fakeClock) Now() time.Time { return clock.now }

func TestCreatorPersistsArtifactWithoutRetainingItInResults(t *testing.T) {
	clock := fakeClock{now: time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)}
	store := &MemoryStore{}
	creator, _ := New(store, clock)
	input := Input{SourceID: "artifact", IdentityKey: "hash", ParticipationDeadline: clock.now.Add(time.Hour), ObservationDeadline: clock.now.Add(2 * time.Hour), TorrentBody: []byte("original")}
	first, err := creator.Create(context.Background(), input)
	if err != nil || !first.Created || len(first.Job.TorrentBody) != 0 {
		t.Fatalf("creation: created=%v retained bytes=%d err=%v", first.Created, len(first.Job.TorrentBody), err)
	}
	if string(store.jobs[input.SourceID].TorrentBody) != "original" {
		t.Fatal("artifact was not passed to storage")
	}
	input.TorrentBody[0] = 'x'
	if string(store.jobs[input.SourceID].TorrentBody) != "original" {
		t.Fatal("stored artifact aliases caller input")
	}
	duplicate, err := creator.Create(context.Background(), input)
	if err != nil || !duplicate.Duplicate || len(duplicate.Job.TorrentBody) != 0 {
		t.Fatalf("duplicate: duplicate=%v retained bytes=%d err=%v", duplicate.Duplicate, len(duplicate.Job.TorrentBody), err)
	}
	loaded, found, err := store.LoadBySource(context.Background(), input.SourceID)
	if err != nil || !found || len(loaded.TorrentBody) != 0 {
		t.Fatal("metadata lookup retains artifact")
	}
}

func TestCreatorCreatesInitialDiscoveredJob(t *testing.T) {
	clock := fakeClock{now: time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)}
	creator, err := New(&MemoryStore{}, clock)
	if err != nil {
		t.Fatal(err)
	}
	result, err := creator.Create(context.Background(), Input{SourceID: "source-a", IdentityKey: "v1:abc", ParticipationDeadline: clock.now.Add(time.Hour), ObservationDeadline: clock.now.Add(2 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	job := result.Job
	if !result.Created || result.Duplicate || job.JobState != "DISCOVERED" || job.RuleState != "PENDING" || job.TorrentState != "PENDING" || job.PlacementState != "PENDING" || job.ObservationState != "NOT_STARTED" || job.PreparationRevision != 0 {
		t.Fatalf("result=%+v", result)
	}
	if !job.CreatedAt.Equal(clock.now) || !job.UpdatedAt.Equal(clock.now) {
		t.Fatalf("job=%+v", job)
	}
}

func TestCreatorIsIdempotentBySourceAndRejectsRebind(t *testing.T) {
	clock := fakeClock{now: time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)}
	creator, err := New(&MemoryStore{}, clock)
	if err != nil {
		t.Fatal(err)
	}
	input := Input{SourceID: "source-a", IdentityKey: "v1:abc", ParticipationDeadline: clock.now.Add(time.Hour), ObservationDeadline: clock.now.Add(2 * time.Hour)}
	first, err := creator.Create(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := creator.Create(context.Background(), input)
	if err != nil || !replay.Duplicate || replay.Created || replay.Job.ID != first.Job.ID {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	input.IdentityKey = "v1:def"
	if _, err := creator.Create(context.Background(), input); err == nil {
		t.Fatal("expected source rebind rejection")
	}
}

func TestCreatorRejectsInvalidDeadlinesAndStoreErrors(t *testing.T) {
	clock := fakeClock{now: time.Now().UTC()}
	creator, err := New(nil, clock)
	if err == nil {
		t.Fatal("expected store error")
	}
	creator, err = New(&failingStore{loadErr: errors.New("load")}, clock)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := creator.Create(context.Background(), Input{}); err == nil {
		t.Fatal("expected input error")
	}
	if _, err := creator.Create(context.Background(), Input{SourceID: "source-a", ParticipationDeadline: clock.now.Add(time.Hour), ObservationDeadline: clock.now}); err == nil {
		t.Fatal("expected deadline order error")
	}
	creator, err = New(&failingStore{saveErr: errors.New("save")}, clock)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := creator.Create(context.Background(), Input{SourceID: "source-a", ParticipationDeadline: clock.now.Add(time.Hour), ObservationDeadline: clock.now.Add(2 * time.Hour)}); err == nil {
		t.Fatal("expected save error")
	}
}

func TestCreatorConcurrentDuplicateSourceCreatesOneJob(t *testing.T) {
	clock := fakeClock{now: time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)}
	creator, err := New(&MemoryStore{}, clock)
	if err != nil {
		t.Fatal(err)
	}
	input := Input{SourceID: "source-a", IdentityKey: "v1:abc", ParticipationDeadline: clock.now.Add(time.Hour), ObservationDeadline: clock.now.Add(2 * time.Hour)}
	results := make([]Result, 12)
	errs := make([]error, 12)
	var wait sync.WaitGroup
	for index := range results {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			results[index], errs[index] = creator.Create(context.Background(), input)
		}(index)
	}
	wait.Wait()
	created := 0
	for index := range results {
		if errs[index] != nil {
			t.Fatal(errs[index])
		}
		if results[index].Created {
			created++
		}
		if results[index].Job.ID != results[0].Job.ID {
			t.Fatalf("index=%d result=%+v", index, results[index])
		}
	}
	if created != 1 {
		t.Fatalf("created=%d results=%+v", created, results)
	}
}

type failingStore struct{ loadErr, saveErr error }

func (store *failingStore) LoadBySource(context.Context, string) (Job, bool, error) {
	return Job{}, false, store.loadErr
}
func (store *failingStore) Save(context.Context, Job) error { return store.saveErr }
