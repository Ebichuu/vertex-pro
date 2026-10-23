package schedulercycle

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/vertex-pro/vertex-pro/internal/rsssource"
	"github.com/vertex-pro/vertex-pro/internal/rules"
	"github.com/vertex-pro/vertex-pro/internal/ruleservice"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestPrefilterOnlySkipsDefiniteRSSRejections(t *testing.T) {
	text := "other"
	size := float64(1)
	falseTitle := rules.Node{Kind: rules.LeafKind, Field: "title", Operator: rules.EqualsOperator, Value: &rules.Value{Type: rules.StringValue, String: &text}}
	sizeExists := rules.Node{Kind: rules.LeafKind, Field: "size", Operator: rules.ExistsOperator}
	for _, tc := range []struct {
		name               string
		node               rules.Node
		downloads, matched int
	}{
		{"title mismatch", falseTitle, 0, 0},
		{"missing exists", sizeExists, 1, 1},
		{"missing not exists", rules.Node{Kind: rules.LeafKind, Field: "size", Operator: rules.NotExistsOperator}, 1, 0},
		{"rss size ignored", rules.Node{Kind: rules.LeafKind, Field: "size", Operator: rules.EqualsOperator, Value: &rules.Value{Type: rules.NumberValue, Number: &size}}, 1, 1},
		{"or uncertain", rules.Node{Kind: rules.GroupKind, Operator: rules.OrOperator, Children: []rules.Node{falseTitle, sizeExists}}, 1, 1},
		{"and false", rules.Node{Kind: rules.GroupKind, Operator: rules.AndOperator, Children: []rules.Node{falseTitle, sizeExists}}, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			database := cycleDatabase(t)
			service, _ := ruleservice.New(database, nil)
			raw, _ := json.Marshal(rules.ConditionTree{Version: 1, Root: tc.node})
			if _, err := service.Create(context.Background(), ruleservice.CreateInput{SiteID: "site-1", Name: "filter", Enabled: true, Definition: raw}); err != nil {
				t.Fatal(err)
			}
			var downloads atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/item.torrent" {
					downloads.Add(1)
					w.Header().Set("Content-Type", "application/x-bittorrent")
					w.Write(fixtureTorrent("Linux"))
					return
				}
				fmt.Fprintf(w, `<rss><channel><item><guid>x</guid><title>Linux</title><enclosure url="http://%s/item.torrent" length="9999" type="application/x-bittorrent"/></item></channel></rss>`, r.Host)
			}))
			defer server.Close()
			now := time.Now()
			cycle := recoveryCycle(t, database, server.URL, &now, &service)
			result, err := cycle.Run(context.Background(), FeedRef{SiteID: "site-1", FeedID: "feed-1", URL: server.URL + "/rss"})
			if err != nil || result.Failed != 0 || result.Matched != tc.matched || downloads.Load() != int32(tc.downloads) {
				t.Fatalf("result=%+v downloads=%d err=%v", result, downloads.Load(), err)
			}
		})
	}
}

func TestOverlappingConsumersShareArtifactButKeepTheirOwnFacts(t *testing.T) {
	database := cycleDatabase(t)
	service := ruleserviceFor(t, database)
	if _, err := database.Exec(`INSERT INTO feeds(feed_id,site_id,name,feed_url,created_at,updated_at) VALUES('feed-2','site-1','second','https://site.example/second','now','now')`); err != nil {
		t.Fatal(err)
	}
	var downloads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/item.torrent" {
			downloads.Add(1)
			w.Header().Set("Content-Type", "application/x-bittorrent")
			w.Write(fixtureTorrent("Linux"))
			return
		}
		fmt.Fprintf(w, `<rss><channel><item><guid>same</guid><title>Linux %s</title><link>http://%s/item.torrent</link></item></channel></rss>`, r.URL.Path, r.Host)
	}))
	defer server.Close()
	now := time.Now()
	cycle := recoveryCycle(t, database, server.URL, &now, service)
	for _, id := range []string{"feed-1", "feed-2"} {
		result, err := cycle.Run(context.Background(), FeedRef{SiteID: "site-1", FeedID: id, URL: server.URL + "/" + id})
		if err != nil || result.Matched != 1 {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	}
	if downloads.Load() != 1 {
		t.Fatalf("overlapping consumers downloaded %d times", downloads.Load())
	}
	item, err := cycle.identify(context.Background(), rsssource.Source{SiteID: "site-1", SourceKey: "guid:same", URL: server.URL + "/item.torrent", Title: "consumer own title", PublishedAt: "consumer own publication"}, FeedRef{})
	if err != nil || item.Source.Title != "consumer own title" || item.Source.PublishedAt != "consumer own publication" {
		t.Fatalf("consumer facts overwritten: %v", err)
	}
}
