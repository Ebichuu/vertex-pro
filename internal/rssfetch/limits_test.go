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
