package rssfetch

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFetcherFetchesBodyAndConditionalHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if got := request.Header.Get("If-None-Match"); got != `"v1"` {
			t.Errorf("If-None-Match = %q", got)
		}
		if got := request.Header.Get("If-Modified-Since"); got != "Wed, 21 Oct 2015 07:28:00 GMT" {
			t.Errorf("If-Modified-Since = %q", got)
		}
		if got := request.Header.Get("User-Agent"); got == "" {
			t.Error("User-Agent is empty")
		}
		writer.Header().Set("ETag", `"v2"`)
		writer.Header().Set("Last-Modified", "Thu, 22 Oct 2015 07:28:00 GMT")
		writer.Header().Set("Content-Type", "application/rss+xml")
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte("<rss/>"))
	}))
	defer server.Close()

	fetcher, err := New(server.Client(), 1024)
	if err != nil {
		t.Fatal(err)
	}
	result, err := fetcher.Fetch(context.Background(), Request{URL: server.URL, ETag: `"v1"`, LastModified: "Wed, 21 Oct 2015 07:28:00 GMT"})
	if err != nil {
		t.Fatal(err)
	}
	if result.StatusCode != http.StatusOK || string(result.Body) != "<rss/>" || result.ETag != `"v2"` || result.ContentType != "application/rss+xml" {
		t.Fatalf("unexpected response: %+v", result)
	}
}

func TestFetcherReturnsNotModifiedWithoutBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusNotModified)
	}))
	defer server.Close()
	fetcher, err := New(server.Client(), 1024)
	if err != nil {
		t.Fatal(err)
	}
	result, err := fetcher.Fetch(context.Background(), Request{URL: server.URL})
	if err != nil || result.StatusCode != http.StatusNotModified || len(result.Body) != 0 {
		t.Fatalf("unexpected 304 result: %+v, err=%v", result, err)
	}
}

func TestFetcherRejectsInvalidInputsAndOversizedBody(t *testing.T) {
	tests := []struct {
		name   string
		input  Request
		limit  int64
		server bool
	}{
		{name: "uninitialized"},
		{name: "invalid URL", input: Request{URL: "file:///tmp/feed.xml"}, limit: 1024},
		{name: "empty URL", input: Request{}, limit: 1024},
		{name: "oversized body", limit: 3, server: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.name == "uninitialized" {
				_, err := (Fetcher{}).Fetch(context.Background(), Request{URL: "https://example.com"})
				if err == nil {
					t.Fatal("expected initialization error")
				}
				return
			}
			var server *httptest.Server
			if test.server {
				server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) { _, _ = writer.Write([]byte("1234")) }))
				defer server.Close()
				test.input.URL = server.URL
			}
			fetcher, err := New(http.DefaultClient, test.limit)
			if err != nil {
				t.Fatal(err)
			}
			_, err = fetcher.Fetch(context.Background(), test.input)
			if err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestFetcherRejectsHTTPErrorAndHonorsContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/error" {
			writer.WriteHeader(http.StatusBadGateway)
			return
		}
		time.Sleep(100 * time.Millisecond)
	}))
	defer server.Close()
	fetcher, err := New(server.Client(), 1024)
	if err != nil {
		t.Fatal(err)
	}
	_, err = fetcher.Fetch(context.Background(), Request{URL: server.URL + "/error"})
	if err == nil || !strings.Contains(err.Error(), "HTTP 502") {
		t.Fatalf("unexpected HTTP error: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = fetcher.Fetch(ctx, Request{URL: server.URL})
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
}

func TestNewRejectsInvalidLimit(t *testing.T) {
	if _, err := New(nil, 0); err == nil {
		t.Fatal("expected invalid limit error")
	}
}
