package schedulercycle

import (
	"context"
	"sync"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/rsssource"
)

const rateProgressCapacity = 128
const rateProgressWriteTimeout = time.Second

// Progress is observational: neither the HTTP path nor attempt completion may
// wait for its persistence. Each admitted attempt keeps a slot until its last
// update is written, so saturation cannot discard an end after accepting a start.
// A full reporter drops the new attempt's telemetry, never its actual work.
type rateProgressReporter struct {
	mu      sync.Mutex
	store   rsssource.RateWaitStore
	ctx     context.Context
	cancel  context.CancelFunc
	queue   chan *rateProgressAttempt
	done    chan struct{}
	entries map[*rateProgressAttempt]struct{}
	started bool
	closed  bool
}

type rateProgressAttempt struct {
	reporter *rateProgressReporter
	work     rsssource.Consumer
	admitted bool
	dropped  bool
	finished bool
	queued   bool
	inFlight bool
	pending  bool
	waiting  bool
	elapsed  time.Duration
	at       time.Time
}

func newRateProgressReporter(store rsssource.RateWaitStore) *rateProgressReporter {
	ctx, cancel := context.WithCancel(context.Background())
	return &rateProgressReporter{store: store, ctx: ctx, cancel: cancel, queue: make(chan *rateProgressAttempt, rateProgressCapacity), done: make(chan struct{}), entries: make(map[*rateProgressAttempt]struct{})}
}

func (reporter *rateProgressReporter) attempt(work rsssource.Consumer) *rateProgressAttempt {
	// Only the durable identity and generation are needed; do not retain source
	// URLs, titles or other artifact inputs in the background queue.
	identity := rsssource.Consumer{Source: rsssource.Source{SiteID: work.Source.SiteID, SourceKey: work.Source.SourceKey}, FeedID: work.FeedID, Attempts: work.Attempts}
	return &rateProgressAttempt{reporter: reporter, work: identity}
}

func (attempt *rateProgressAttempt) report(waiting bool, elapsed time.Duration, at time.Time) {
	reporter := attempt.reporter
	reporter.mu.Lock()
	defer reporter.mu.Unlock()
	if reporter.closed || attempt.finished || attempt.dropped {
		return
	}
	if !attempt.admitted {
		if len(reporter.entries) == rateProgressCapacity {
			attempt.dropped = true
			return
		}
		attempt.admitted = true
		reporter.entries[attempt] = struct{}{}
		if !reporter.started {
			reporter.started = true
			go reporter.run()
		}
	}
	// Preserve all completed waits across redirects while coalescing their
	// transient start/end state to the newest observation and its original time.
	attempt.waiting = waiting
	attempt.elapsed += max(0, elapsed)
	attempt.at = at
	attempt.pending = true
	reporter.enqueue(attempt)
}

func (attempt *rateProgressAttempt) finish(at time.Time) {
	reporter := attempt.reporter
	reporter.mu.Lock()
	defer reporter.mu.Unlock()
	if reporter.closed || attempt.finished {
		return
	}
	attempt.finished = true
	if !attempt.admitted {
		return
	}
	if attempt.waiting {
		attempt.waiting = false
		attempt.at = at
		attempt.pending = true
	}
	if attempt.pending {
		reporter.enqueue(attempt)
	} else if !attempt.inFlight {
		delete(reporter.entries, attempt)
	}
}

// enqueue runs with mu held. At most one entry per admitted attempt can be
// queued, and the queue has room for every admitted attempt, including those
// currently being written. Its send therefore never waits on the worker.
func (reporter *rateProgressReporter) enqueue(attempt *rateProgressAttempt) {
	if !attempt.queued && !attempt.inFlight {
		attempt.queued = true
		reporter.queue <- attempt
	}
}

func (reporter *rateProgressReporter) run() {
	defer close(reporter.done)
	for {
		select {
		case <-reporter.ctx.Done():
			return
		case attempt := <-reporter.queue:
			reporter.mu.Lock()
			if reporter.closed {
				reporter.mu.Unlock()
				return
			}
			waiting, elapsed, at := attempt.waiting, attempt.elapsed, attempt.at
			attempt.queued, attempt.pending, attempt.inFlight = false, false, true
			attempt.elapsed = 0
			reporter.mu.Unlock()

			ctx, cancel := context.WithTimeout(reporter.ctx, rateProgressWriteTimeout)
			// Failed observations are not retried: a store may already have applied
			// the additive duration, so retrying could double-count it.
			_ = reporter.store.RecordRateWait(ctx, attempt.work, waiting, elapsed, at)
			cancel()

			reporter.mu.Lock()
			attempt.inFlight = false
			if !reporter.closed {
				if attempt.pending {
					reporter.enqueue(attempt)
				} else if attempt.finished {
					delete(reporter.entries, attempt)
				}
			}
			reporter.mu.Unlock()
		}
	}
}

func (reporter *rateProgressReporter) close() {
	reporter.mu.Lock()
	if !reporter.closed {
		reporter.closed = true
		reporter.cancel()
		reporter.entries = nil
		if !reporter.started {
			close(reporter.done)
		}
	}
	reporter.mu.Unlock()
	<-reporter.done
}
