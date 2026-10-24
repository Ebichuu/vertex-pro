package rssfetch

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestLimitsShareHostsAndCancelQueuedRequests(t *testing.T) {
	limits, err := NewLimits(1, 2)
	if err != nil {
		t.Fatal(err)
	}
	release, err := limits.acquire(context.Background(), "EXAMPLE.COM")
	if err != nil {
		t.Fatal(err)
	}
	release()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := limits.acquire(ctx, "example.com"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("same host bypassed pacing: %v", err)
	}
	release, err = limits.acquire(context.Background(), "another.example")
	if err != nil {
		t.Fatalf("another host should have an independent budget: %v", err)
	}
	release()
	if len(limits.slots) != 0 {
		t.Fatal("canceled request leaked a concurrency slot")
	}
}

func TestArtifactsShareRSSCadenceWithoutTakingRSSSlots(t *testing.T) {
	limits, _ := NewLimits(1, 1)
	base := limitTestTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("body")), Header: make(http.Header)}, nil
	})
	fetcher, _ := New(&http.Client{Transport: base}, 1024)
	fetcher = fetcher.WithLimits(limits)
	client := fetcher.ArtifactClient(time.Second)
	response, err := client.Get("https://site.example/artifact")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if len(limits.slots) != 0 {
		t.Fatal("torrent occupied an RSS slot")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := fetcher.Fetch(ctx, Request{URL: "https://site.example/rss"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("RSS bypassed artifact cadence: %v", err)
	}
	if _, err := fetcher.Fetch(context.Background(), Request{URL: "https://another.example/rss"}); err != nil {
		t.Fatal(err)
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel2()
	request, _ := http.NewRequestWithContext(ctx2, http.MethodGet, "https://another.example/artifact", nil)
	if _, err := client.Do(request); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("artifact bypassed RSS cadence: %v", err)
	}
}

type limitTestTransport func(*http.Request) (*http.Response, error)

func (transport limitTestTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func TestLimitsHoldSlotUntilBodyClosedAndReleaseOnFailure(t *testing.T) {
	limits, _ := NewLimits(600, 1)
	transport := limitedTransport{limits: limits, base: limitTestTransport(func(request *http.Request) (*http.Response, error) {
		if request.URL.Hostname() == "fail.example" {
			return nil, errors.New("network failure")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ok")), Header: make(http.Header)}, nil
	})}
	request, _ := http.NewRequest(http.MethodGet, "https://one.example/rss", nil)
	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	blocked, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://two.example/rss", nil)
	if _, err := transport.RoundTrip(blocked); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("concurrency limit bypassed: %v", err)
	}
	response.Body.Close()
	response.Body.Close()
	failed, _ := http.NewRequest(http.MethodGet, "https://fail.example/rss", nil)
	if _, err := transport.RoundTrip(failed); err == nil {
		t.Fatal("expected transport failure")
	}
	if len(limits.slots) != 0 {
		t.Fatal("body close or failure leaked a slot")
	}
}

func TestLimitsAlsoPaceRedirectRequests(t *testing.T) {
	limits, _ := NewLimits(1, 1)
	client := &http.Client{Transport: limitTestTransport(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": {"https://same.example/next"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}
	fetcher, _ := New(client, 1024)
	fetcher = fetcher.WithLimits(limits).WithBoundary(Boundary{})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := fetcher.Fetch(ctx, Request{URL: "https://same.example/rss"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("redirect bypassed pacing: %v", err)
	}
	if len(limits.slots) != 0 {
		t.Fatal("redirect cancellation leaked a slot")
	}
}

func TestBurstRespectsRollingBudgetAndRSSReserve(t *testing.T) {
	limits, _ := NewLimitsWithBurst(5, 2, 5)
	ctx := context.Background()
	for range 4 {
		if err := limits.acquireRate(ctx, "site.example"); err != nil {
			t.Fatal(err)
		}
	}
	queued, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if err := limits.acquireRate(queued, "site.example"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("torrent consumed RSS reserve", err)
	}
	release, err := limits.acquire(ctx, "site.example")
	if err != nil {
		t.Fatal(err)
	}
	release()
	limits.mu.Lock()
	b := limits.hosts["site.example"]
	// Even a full token refill must not exceed five requests in any minute.
	at := b.requests[0].Add(59 * time.Second)
	if delay := limits.delay(b, at, true); delay <= 0 {
		t.Fatal("rolling-minute cap was bypassed")
	}
	if delay := limits.delay(b, b.requests[len(b.requests)-1].Add(time.Minute), true); delay != 0 {
		t.Fatal("expired requests retained", delay)
	}
	limits.mu.Unlock()
}

func TestQueuedRSSHasPriorityAndCancellationReleasesPriority(t *testing.T) {
	limits, _ := NewLimitsWithBurst(600, 2, 1)
	if err := limits.acquireRate(context.Background(), "site.example"); err != nil {
		t.Fatal(err)
	}
	rssCtx, cancel := context.WithCancel(context.Background())
	waiting := make(chan struct{})
	rssCtx = WithWaitObserver(rssCtx, func(active bool, _ time.Duration) {
		if active {
			close(waiting)
		}
	})
	done := make(chan error, 1)
	go func() {
		release, err := limits.acquire(rssCtx, "site.example")
		if release != nil {
			release()
		}
		done <- err
	}()
	<-waiting
	limits.mu.Lock()
	b := limits.hosts["site.example"]
	if delay := limits.delay(b, time.Now().Add(time.Second), false); delay <= 0 {
		t.Fatal("artifact jumped queued RSS")
	}
	limits.mu.Unlock()
	cancel()
	<-done
	limits.mu.Lock()
	defer limits.mu.Unlock()
	if b.rssWaiting != 0 || b.waiting != 0 {
		t.Fatal("cancel leaked priority")
	}
}

func TestRateWaitDoesNotConsumeNetworkTimeout(t *testing.T) {
	limits, _ := NewLimits(600, 1)
	base := limitTestTransport(func(r *http.Request) (*http.Response, error) {
		deadline, ok := r.Context().Deadline()
		if !ok || time.Until(deadline) < 15*time.Millisecond {
			t.Error("network timeout was spent in limiter")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ok")), Header: make(http.Header)}, nil
	})
	fetcher, _ := New(&http.Client{Transport: base, Timeout: 25 * time.Millisecond}, 1024)
	fetcher = fetcher.WithLimits(limits)
	for _, artifact := range []bool{false, true} {
		if err := limits.acquireRate(context.Background(), "site.example"); err != nil {
			t.Fatal(err)
		}
		var elapsed time.Duration
		ctx := WithWaitObserver(context.Background(), func(waiting bool, d time.Duration) {
			if !waiting {
				elapsed += d
			}
		})
		if artifact {
			req, _ := http.NewRequestWithContext(ctx, "GET", "https://site.example/file", nil)
			resp, err := fetcher.ArtifactClient(25 * time.Millisecond).Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
		} else if _, err := fetcher.Fetch(ctx, Request{URL: "https://site.example/rss"}); err != nil {
			t.Fatal(err)
		}
		if elapsed < 50*time.Millisecond {
			t.Fatal("rate wait was not observed", elapsed)
		}
	}
}
