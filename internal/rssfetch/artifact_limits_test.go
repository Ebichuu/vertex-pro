package rssfetch

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The transport only supplies local DNS routing. Requests, redirects and body
// cancellation still go through the real HTTP client/server implementation.
func artifactHTTPFetcher(t *testing.T, limit int, handler http.HandlerFunc) Fetcher {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	transport := &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}}
	t.Cleanup(transport.CloseIdleConnections)
	limits, err := NewLimitsWithPreparationLimit(600, 1, 16, limit)
	if err != nil {
		t.Fatal(err)
	}
	fetcher, err := New(&http.Client{Transport: transport}, 1024)
	if err != nil {
		t.Fatal(err)
	}
	return fetcher.WithLimits(limits)
}

type artifactHTTPResult struct {
	response *http.Response
	err      error
}

func startArtifactHTTP(ctx context.Context, client *http.Client, target string) <-chan artifactHTTPResult {
	done := make(chan artifactHTTPResult, 1)
	go func() {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			done <- artifactHTTPResult{err: err}
			return
		}
		response, err := client.Do(request)
		done <- artifactHTTPResult{response: response, err: err}
	}()
	return done
}

func awaitArtifactHTTP(t *testing.T, done <-chan artifactHTTPResult) *http.Response {
	t.Helper()
	select {
	case result := <-done:
		if result.err != nil {
			t.Fatal(result.err)
		}
		t.Cleanup(func() { _ = result.response.Body.Close() })
		return result.response
	case <-time.After(2 * time.Second):
		t.Fatal("artifact request did not finish; a host slot may have leaked")
		return nil
	}
}

func queueArtifactHTTP(t *testing.T, ctx context.Context, client *http.Client, target string) <-chan artifactHTTPResult {
	t.Helper()
	waiting := make(chan struct{}, 1)
	ctx = WithWaitObserver(ctx, func(active bool, _ time.Duration) {
		if active {
			select {
			case waiting <- struct{}{}:
			default:
			}
		}
	})
	done := startArtifactHTTP(ctx, client, target)
	select {
	case <-waiting:
		return done
	case result := <-done:
		if result.response != nil {
			_ = result.response.Body.Close()
		}
		t.Fatalf("request bypassed a full actual-host limit: %v", result.err)
	case <-time.After(time.Second):
		t.Fatal("host-slot waiting was not observed")
	}
	return nil
}

func TestArtifactActualHostLimitSharedAcrossRSSOriginsAndClients(t *testing.T) {
	fetcher := artifactHTTPFetcher(t, 2, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/feed" {
			if r.Host == "feed-a.example" {
				_, _ = io.WriteString(w, "http://DOWNLOAD.example.:81/file")
			} else {
				_, _ = io.WriteString(w, "http://download.example:82/file")
			}
			return
		}
		_, _ = io.WriteString(w, "torrent")
	})
	firstFeed, err := fetcher.Fetch(t.Context(), Request{URL: "http://feed-a.example/feed"})
	if err != nil {
		t.Fatal(err)
	}
	secondFeed, err := fetcher.Fetch(t.Context(), Request{URL: "http://feed-b.example/feed"})
	if err != nil {
		t.Fatal(err)
	}
	first := awaitArtifactHTTP(t, startArtifactHTTP(t.Context(), fetcher.ArtifactClient(time.Second), string(firstFeed.Body)))
	second := awaitArtifactHTTP(t, startArtifactHTTP(t.Context(), fetcher.ArtifactClient(time.Second), string(secondFeed.Body)))
	queued := queueArtifactHTTP(t, t.Context(), fetcher.ArtifactClient(time.Second), "http://download.example/file")

	// A full torrent host neither consumes the RSS global slot nor blocks
	// artifacts from other hostnames, even when their IP addresses are equal.
	if _, err := fetcher.Fetch(t.Context(), Request{URL: "http://download.example/rss"}); err != nil {
		t.Fatalf("artifacts blocked the RSS slot: %v", err)
	}
	other := awaitArtifactHTTP(t, startArtifactHTTP(t.Context(), fetcher.ArtifactClient(time.Second), "http://other.example/file"))
	_ = other.Body.Close()
	select {
	case result := <-queued:
		if result.response != nil {
			_ = result.response.Body.Close()
		}
		t.Fatalf("same host exceeded two outstanding response bodies: %v", result.err)
	default:
	}
	if _, err := io.ReadAll(first.Body); err != nil {
		t.Fatal(err)
	}
	third := awaitArtifactHTTP(t, queued)
	_ = third.Body.Close()
	_ = second.Body.Close()
}

