package rssfetch

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Limits is shared by scheduled polls and manual previews. A request keeps its
// concurrency slot until its response body is closed. Redirects acquire their
// destination's budget through the transport, just like initial requests.
type Limits struct {
	interval time.Duration
	slots    chan struct{}
	mu       sync.Mutex
	next     map[string]time.Time
}

func NewLimits(perMinute, concurrent int) (*Limits, error) {
	if perMinute < 1 || perMinute > 600 || concurrent < 1 || concurrent > 32 {
		return nil, errors.New("invalid RSS limits")
	}
	return &Limits{interval: time.Minute / time.Duration(perMinute), slots: make(chan struct{}, concurrent), next: make(map[string]time.Time)}, nil
}

func (limits *Limits) acquire(ctx context.Context, host string) (func(), error) {
	select {
	case limits.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	release := func() { <-limits.slots }
	if err := limits.acquireRate(ctx, host); err != nil {
		release()
		return nil, err
	}
	return release, nil
}

func (limits *Limits) acquireRate(ctx context.Context, host string) error {
	host = strings.ToLower(host)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		limits.mu.Lock()
		now := time.Now()
		wait := limits.next[host].Sub(now)
		if wait <= 0 {
			for key, next := range limits.next {
				if !next.After(now) {
					delete(limits.next, key)
				}
			}
			limits.next[host] = now.Add(limits.interval)
			limits.mu.Unlock()
			return nil
		}
		limits.mu.Unlock()
		timer := time.NewTimer(wait)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		}
	}
}

// ArtifactClient shares the upstream request cadence with RSS, including
// redirects, but uses preparation slots independently of RSS fetch slots.
func (fetcher Fetcher) ArtifactClient(timeout time.Duration) *http.Client {
	client := &http.Client{Timeout: timeout}
	if fetcher.client == nil {
		return client
	}
	if limited, ok := fetcher.client.Transport.(limitedTransport); ok {
		client.Transport = artifactTransport{base: limited.base, limits: limited.limits}
	}
	return client
}

type artifactTransport struct {
	base   http.RoundTripper
	limits *Limits
}

func (transport artifactTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if err := transport.limits.acquireRate(request.Context(), request.URL.Hostname()); err != nil {
		return nil, err
	}
	return transport.base.RoundTrip(request)
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
	client.Transport = limitedTransport{base: base, limits: limits}
	fetcher.client = &client
	return fetcher
}

type limitedTransport struct {
	base   http.RoundTripper
	limits *Limits
}

func (transport limitedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	release, err := transport.limits.acquire(request.Context(), request.URL.Hostname())
	if err != nil {
		return nil, err
	}
	response, err := transport.base.RoundTrip(request)
	if err != nil {
		release()
		return nil, err
	}
	response.Body = &limitedBody{ReadCloser: response.Body, release: release}
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
