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
	interval            time.Duration
	perMinute, burst    int
	preparationsPerHost int
	slots               chan struct{}
	mu                  sync.Mutex
	hosts               map[string]*hostBudget
}
type hostBudget struct {
	tokens              float64
	updated             time.Time
	requests            []time.Time
	rssWaiting, waiting int
	preparing           int
	changed             chan struct{}
}

func NewLimits(perMinute, concurrent int) (*Limits, error) {
	return NewLimitsWithBurst(perMinute, concurrent, 1)
}
func NewLimitsWithBurst(perMinute, concurrent, burst int) (*Limits, error) {
	return NewLimitsWithPreparationLimit(perMinute, concurrent, burst, 2)
}

// NewLimitsWithPreparationLimit keeps RSS fetch slots separate from the active
// torrent requests and response bodies allowed for each actual request host.
func NewLimitsWithPreparationLimit(perMinute, concurrent, burst, preparationsPerHost int) (*Limits, error) {
	if perMinute < 1 || perMinute > 600 || concurrent < 1 || concurrent > 32 || burst < 1 || burst > 16 || preparationsPerHost < 1 || preparationsPerHost > 8 {
		return nil, errors.New("invalid RSS limits")
	}
	return &Limits{interval: time.Minute / time.Duration(perMinute), perMinute: perMinute, burst: min(burst, perMinute), preparationsPerHost: preparationsPerHost, slots: make(chan struct{}, concurrent), hosts: map[string]*hostBudget{}}, nil
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

func (limits *Limits) acquireArtifact(ctx context.Context, host string) (func(), error) {
	return limits.acquireHost(ctx, host, false, true)
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
	_, err := limits.acquireHost(ctx, host, rss, false)
	return err
}

func (limits *Limits) acquireHost(ctx context.Context, host string, rss, artifact bool) (func(), error) {
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
			return nil, err
		}
		limits.mu.Lock()
		now = time.Now()
		wait := limits.delay(b, now, rss)
		if wait <= 0 && (!artifact || b.preparing < limits.preparationsPerHost) {
			b.tokens--
			b.requests = append(b.requests, now)
			if artifact {
				b.preparing++
			}
			limits.mu.Unlock()
			if !artifact {
				return nil, nil
			}
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
		changed := b.changed
		limits.mu.Unlock()
		if started.IsZero() {
			started = time.Now()
			reportWait(ctx, true, 0)
		}
		// A full host waits for a body release. No rate token or network timeout
		// is consumed while either the host slot or its rate budget is pending.
		var timer *time.Timer
		var timerC <-chan time.Time
		if wait > 0 {
			timer = time.NewTimer(wait)
			timerC = timer.C
		}
		select {
		case <-timerC:
		case <-changed:
		case <-ctx.Done():
		}
		if timer != nil {
			timer.Stop()
		}
	}
}

type waitObserverKey struct{}

// WithWaitObserver reports local rate-budget or artifact-host-slot waiting,
// never network time.
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
	release, err := transport.limits.acquireArtifact(request.Context(), request.URL.Hostname())
	if err != nil {
		return nil, err
	}
	return roundTripTimed(transport.base, request, transport.timeout, release)
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
	body := &limitedBody{ReadCloser: response.Body, release: finish}
	response.Body = body
	body.watch(request.Context())
	return response, nil
}

type limitedBody struct {
	io.ReadCloser
	once     sync.Once
	closeErr error
	release  func()
	mu       sync.Mutex
	stop     func() bool
	finished bool
}

func (body *limitedBody) watch(ctx context.Context) {
	stop := context.AfterFunc(ctx, func() { _ = body.Close() })
	body.mu.Lock()
	if body.finished {
		stop()
	} else {
		body.stop = stop
	}
	body.mu.Unlock()
}

func (body *limitedBody) Read(p []byte) (int, error) {
	n, err := body.ReadCloser.Read(p)
	if err != nil {
		_ = body.Close()
	}
	return n, err
}

func (body *limitedBody) Close() error {
	body.once.Do(func() {
		body.closeErr = body.ReadCloser.Close()
		body.mu.Lock()
		body.finished = true
		if body.stop != nil {
			body.stop()
		}
		body.mu.Unlock()
		body.release()
	})
	return body.closeErr
}