func TestArtifactHostSlotReleasedOnBodyCompletionAndCancellation(t *testing.T) {
	for _, mode := range []string{"eof", "close", "read-error", "cancel", "network-timeout"} {
		t.Run(mode, func(t *testing.T) {
			fetcher := artifactHTTPFetcher(t, 1, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/first" || mode == "eof" {
					_, _ = io.WriteString(w, "torrent")
					return
				}
				w.Header().Set("Content-Length", "100")
				_, _ = io.WriteString(w, "x")
				w.(http.Flusher).Flush()
				if mode != "read-error" {
					<-r.Context().Done()
				}
			})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			timeout := time.Second
			if mode == "network-timeout" {
				timeout = 200 * time.Millisecond
			}
			first := awaitArtifactHTTP(t, startArtifactHTTP(ctx, fetcher.ArtifactClient(timeout), "http://download.example/first"))
			queued := queueArtifactHTTP(t, t.Context(), fetcher.ArtifactClient(time.Second), "http://DOWNLOAD.example./second")
			switch mode {
			case "eof":
				if _, err := io.ReadAll(first.Body); err != nil {
					t.Fatal(err)
				}
			case "close":
				_ = first.Body.Close()
				_ = first.Body.Close()
			case "read-error":
				if _, err := io.ReadAll(first.Body); !errors.Is(err, io.ErrUnexpectedEOF) {
					t.Fatalf("expected truncated response body, got %v", err)
				}
			case "cancel":
				cancel() // No Read or Close is needed to release an idle canceled body.
			case "network-timeout":
				// The per-hop timeout must also release an unread response body.
			}
			second := awaitArtifactHTTP(t, queued)
			if _, err := io.ReadAll(second.Body); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestArtifactHostSlotReleasedOnHTTPTransportFailure(t *testing.T) {
	fetcher := artifactHTTPFetcher(t, 1, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/broken" {
			connection, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = connection.Close()
			}
			return
		}
		_, _ = io.WriteString(w, "torrent")
	})
	client := fetcher.ArtifactClient(time.Second)
	broken := <-startArtifactHTTP(t.Context(), client, "http://download.example/broken")
	if broken.err == nil {
		_ = broken.response.Body.Close()
		t.Fatal("expected a network error before response headers")
	}
	awaitArtifactHTTP(t, startArtifactHTTP(t.Context(), client, "http://download.example/file"))
}

