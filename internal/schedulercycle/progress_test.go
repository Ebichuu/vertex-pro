package schedulercycle

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/matchpipeline"
	"github.com/vertex-pro/vertex-pro/internal/rssfetch"
	"github.com/vertex-pro/vertex-pro/internal/rsssource"
	"github.com/vertex-pro/vertex-pro/internal/rsssourcedb"
	"github.com/vertex-pro/vertex-pro/internal/torrentdownload"
)

type heldProgressStore struct {
	*rsssourcedb.Store
	entered chan struct{}
	release chan struct{}
}

func TestRateWaitDatabaseContentionDoesNotDelayArtifactHTTP(t *testing.T) {
	database := cycleDatabase(t)
	store, _ := rsssourcedb.New(database)
	requested := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/item.torrent" {
			requested <- struct{}{}
		}
		w.Header().Set("Content-Type", "application/x-bittorrent")
		_, _ = w.Write(fixtureTorrent("Linux"))
	}))
	defer server.Close()
	limits, _ := rssfetch.NewLimits(600, 1)
	fetcher, _ := rssfetch.New(server.Client(), 1<<20)
	fetcher = fetcher.WithLimits(limits)
	client := fetcher.ArtifactClient(time.Second)
	downloader, err := torrentdownload.New(torrentdownload.Config{Client: client, AllowedHosts: []string{strings.TrimPrefix(server.URL, "http://")}})
	if err != nil {
		t.Fatal(err)
	}
	dedupe, _ := rsssource.New(store)
	cycle, err := New(fetcher, downloader, dedupe, matchpipeline.Deps{}, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cycle.Close)
	// Hold the only real SQLite connection before the limiter calls its start
	// observer. Progress persistence must wait, while network work remains free.
	conn, err := database.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	waitCount := database.Stats().WaitCount
	response, err := client.Get(server.URL + "/prime")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	work := rsssource.Consumer{Source: rsssource.Source{SiteID: "site-1", SourceKey: "one", URL: server.URL + "/item.torrent"}, FeedID: "feed-1", Attempts: 1, Deadline: time.Now().Add(time.Minute)}
	ctx, cancel := cycle.attemptContext(context.Background(), work)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := cycle.downloadIdentity(ctx, work.Source, FeedRef{})
		done <- err
	}()
	waitForProgress(t, func() bool { return database.Stats().WaitCount > waitCount })
	select {
	case <-requested:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("artifact HTTP waited for an unavailable SQLite progress connection")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

type rateProgressStoreFunc func(context.Context, rsssource.Consumer, bool, time.Duration, time.Time) error

func (fn rateProgressStoreFunc) RecordRateWait(ctx context.Context, work rsssource.Consumer, waiting bool, elapsed time.Duration, at time.Time) error {
	return fn(ctx, work, waiting, elapsed, at)
}

func waitForProgress(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for !ready() {
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal("progress worker did not reach the expected state")
		}
	}
}

func awaitProgressDrain(t *testing.T, reporter *rateProgressReporter) {
	t.Helper()
	waitForProgress(t, func() bool {
		reporter.mu.Lock()
		defer reporter.mu.Unlock()
		return len(reporter.entries) == 0
	})
}

