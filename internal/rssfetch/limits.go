package rssfetch

import (
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Limits shares a strict rolling-minute host budget between RSS and torrent
// requests. A token bucket bounds short bursts. One request per minute is
// reserved for RSS when the budget is greater than one.
type Limits struct {
	interval         time.Duration
	perMinute, burst int
	slots            chan struct{}
	mu               sync.Mutex
	hosts            map[string]*hostBudget
}
type hostBudget struct {
	tokens              float64
	updated             time.Time
	requests            []time.Time
	rssWaiting, waiting int
	changed             chan struct{}
}

func NewLimits(perMinute, concurrent int) (*Limits, error) {
	return NewLimitsWithBurst(perMinute, concurrent, 1)
}
func NewLimitsWithBurst(perMinute, concurrent, burst int) (*Limits, error) {
	if perMinute < 1 || perMinute > 600 || concurrent < 1 || concurrent > 32 || burst < 1 || burst > 16 {
		return nil, errors.New("invalid RSS limits")
	}
	return &Limits{interval: time.Minute / time.Duration(perMinute), perMinute: perMinute, burst: min(burst, perMinute), slots: make(chan struct{}, concurrent), hosts: map[string]*hostBudget{}}, nil
}
func (limits *Limits) acquire(ctx context.Context, host string) (func(), error) {
	select {
	case limits.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	release := func() { <-limits.slots }
	if err := limits.acquireBudget(ctx, host, true); err != nil {
		release()
		return nil, err
	}
	return release, nil
}
func (limits *Limits) acquireRate(ctx context.Context, host string) error {
	return limits.acquireBudget(ctx, host, false)
}

// delay mutates only expired history/refill state. Call with limits.mu held.
func (limits *Limits) delay(b *hostBudget, now time.Time, rss bool) time.Duration {
	b.tokens = min(float64(limits.burst), b.tokens+max(0, float64(now.Sub(b.updated))/float64(limits.interval)))
	b.updated = now
	for len(b.requests) > 0 && !b.requests[0].Add(time.Minute).After(now) {
		b.requests = b.requests[1:]
	}
	budget := limits.perMinute
	if !rss && budget > 1 {
		budget--
	}
	var wait time.Duration
	if len(b.requests) >= budget {
		wait = b.requests[len(b.requests)-budget].Add(time.Minute).Sub(now)
	}
	if b.tokens < 1 {
		wait = max(wait, time.Duration(math.Ceil((1-b.tokens)*float64(limits.interval))))
	}
	if !rss && b.rssWaiting > 0 {
		wait = max(wait, limits.interval)
	}
	return wait
}
func (limits *Limits) acquireBudget(ctx context.Context, host string, rss bool) error {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	limits.mu.Lock()
	now := time.Now()
	for key, b := range limits.hosts {
		if b.waiting == 0 && now.Sub(b.updated) > time.Minute {
			delete(limits.hosts, key)
		}
	}
	b := limits.hosts[host]
	if b == nil {
		b = &hostBudget{tokens: float64(limits.burst), updated: now, changed: make(chan struct{})}
		limits.hosts[host] = b
	}
	b.waiting++
	if rss {
		b.rssWaiting++
	}
	limits.mu.Unlock()
	defer func() {
		limits.mu.Lock()
		b.waiting--
		if rss {
			b.rssWaiting--
			close(b.changed)
			b.changed = make(chan struct{})
		}
		limits.mu.Unlock()
	}()
	var started time.Time
	defer func() {
		if !started.IsZero() {
			reportWait(ctx, false, time.Since(started))
		}
	}()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		limits.mu.Lock()
		now = time.Now()
		wait := limits.delay(b, now, rss)
		if wait <= 0 {
			b.tokens--
			b.requests = append(b.requests, now)
			limits.mu.Unlock()
			return nil
		}
		changed := b.changed
		limits.mu.Unlock()
		if started.IsZero() {
			started = time.Now()
			reportWait(ctx, true, 0)
		}
		timer := time.NewTimer(wait)
		select {
		case <-timer.C:
		case <-changed:
			timer.Stop()
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		}
	}
}

type waitObserverKey struct{}

// WithWaitObserver reports local rate-budget waiting, never network time.
// The callback receives no host, URL or credentials.
func WithWaitObserver(ctx context.Context, observer func(bool, time.Duration)) context.Context {
	return context.WithValue(ctx, waitObserverKey{}, observer)
}
func reportWait(ctx context.Context, waiting bool, elapsed time.Duration) {
	if observer, ok := ctx.Value(waitObserverKey{}).(func(bool, time.Duration)); ok {
		observer(waiting, elapsed)
	}
}

// ArtifactClient uses preparation slots independently of RSS fetch slots.
// Network timeout starts after permission is granted, on each redirect hop.
func (fetcher Fetcher) ArtifactClient(timeout time.Duration) *http.Client {
	client := &http.Client{Timeout: timeout}
	if fetcher.client != nil {
		if limited, ok := fetcher.client.Transport.(limitedTransport); ok {
			client.Timeout = 0
			client.Transport = artifactTransport{base: limited.base, limits: limited.limits, timeout: timeout}
		}
	}
	return client
}

type artifactTransport struct {
	base    http.RoundTripper
	limits  *Limits
	timeout time.Duration
}

func (transport artifactTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if err := transport.limits.acquireRate(request.Context(), request.URL.Hostname()); err != nil {
		return nil, err
	}
	return roundTripTimed(transport.base, request, transport.timeout, func() {})
}
func (fetcher Fetcher) WithLimits(limits *Limits) Fetcher {
	if limits == nil {
		return fetcher
	}
	client := *fetcher.client
	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	client.Transport = limitedTransport{base: base, limits: limits, timeout: client.Timeout}
	client.Timeout = 0
	fetcher.client = &client
	return fetcher
}

type limitedTransport struct {
	base    http.RoundTripper
	limits  *Limits
	timeout time.Duration
}

func (transport limitedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	release, err := transport.limits.acquire(request.Context(), request.URL.Hostname())
	if err != nil {
		return nil, err
	}
	return roundTripTimed(transport.base, request, transport.timeout, release)
}
func roundTripTimed(base http.RoundTripper, request *http.Request, timeout time.Duration, release func()) (*http.Response, error) {
	cancel := func() {}
	if timeout > 0 {
		var ctx context.Context
		ctx, cancel = context.WithTimeout(request.Context(), timeout)
		request = request.Clone(ctx)
	}
	finish := func() { cancel(); release() }
	response, err := base.RoundTrip(request)
	if err != nil {
		finish()
		return nil, err
	}
	response.Body = &limitedBody{ReadCloser: response.Body, release: finish}
	return response, nil
}

type limitedBody struct {
	io.ReadCloser
	once    sync.Once
	release func()
}

func (body *limitedBody) Close() error {
	err := body.ReadCloser.Close()
	body.once.Do(body.release)
	return err
}