func TestArtifactHostLimitAppliesBeforeHeadersAndReleasesOnCancel(t *testing.T) {
	started := make(chan struct{})
	fetcher := artifactHTTPFetcher(t, 1, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow-headers" {
			close(started)
			<-r.Context().Done()
			return
		}
		_, _ = io.WriteString(w, "torrent")
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	client := fetcher.ArtifactClient(time.Second)
	first := startArtifactHTTP(ctx, client, "http://download.example/slow-headers")
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first request never reached the HTTP server")
	}
	queued := queueArtifactHTTP(t, t.Context(), client, "http://download.example/second")
	cancel()
	select {
	case result := <-first:
		if !errors.Is(result.err, context.Canceled) {
			t.Fatalf("network request did not report cancellation: %v", result.err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not stop the request waiting for headers")
	}
	awaitArtifactHTTP(t, queued)
}

func TestArtifactHostSlotWaitPreservesNetworkTimeoutAndConsumerDeadline(t *testing.T) {
	fetcher := artifactHTTPFetcher(t, 1, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "torrent")
	})
	first := awaitArtifactHTTP(t, startArtifactHTTP(t.Context(), fetcher.ArtifactClient(time.Second), "http://download.example/first"))
	shortNetwork := fetcher.ArtifactClient(30 * time.Millisecond)
	queued := queueArtifactHTTP(t, t.Context(), shortNetwork, "http://download.example/second")
	select {
	case result := <-queued:
		if result.response != nil {
			_ = result.response.Body.Close()
		}
		t.Fatalf("network timeout ran while waiting for a host slot: %v", result.err)
	case <-time.After(80 * time.Millisecond):
	}
	_ = first.Body.Close()
	second := awaitArtifactHTTP(t, queued)
	if _, err := io.ReadAll(second.Body); err != nil {
		t.Fatalf("network timeout was consumed while waiting: %v", err)
	}
	held := awaitArtifactHTTP(t, startArtifactHTTP(t.Context(), fetcher.ArtifactClient(time.Second), "http://download.example/held"))
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	consumerQueued := queueArtifactHTTP(t, ctx, shortNetwork, "http://download.example/third")
	select {
	case result := <-consumerQueued:
		if !errors.Is(result.err, context.DeadlineExceeded) {
			t.Fatalf("host wait ignored consumer deadline: %v", result.err)
		}
	case <-time.After(time.Second):
		t.Fatal("consumer deadline did not cancel queued host request")
	}
	_ = held.Body.Close()
	awaitArtifactHTTP(t, startArtifactHTTP(t.Context(), shortNetwork, "http://download.example/after-cancel"))
}

func TestArtifactRedirectsUseDestinationHostLimit(t *testing.T) {
	fetcher := artifactHTTPFetcher(t, 1, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "http://download.example/file", http.StatusFound)
			return
		}
		_, _ = io.WriteString(w, "torrent")
	})
	client := fetcher.ArtifactClient(time.Second)
	first := awaitArtifactHTTP(t, startArtifactHTTP(t.Context(), client, "http://feed-a.example/redirect"))
	queued := queueArtifactHTTP(t, t.Context(), client, "http://feed-b.example/redirect")
	// The second redirect waits only on its destination, leaving its original
	// host available for another artifact request.
	awaitArtifactHTTP(t, startArtifactHTTP(t.Context(), client, "http://feed-b.example/file"))
	_ = first.Body.Close()
	awaitArtifactHTTP(t, queued)
}

func TestArtifactOppositeHostRedirectsDoNotDeadlock(t *testing.T) {
	arrived := make(chan struct{}, 2)
	redirect := make(chan struct{})
	fetcher := artifactHTTPFetcher(t, 1, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			arrived <- struct{}{}
			select {
			case <-redirect:
			case <-r.Context().Done():
				return
			}
			target := "a.example"
			if strings.HasPrefix(r.Host, "a.") {
				target = "b.example"
			}
			http.Redirect(w, r, "http://"+target+"/file", http.StatusFound)
			return
		}
		_, _ = io.WriteString(w, "torrent")
	})
	client := fetcher.ArtifactClient(time.Second)
	first := startArtifactHTTP(t.Context(), client, "http://a.example/start")
	second := startArtifactHTTP(t.Context(), client, "http://b.example/start")
	for range 2 {
		select {
		case <-arrived:
		case <-time.After(time.Second):
			t.Fatal("independent source hosts failed to start in parallel")
		}
	}
	close(redirect)
	awaitArtifactHTTP(t, first)
	awaitArtifactHTTP(t, second)
}

func TestLimitsValidatePreparationHostLimit(t *testing.T) {
	for _, maximum := range []int{0, 9} {
		if _, err := NewLimitsWithPreparationLimit(30, 4, 4, maximum); err == nil {
			t.Fatalf("accepted invalid per-host limit %d", maximum)
		}
	}
}
