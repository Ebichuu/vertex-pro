package rssfetch

import (
	"context"
	"strings"
	"sync"
	"time"
)

type artifactAdmissionKey struct{}

// WithNonblockingArtifacts lets a durable preparation worker yield instead of
// sleeping inside the transport. RSS fetches and callers without a durable
// continuation keep the blocking limiter. Every redirect uses the same policy.
func WithNonblockingArtifacts(ctx context.Context) context.Context {
	return context.WithValue(ctx, artifactAdmissionKey{}, true)
}

func NonblockingArtifacts(ctx context.Context) bool {
	value, _ := ctx.Value(artifactAdmissionKey{}).(bool)
	return value
}

// ArtifactDeferred contains only a local retry time, never a request URL or
// credentials. No request budget or HTTP slot was consumed by this decision.
type ArtifactDeferred struct{ RetryAt time.Time }

func (*ArtifactDeferred) Error() string { return "artifact request permission unavailable" }

const artifactSlotRetry = 250 * time.Millisecond

func (limits *Limits) tryArtifact(ctx context.Context, host string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	limits.mu.Lock()
	now := time.Now()
	for key, b := range limits.hosts {
		if b.waiting == 0 && b.preparing == 0 && now.Sub(b.updated) > time.Minute {
			delete(limits.hosts, key)
		}
	}
	b := limits.hosts[host]
	if b == nil {
		b = &hostBudget{tokens: float64(limits.burst), updated: now, changed: make(chan struct{})}
		limits.hosts[host] = b
	}
	delay := limits.delay(b, now, false)
	if b.preparing >= limits.preparationsPerHost {
		delay = max(delay, artifactSlotRetry)
	}
	if delay > 0 {
		limits.mu.Unlock()
		return nil, &ArtifactDeferred{RetryAt: now.Add(delay)}
	}
	b.tokens--
	b.requests = append(b.requests, now)
	b.preparing++
	limits.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			limits.mu.Lock()
			b.preparing--
			close(b.changed)
			b.changed = make(chan struct{})
			limits.mu.Unlock()
		})
	}, nil
}