func TestRateProgressSaturationCoalescesWithoutLosingAdmittedEnds(t *testing.T) {
	type observation struct {
		work    rsssource.Consumer
		waiting bool
		elapsed time.Duration
		at      time.Time
	}
	var mu sync.Mutex
	var observations []observation
	var active, maxActive atomic.Int32
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	reporter := newRateProgressReporter(rateProgressStoreFunc(func(ctx context.Context, work rsssource.Consumer, waiting bool, elapsed time.Duration, at time.Time) error {
		current := active.Add(1)
		defer active.Add(-1)
		for old := maxActive.Load(); current > old && !maxActive.CompareAndSwap(old, current); old = maxActive.Load() {
		}
		once.Do(func() { close(entered) })
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
		mu.Lock()
		observations = append(observations, observation{work, waiting, elapsed, at})
		mu.Unlock()
		return nil
	}))
	t.Cleanup(reporter.close)
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	now := time.Now()
	work := func(index int) rsssource.Consumer {
		return rsssource.Consumer{Source: rsssource.Source{SiteID: "site", SourceKey: fmt.Sprint(index)}, FeedID: "feed", Attempts: 1}
	}
	first := reporter.attempt(work(0))
	first.report(true, 0, now)
	<-entered
	first.report(false, 5*time.Millisecond, now.Add(5*time.Millisecond))
	first.finish(now)
	for i := 1; i < rateProgressCapacity; i++ {
		attempt := reporter.attempt(work(i))
		attempt.report(true, 0, now)
		attempt.report(false, 7*time.Millisecond, now.Add(7*time.Millisecond))
		attempt.report(true, 0, now.Add(8*time.Millisecond))
		attempt.report(false, 11*time.Millisecond, now.Add(19*time.Millisecond))
		attempt.finish(now.Add(20 * time.Millisecond))
		// Once complete, a delayed start cannot replace its queued end.
		attempt.report(true, 0, now.Add(time.Second))
	}
	for i := rateProgressCapacity; i < rateProgressCapacity*4; i++ {
		attempt := reporter.attempt(work(i))
		attempt.report(true, 0, now)
		attempt.report(false, time.Hour, now)
		attempt.finish(now)
		if !attempt.dropped {
			t.Fatal("saturated reporter admitted more work")
		}
	}
	reporter.mu.Lock()
	entries, queued := len(reporter.entries), len(reporter.queue)
	reporter.mu.Unlock()
	if entries != rateProgressCapacity || queued != rateProgressCapacity-1 {
		t.Fatalf("unbounded progress storage: entries=%d queued=%d", entries, queued)
	}
	releaseOnce.Do(func() { close(release) })
	awaitProgressDrain(t, reporter)
	mu.Lock()
	defer mu.Unlock()
	if maxActive.Load() != 1 || len(observations) != rateProgressCapacity+1 {
		t.Fatalf("progress was not serialized/coalesced: active=%d writes=%d", maxActive.Load(), len(observations))
	}
	totals, last := map[string]time.Duration{}, map[string]observation{}
	for _, event := range observations {
		totals[event.work.Source.SourceKey] += event.elapsed
		last[event.work.Source.SourceKey] = event
	}
	for i := 0; i < rateProgressCapacity; i++ {
		key, want := fmt.Sprint(i), 18*time.Millisecond
		if i == 0 {
			want = 5 * time.Millisecond
		}
		if totals[key] != want || last[key].waiting {
			t.Fatalf("lost or duplicated progress for %s: elapsed=%s last=%+v", key, totals[key], last[key])
		}
	}
}

func TestRateProgressLateWritesPreserveFinishedStateAndAttemptGeneration(t *testing.T) {
	for _, mode := range []string{"DONE", "FAILED", "RETRY", "NEW_ATTEMPT"} {
		t.Run(mode, func(t *testing.T) {
			database := cycleDatabase(t)
			store, _ := rsssourcedb.New(database)
			ctx, now := context.Background(), time.Now()
			if _, err := store.Discover(ctx, "site-1", "feed-1", []rsssource.Entry{{GUID: "one", URL: "https://site.example/file"}}, false, now); err != nil {
				t.Fatal(err)
			}
			work, err := store.Claim(ctx, "site-1", "feed-1", now)
			if err != nil || work == nil {
				t.Fatal("claim failed", err)
			}
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			reporter := newRateProgressReporter(rateProgressStoreFunc(func(ctx context.Context, work rsssource.Consumer, waiting bool, elapsed time.Duration, at time.Time) error {
				once.Do(func() { close(entered) })
				select {
				case <-release:
				case <-ctx.Done():
					return ctx.Err()
				}
				if err := store.RecordRateWait(ctx, work, waiting, elapsed, at); err != nil {
					return err
				}
				var state string
				var started sql.NullString
				if err := database.QueryRow(`SELECT state,rate_wait_started_at FROM rss_source_consumers`).Scan(&state, &started); err != nil {
					return err
				}
				if mode != "NEW_ATTEMPT" && started.Valid {
					t.Errorf("late progress revived %s as waiting", state)
				}
				return nil
			}))
			t.Cleanup(reporter.close)
			attempt := reporter.attempt(*work)
			attempt.report(true, 0, now)
			<-entered
			attempt.report(false, 17*time.Millisecond, now.Add(17*time.Millisecond))
			attempt.finish(now.Add(time.Second))
			code, wantState := "", "DONE"
			if mode != "DONE" {
				code, wantState = "RSS_ITEM_PROCESSING_FAILED", "PENDING"
			}
			if mode == "FAILED" {
				work.Deadline = now
				wantState = "FAILED"
			}
			if err := store.Finish(ctx, *work, code, now); err != nil {
				t.Fatal(err)
			}
			if mode == "NEW_ATTEMPT" {
				next, err := store.Claim(ctx, "site-1", "feed-1", now.Add(rsssource.ConsumerRetry(work.Attempts)))
				if err != nil || next == nil || next.Attempts != work.Attempts+1 {
					t.Fatal("reclaim failed", err)
				}
				if err := store.RecordRateWait(ctx, *next, true, 0, now.Add(time.Minute)); err != nil {
					t.Fatal(err)
				}
			}
			close(release)
			awaitProgressDrain(t, reporter)
			var state string
			var started sql.NullString
			var elapsed int64
			if err := database.QueryRow(`SELECT state,rate_wait_started_at,rate_wait_ms FROM rss_source_consumers`).Scan(&state, &started, &elapsed); err != nil {
				t.Fatal(err)
			}
			wantElapsed := int64(17)
			if mode == "NEW_ATTEMPT" {
				wantElapsed = 0
			}
			if state != wantState || started.Valid != (mode == "NEW_ATTEMPT") || elapsed != wantElapsed {
				t.Fatalf("late progress changed finished/current attempt: state=%s started=%v elapsed=%d", state, started, elapsed)
			}
		})
	}
}

