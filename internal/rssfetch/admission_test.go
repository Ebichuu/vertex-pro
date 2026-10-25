package rssfetch

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"
)

func TestDeferredArtifactDoesNotConsumeHostSlotOrRequestBudget(t *testing.T) {
	limits, _ := NewLimitsWithPreparationLimit(30, 4, 4, 2)
	ctx := context.Background()
	a, err := limits.tryArtifact(ctx, "H.EXAMPLE.")
	if err != nil {
		t.Fatal(err)
	}
	b, err := limits.tryArtifact(ctx, "h.example")
	if err != nil {
		t.Fatal(err)
	}
	defer a()
	defer b()
	for range 12 {
		_, err := limits.tryArtifact(ctx, "h.example")
		var deferred *ArtifactDeferred
		if !errors.As(err, &deferred) || !deferred.RetryAt.After(time.Now()) {
			t.Fatalf("not deferred: %v", err)
		}
	}
	limits.mu.Lock()
	h := limits.hosts["h.example"]
	active, requests := h.preparing, len(h.requests)
	limits.mu.Unlock()
	if active != 2 || requests != 2 {
		t.Fatalf("active=%d requests=%d", active, requests)
	}
	k, err := limits.tryArtifact(ctx, "k.example")
	if err != nil {
		t.Fatal(err)
	}
	k()
	a()
	a() // Response EOF, Close and cancellation may all release the same slot.
	next, err := limits.tryArtifact(ctx, "h.example")
	if err != nil {
		t.Fatal(err)
	}
	next()
	limits.mu.Lock()
	active, requests = h.preparing, len(h.requests)
	limits.mu.Unlock()
	if active != 1 || requests != 3 {
		t.Fatalf("active=%d requests=%d", active, requests)
	}
}

func TestDeferredArtifactPreservesRollingBudgetRSSReservationAndCancellation(t *testing.T) {
	limits, _ := NewLimitsWithPreparationLimit(3, 1, 3, 2)
	ctx := context.Background()
	for range 2 {
		release, err := limits.tryArtifact(ctx, "h.example")
		if err != nil {
			t.Fatal(err)
		}
		release()
	}
	_, err := limits.tryArtifact(ctx, "h.example")
	var deferred *ArtifactDeferred
	if !errors.As(err, &deferred) || time.Until(deferred.RetryAt) < 55*time.Second {
		t.Fatalf("rolling budget not enforced: %v", err)
	}
	rssCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if err := limits.acquireBudget(rssCtx, "h.example", true); err != nil {
		t.Fatal("reserved RSS request blocked", err)
	}
	canceled, stop := context.WithCancel(ctx)
	stop()
	if _, err := limits.tryArtifact(canceled, "fresh.example"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	limits.mu.Lock()
	requests, hosts := len(limits.hosts["h.example"].requests), len(limits.hosts)
	limits.mu.Unlock()
	if requests != 3 || hosts != 1 {
		t.Fatalf("requests=%d hosts=%d", requests, hosts)
	}
}

func TestNonblockingArtifactTransportDoesNotCallHTTPWhileFull(t *testing.T) {
	called := make(chan string, 8)
	fetcher := artifactHTTPFetcher(t, 1, func(w http.ResponseWriter, r *http.Request) {
		called <- r.Host
		w.Header().Set("Content-Length", "4")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(WithNonblockingArtifacts(context.Background()))
	defer cancel()
	client := fetcher.ArtifactClient(5 * time.Second)
	first := awaitArtifactHTTP(t, startArtifactHTTP(ctx, client, "http://h.example/one"))
	<-called
	select {
	case result := <-startArtifactHTTP(ctx, client, "http://h.example/two"):
		var deferred *ArtifactDeferred
		if !errors.As(result.err, &deferred) {
			t.Fatalf("not deferred: %v", result.err)
		}
	case <-time.After(time.Second):
		t.Fatal("permission wait blocked the caller")
	}
	select {
	case <-called:
		t.Fatal("denied request reached HTTP")
	default:
	}
	other := awaitArtifactHTTP(t, startArtifactHTTP(ctx, client, "http://k.example/one"))
	_ = other.Body.Close()
	_ = first.Body.Close()
	resumed := awaitArtifactHTTP(t, startArtifactHTTP(ctx, client, "http://h.example/two"))
	_ = resumed.Body.Close()
	_, _ = io.Copy(io.Discard, first.Body)
}
