package torrentdownload

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/rssfetch"
)

func TestArtifactRedirectContinuationDoesNotRepeatEntryAndRechecksBoundaries(t *testing.T) {
	var entries, artifacts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Host {
		case "entry.example":
			entries.Add(1)
			http.Redirect(w, r, "http://h.example/file", http.StatusFound)
		default:
			if r.URL.Path == "/hold" {
				w.Header().Set("Content-Length", "4")
				w.WriteHeader(200)
				w.(http.Flusher).Flush()
				<-r.Context().Done()
				return
			}
			artifacts.Add(1)
			w.Header().Set("Content-Type", "application/x-bittorrent")
			_, _ = io.WriteString(w, "fixture")
		}
	}))
	defer server.Close()
	transport := &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}}
	defer transport.CloseIdleConnections()
	limits, _ := rssfetch.NewLimitsWithPreparationLimit(600, 1, 16, 1)
	fetcher, _ := rssfetch.New(&http.Client{Transport: transport}, 1024)
	fetcher = fetcher.WithLimits(limits)
	client := fetcher.ArtifactClient(3 * time.Second)
	held, err := client.Get("http://h.example/hold")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Body.Close()
	downloader, err := New(Config{Client: client, AllowedHosts: []string{"entry.example", "h.example"}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := rssfetch.WithNonblockingArtifacts(context.Background())
	input := Request{URL: "http://entry.example/file"}
	for range 3 {
		_, err := downloader.Download(ctx, input)
		var deferred *Deferred
		if !errors.As(err, &deferred) || deferred.Redirects != 1 || deferred.URL != "http://h.example/file" {
			t.Fatalf("continuation missing: %v", err)
		}
		if strings.Contains(err.Error(), "example") {
			t.Fatal("continuation error exposes private URL")
		}
		input.ResumeURL, input.Redirects = deferred.URL, deferred.Redirects
	}
	if entries.Load() != 1 || artifacts.Load() != 0 {
		t.Fatal("repeated entry or bypassed H slot", entries.Load(), artifacts.Load())
	}
	_ = held.Body.Close()
	artifact, err := downloader.Download(ctx, input)
	if err != nil || string(artifact.Body) != "fixture" || artifacts.Load() != 1 || entries.Load() != 1 {
		t.Fatal("continuation failed", err)
	}
	// A stored continuation is not permission to bypass a changed allowlist.
	restricted, _ := New(Config{Client: client, AllowedHosts: []string{"entry.example"}})
	if _, err := restricted.Download(ctx, input); err == nil {
		t.Fatal("continuation bypassed allowlist")
	}
	input.CredentialRef, input.CredentialOrigin = "env:VERTEX_SITE_FIXTURE", "https://entry.example"
	if _, err := downloader.Download(ctx, input); err == nil {
		t.Fatal("continuation bypassed credential origin")
	}
	if artifacts.Load() != 1 {
		t.Fatal("rejected continuation reached HTTP")
	}
}

type continuationTransport func(*http.Request) (*http.Response, error)

func (fn continuationTransport) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }

func TestRedirectLimitSurvivesRepeatedDeferral(t *testing.T) {
	// Allow exactly one hop per invocation, then yield its next hop. Without a
	// persisted total, a redirect loop would reset the client's limit each time.
	deny := false
	requests := 0
	client := &http.Client{Transport: continuationTransport(func(r *http.Request) (*http.Response, error) {
		if deny {
			return nil, &rssfetch.ArtifactDeferred{RetryAt: time.Now().Add(time.Second)}
		}
		deny = true
		requests++
		return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": []string{"http://h.example/again"}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	})}
	downloader, _ := New(Config{Client: client, AllowedHosts: []string{"h.example"}})
	input := Request{URL: "http://h.example/first"}
	for hop := 1; hop <= 10; hop++ {
		deny = false
		_, err := downloader.Download(context.Background(), input)
		var deferred *Deferred
		if hop == 10 {
			if err == nil || errors.As(err, &deferred) || !strings.Contains(err.Error(), "too many torrent redirects") {
				t.Fatalf("redirect limit reset: %v", err)
			}
			break
		}
		if !errors.As(err, &deferred) || deferred.Redirects != hop {
			t.Fatalf("hop=%d err=%v", hop, err)
		}
		input.ResumeURL, input.Redirects = deferred.URL, deferred.Redirects
	}
	if requests != 10 {
		t.Fatalf("requests=%d", requests)
	}
}
