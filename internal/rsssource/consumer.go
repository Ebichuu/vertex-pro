package rsssource

import (
	"context"
	"errors"
	"time"
)

const MaxConsumerAttempts = 5
const ConsumerLifetime = 24 * time.Hour
const ConsumerLease = 2 * time.Minute

type Consumer struct {
	Source   Source
	FeedID   string
	Attempts int
	Deadline time.Time
}

// ConsumerStore keeps discovery and feed-specific consumption separate. Discover
// must atomically persist both records, so a crash cannot lose newly found work.
type ConsumerStore interface {
	Discover(context.Context, string, string, []Entry, bool, time.Time) (Result, error)
	Claim(context.Context, string, string, time.Time) (*Consumer, error)
	Finish(context.Context, Consumer, string, time.Time) error
}

func (dedupe *Dedupe) Consumers() (ConsumerStore, error) {
	store, ok := dedupe.store.(ConsumerStore)
	if !ok {
		return nil, errors.New("durable feed consumer store is required")
	}
	return store, nil
}

func Normalize(siteID string, entry Entry) (Source, error) { return normalize(siteID, entry) }

func ConsumerRetry(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > MaxConsumerAttempts {
		attempt = MaxConsumerAttempts
	}
	return time.Minute * time.Duration(1<<(attempt-1))
}
