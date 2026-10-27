package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vertex-pro/vertex-pro/internal/secrets"
	"github.com/vertex-pro/vertex-pro/internal/syncfixture"
)

func newMaindataClient(tb testing.TB, body []byte) *Client {
	tb.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write(body)
	}))
	tb.Cleanup(server.Close)
	client, err := New(context.Background(), Config{BaseURL: server.URL, ProxyKey: secrets.SecretRef{Provider: "env", Key: "PROXY_KEY"}, Store: staticStore{value: "key"}})
	if err != nil {
		tb.Fatal(err)
	}
	return client
}

// A full maindata response for a large seeding box is tens of megabytes.
func TestFetchDecodesLargeFullMaindata(t *testing.T) {
	const n = 20_000
	client := newMaindataClient(t, syncfixture.FullMaindataJSON(n, 1))
	delta, err := client.Fetch(context.Background(), 0)
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if !delta.FullUpdate || len(delta.Torrents) != n || delta.Torrents[syncfixture.Hash(7)].Name == nil {
		t.Fatalf("delta: full=%v torrents=%d", delta.FullUpdate, len(delta.Torrents))
	}
}

func TestFetchRejectsOversizedBodyAndDigestsRawBytes(t *testing.T) {
	body := syncfixture.IncrementalMaindataJSON(100, 10, 2)
	client := newMaindataClient(t, body)
	first, err := client.Fetch(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	second, err := client.Fetch(context.Background(), 1)
	if err != nil || first.BodyDigest == "" || first.BodyDigest != second.BodyDigest || first.RID != 2 || len(first.Torrents) != 10 {
		t.Fatalf("digests %q/%q delta=%+v err=%v", first.BodyDigest, second.BodyDigest, first.RID, err)
	}
	capped := &cappedReader{reader: bytes.NewReader(body), remaining: int64(len(body) - 1)}
	var target map[string]any
	if err := json.NewDecoder(capped).Decode(&target); !errors.Is(err, errResponseTooLarge) {
		t.Fatalf("oversized decode error = %v", err)
	}
}

func BenchmarkFetchFullMaindata(b *testing.B) {
	for _, n := range []int{1_000, 10_000} {
		b.Run(fmt.Sprintf("torrents=%d", n), func(b *testing.B) {
			body := syncfixture.FullMaindataJSON(n, 1)
			client := newMaindataClient(b, body)
			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if _, err := client.Fetch(context.Background(), 0); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkFetchIncrementalMaindata(b *testing.B) {
	for _, n := range []int{1_000, 10_000} {
		b.Run(fmt.Sprintf("torrents=%d", n), func(b *testing.B) {
			body := syncfixture.IncrementalMaindataJSON(n, n/20, 2)
			client := newMaindataClient(b, body)
			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if _, err := client.Fetch(context.Background(), 1); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