func TestCycleCloseCancelsAndWaitsForProgressWorker(t *testing.T) {
	entered, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	var calls atomic.Int32
	reporter := newRateProgressReporter(rateProgressStoreFunc(func(ctx context.Context, _ rsssource.Consumer, _ bool, _ time.Duration, _ time.Time) error {
		calls.Add(1)
		close(entered)
		<-ctx.Done()
		close(canceled)
		<-release
		return ctx.Err()
	}))
	cycle := &Cycle{progress: reporter}
	attempt := reporter.attempt(rsssource.Consumer{})
	attempt.report(true, 0, time.Now())
	<-entered
	done := make(chan struct{})
	var closers sync.WaitGroup
	for range 8 {
		closers.Go(cycle.Close)
	}
	go func() { closers.Wait(); close(done) }()
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("Close did not cancel the in-flight store call")
	}
	select {
	case <-done:
		t.Fatal("Close returned before the progress worker exited")
	default:
	}
	attempt.report(false, time.Second, time.Now())
	attempt.finish(time.Now())
	releaseOnce.Do(func() { close(release) })
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Close leaked its progress worker")
	}
	cycle.Close()
	reporter.attempt(rsssource.Consumer{}).report(true, 0, time.Now())
	if calls.Load() != 1 {
		t.Fatal("closed reporter accepted more writes")
	}
	select {
	case <-reporter.done:
	default:
		t.Fatal("progress goroutine survived Close")
	}
	// A cycle that never observed a wait owns no background goroutine.
	empty := newRateProgressReporter(rateProgressStoreFunc(func(context.Context, rsssource.Consumer, bool, time.Duration, time.Time) error {
		t.Error("unused reporter wrote progress")
		return errors.New("unexpected")
	}))
	(&Cycle{progress: empty}).Close()
	empty.close()
	if empty.started {
		t.Fatal("unused reporter started a worker")
	}
}

func (store *heldProgressStore) RecordRateWait(ctx context.Context, work rsssource.Consumer, waiting bool, elapsed time.Duration, now time.Time) error {
	if !waiting {
		close(store.entered)
		select {
		case <-store.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return store.Store.RecordRateWait(ctx, work, waiting, elapsed, now)
}

// Exercise the actual artifact limiter and HTTP transport: its end observer
// runs after the rate permit has been consumed, before RoundTrip starts.
func TestRateWaitProgressDoesNotDelayGrantedArtifactRequest(t *testing.T) {
	database := cycleDatabase(t)
	store, err := rsssourcedb.New(database)
	if err != nil {
		t.Fatal(err)
	}
	held := &heldProgressStore{Store: store, entered: make(chan struct{}), release: make(chan struct{})}
	defer close(held.release)
	requested := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/item.torrent" {
			requested <- struct{}{}
		}
		w.Header().Set("Content-Type", "application/x-bittorrent")
		_, _ = w.Write(fixtureTorrent("Linux"))
	}))
	defer server.Close()
	limits, _ := rssfetch.NewLimits(600, 1)
	fetcher, _ := rssfetch.New(server.Client(), 1<<20)
	fetcher = fetcher.WithLimits(limits)
	client := fetcher.ArtifactClient(time.Second)
	downloader, err := torrentdownload.New(torrentdownload.Config{Client: client, AllowedHosts: []string{strings.TrimPrefix(server.URL, "http://")}})
	if err != nil {
		t.Fatal(err)
	}
	dedupe, _ := rsssource.New(held)
	cycle, err := New(fetcher, downloader, dedupe, matchpipeline.Deps{}, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cycle.Close)
	work := rsssource.Consumer{Source: rsssource.Source{SiteID: "site-1", SourceKey: "one", URL: server.URL + "/item.torrent"}, FeedID: "feed-1", Attempts: 1, Deadline: time.Now().Add(time.Minute)}
	ctx, cancel := cycle.attemptContext(context.Background(), work)
	defer cancel()
	response, err := client.Get(server.URL + "/prime")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	done := make(chan error, 1)
	go func() {
		_, err := cycle.downloadIdentity(ctx, work.Source, FeedRef{})
		done <- err
	}()
	select {
	case <-held.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("artifact did not reach its post-permit observer")
	}
	select {
	case <-requested:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("granted artifact HTTP request was blocked by progress persistence")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
