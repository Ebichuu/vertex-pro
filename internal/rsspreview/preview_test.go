package rsspreview

import (
	"context"
	"errors"
	"testing"
)

type fakeFetcher struct {
	body  []byte
	err   error
	calls int
}

func (f *fakeFetcher) Fetch(context.Context, string) ([]byte, error) { f.calls++; return f.body, f.err }
func TestTestParsesRSSPreviewWithoutSideEffects(t *testing.T) {
	f := &fakeFetcher{body: []byte(`<rss><channel><item><guid>one</guid><title>One</title><link>https://example.test/one</link></item><item><guid>two</guid><title>Two</title></item></channel></rss>`)}
	p, err := Test(context.Background(), f, "https://example.test/rss", 1)
	if err != nil {
		t.Fatal(err)
	}
	if p.Format != "rss" || len(p.Entries) != 1 || p.Entries[0].ID != "one" || f.calls != 1 {
		t.Fatalf("preview=%+v calls=%d", p, f.calls)
	}
}
func TestParseSupportsAtomAndRejectsInvalidInputs(t *testing.T) {
	p, err := Parse("https://example.test/atom", []byte(`<feed xmlns="http://www.w3.org/2005/Atom"><entry><id>atom-1</id><title>Atom</title><updated>2026-08-25T00:00:00Z</updated><link rel="alternate" href="https://example.test/a"/></entry></feed>`), 10)
	if err != nil || p.Format != "atom" || p.Entries[0].Link == "" {
		t.Fatalf("preview=%+v err=%v", p, err)
	}
	for _, limit := range []int{0, 101} {
		if _, err := Parse("url", []byte(`<rss/>`), limit); err == nil {
			t.Fatalf("limit %d accepted", limit)
		}
	}
	if _, err := Parse("url", nil, 1); err == nil {
		t.Fatal("empty body accepted")
	}
}
func TestTestFailsClosedOnFetcherError(t *testing.T) {
	f := &fakeFetcher{err: errors.New("network blocked")}
	if _, err := Test(context.Background(), f, "https://example.test/rss", 10); err == nil {
		t.Fatal("fetch error swallowed")
	}
	if _, err := Test(context.Background(), nil, "url", 10); err == nil {
		t.Fatal("nil fetcher accepted")
	}
}

func TestEnclosuresResolveAgainstFeedURL(t *testing.T) {
	for _, body := range []string{
		`<rss><channel><item><guid>x</guid><link>/details/1</link><enclosure url="../download/1" type="application/x-bittorrent" length="4096"/></item></channel></rss>`,
		`<feed xmlns="http://www.w3.org/2005/Atom"><entry><id>x</id><link rel="alternate" href="/details/1"/><link rel="enclosure" href="../download/1" type="application/x-bittorrent" length="4096"/></entry></feed>`,
	} {
		p, err := Parse("https://example.test/feeds/main", []byte(body), 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(p.Entries) != 1 {
			t.Fatalf("entries=%+v", p.Entries)
		}
		e := p.Entries[0]
		if e.DownloadURL != "https://example.test/download/1" || e.Link != "https://example.test/details/1" || e.SizeBytes == nil || *e.SizeBytes != 4096 {
			t.Fatalf("entry=%+v", e)
		}
	}
}
