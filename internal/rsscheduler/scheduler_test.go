package rsscheduler

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"testing"
	"time"
)

type fakeClock struct{ now time.Time }

func (clock *fakeClock) Now() time.Time                 { return clock.now }
func (clock *fakeClock) Advance(duration time.Duration) { clock.now = clock.now.Add(duration) }

func testScheduler(t *testing.T, clock *fakeClock, store Store) *Scheduler {
	t.Helper()
	scheduler, err := New(Config{
		BaseInterval: time.Minute,
		MaxInterval:  4 * time.Minute,
		JitterRatio:  0.25,
		Clock:        clock,
		Jitter: func(base time.Duration, ratio float64) time.Duration {
			if ratio != 0.25 {
				t.Fatalf("ratio=%v", ratio)
			}
			return base
		},
	}, store)
	if err != nil {
		t.Fatal(err)
	}
	return scheduler
}

func TestSchedulerUsesFakeClockAndPersistsRestartState(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)}
	store := &MemoryStore{}
	scheduler := testScheduler(t, clock, store)
	if err := scheduler.Register(context.Background(), "feed-a", true); err != nil {
		t.Fatal(err)
	}
	calls := 0
	results, err := scheduler.Tick(context.Background(), func(context.Context, Job) error { calls++; return nil })
	if err != nil || calls != 1 || len(results) != 1 {
		t.Fatalf("results=%+v calls=%d err=%v", results, calls, err)
	}
	clock.Advance(59 * time.Second)
	results, err = scheduler.Tick(context.Background(), func(context.Context, Job) error { calls++; return nil })
	if err != nil || len(results) != 0 || calls != 1 {
		t.Fatalf("early results=%+v calls=%d err=%v", results, calls, err)
	}
	clock.Advance(time.Second)
	results, err = scheduler.Tick(context.Background(), func(context.Context, Job) error { calls++; return nil })
	if err != nil || len(results) != 1 || calls != 2 {
		t.Fatalf("due results=%+v calls=%d err=%v", results, calls, err)
	}

	restarted := testScheduler(t, clock, store)
	if err := restarted.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	jobs := restarted.Jobs()
	if len(jobs) != 1 || jobs[0].ConsecutiveFailures != 0 {
		t.Fatalf("jobs=%+v", jobs)
	}
	if !jobs[0].NextRunAt.Equal(clock.Now().Add(time.Minute)) {
		t.Fatalf("next=%v", jobs[0].NextRunAt)
	}
}

func TestSchedulerUsesExponentialBackoffAndCaps(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)}
	scheduler := testScheduler(t, clock, &MemoryStore{})
	if err := scheduler.Register(context.Background(), "feed-a", true); err != nil {
		t.Fatal(err)
	}
	for failure, want := range []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute} {
		results, err := scheduler.Tick(context.Background(), func(context.Context, Job) error { return errors.New("boom") })
		if err != nil || len(results) != 1 {
			t.Fatalf("failure=%d results=%+v err=%v", failure, results, err)
		}
		job := scheduler.Jobs()[0]
		if job.ConsecutiveFailures != failure+1 || !job.NextRunAt.Equal(clock.Now().Add(want)) {
			t.Fatalf("job=%+v want=%v", job, want)
		}
		clock.Advance(want)
	}
}

func TestSchedulerDisablesJobAndDoesNotRunIt(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)}
	store := &MemoryStore{}
	scheduler := testScheduler(t, clock, store)
	if err := scheduler.Register(context.Background(), "feed-a", true); err != nil {
		t.Fatal(err)
	}
	if err := scheduler.Disable(context.Background(), "feed-a"); err != nil {
		t.Fatal(err)
	}
	results, err := scheduler.Tick(context.Background(), func(context.Context, Job) error { t.Fatal("disabled job ran"); return nil })
	if err != nil || len(results) != 0 {
		t.Fatalf("results=%+v err=%v", results, err)
	}
}

func TestSchedulerDoesNotOverlapRunsForSameJob(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)}
	scheduler := testScheduler(t, clock, &MemoryStore{})
	if err := scheduler.Register(context.Background(), "feed-a", true); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		_, err := scheduler.Tick(context.Background(), func(context.Context, Job) error {
			close(started)
			<-release
			return nil
		})
		firstDone <- err
	}()
	<-started
	second, err := scheduler.Tick(context.Background(), func(context.Context, Job) error { t.Fatal("overlapping job ran"); return nil })
	if err != nil || len(second) != 0 {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
}
func TestSchedulerRejectsInvalidConfigAndCallback(t *testing.T) {
	clock := &fakeClock{now: time.Now()}
	if _, err := New(Config{BaseInterval: 0, MaxInterval: time.Minute, Clock: clock}, &MemoryStore{}); err == nil {
		t.Fatal("expected interval error")
	}
	if _, err := New(Config{BaseInterval: time.Minute, MaxInterval: time.Minute, JitterRatio: 2, Clock: clock}, &MemoryStore{}); err == nil {
		t.Fatal("expected jitter error")
	}
	scheduler := testScheduler(t, clock, &MemoryStore{})
	if _, err := scheduler.Tick(context.Background(), nil); err == nil {
		t.Fatal("expected callback error")
	}
}

func TestMemoryStoreReturnsIndependentCopies(t *testing.T) {
	store := &MemoryStore{}
	jobs := []Job{{ID: "feed-a", Enabled: true, NextRunAt: time.Now()}}
	if err := store.Save(context.Background(), jobs); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	loaded[0].ID = "changed"
	again, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(again, jobs) {
		t.Fatalf("again=%+v jobs=%+v", again, jobs)
	}
}

func TestJobsOrderIsNotContracted(t *testing.T) {
	clock := &fakeClock{now: time.Now()}
	scheduler := testScheduler(t, clock, &MemoryStore{})
	for _, id := range []string{"feed-b", "feed-a"} {
		if err := scheduler.Register(context.Background(), id, true); err != nil {
			t.Fatal(err)
		}
	}
	jobs := scheduler.Jobs()
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].ID < jobs[j].ID })
	if got := []string{jobs[0].ID, jobs[1].ID}; !reflect.DeepEqual(got, []string{"feed-a", "feed-b"}) {
		t.Fatal(got)
	}
}
