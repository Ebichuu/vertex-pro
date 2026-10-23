package schedulercycle

import (
	"bytes"
	"context"
	"crypto/sha1"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
	"github.com/vertex-pro/vertex-pro/internal/matchpipeline"
	"github.com/vertex-pro/vertex-pro/internal/racejob"
	"github.com/vertex-pro/vertex-pro/internal/racejobstore"
	"github.com/vertex-pro/vertex-pro/internal/rssfetch"
	"github.com/vertex-pro/vertex-pro/internal/rsssource"
	"github.com/vertex-pro/vertex-pro/internal/ruleaggregate"
	"github.com/vertex-pro/vertex-pro/internal/ruleservice"
	"github.com/vertex-pro/vertex-pro/internal/torrentdownload"
)

func feedXML(base string) string {
	return fmt.Sprintf(`<?xml version="1.0"?>
<rss version="2.0"><channel><title>Site</title>
<item><guid>guid-1</guid><title>Ubuntu Linux 24.04</title><link>%s/ubuntu.torrent</link></item>
<item><guid>guid-2</guid><title>Debian Linux 12</title><link>%s/debian.torrent</link></item>
</channel></rss>`, base, base)
}

func fixtureTorrent(name string) []byte {
	piece := sha1.Sum([]byte{0})
	var buffer bytes.Buffer
	buffer.WriteString("d4:infod6:lengthi1e4:name")
	fmt.Fprintf(&buffer, "%d:", len(name))
	buffer.WriteString(name)
	buffer.WriteString("12:piece lengthi16384e6:pieces20:")
	buffer.Write(piece[:])
	buffer.WriteString("ee")
	return buffer.Bytes()
}

func cycleDatabase(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.Open(context.Background(), db.Config{Path: filepath.Join(t.TempDir(), "cycle.db"), MaxOpenConns: 1, MaxIdleConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := db.DefaultRunner()
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Apply(context.Background(), database); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO sites(site_id, name, base_url, enabled, created_at, updated_at) VALUES ('site-1','Site','https://site.example',1,strftime('%Y-%m-%dT%H:%M:%SZ','now'),strftime('%Y-%m-%dT%H:%M:%SZ','now'))`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func TestCycleFetchesDedupesIdentifiesAndMatches(t *testing.T) {
	database := cycleDatabase(t)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/feed.xml":
			_, _ = response.Write([]byte(feedXML("http://" + request.Host)))
		case "/ubuntu.torrent":
			_, _ = response.Write(fixtureTorrent("Ubuntu Linux 24.04"))
		default:
			http.Error(response, "not found", http.StatusNotFound)
		}
	}))
	defer server.Close()

	fetcher, err := rssfetch.New(&http.Client{Timeout: 10 * time.Second}, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	allowedHost := server.URL // host with port; normalizeHost keeps the port
	allowedHost = strings.TrimPrefix(strings.TrimPrefix(allowedHost, "https://"), "http://")
	downloader, err := torrentdownload.New(torrentdownload.Config{AllowedHosts: []string{allowedHost}})
	if err != nil {
		t.Fatal(err)
	}
	dedupe, err := rsssource.New(rsssourceMemory())
	if err != nil {
		t.Fatal(err)
	}
	rulesService := ruleserviceFor(t, database)
	jobStore, err := racejobstore.New(database)
	if err != nil {
		t.Fatal(err)
	}
	creator, err := racejob.New(jobStore, nil)
	if err != nil {
		t.Fatal(err)
	}
	pipeline := matchpipeline.Deps{
		Rules:      rulesService,
		JobCreator: creator,
		MatchFactory: func(rule ruleservice.Rule) (ruleaggregate.Match, error) {
			return ruleaggregate.Match{TargetMode: "FIXED_INSTANCE", TargetInstanceID: "instance-1", ParticipationWindow: time.Hour}, nil
		},
	}
	cycle, err := New(fetcher, downloader, dedupe, pipeline, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	feed := FeedRef{SiteID: "site-1", FeedID: "feed-1", URL: server.URL + "/feed.xml"}
	first, err := cycle.Run(context.Background(), feed)
	if err != nil {
		t.Fatal(err)
	}
	if first.Entries != 2 || first.New != 2 || first.Matched != 1 || first.Skipped != 0 || first.Failed != 1 {
		t.Fatalf("first=%+v", first)
	}
	// Debian's artifact 404s: recorded as a failure, cycle continued.
	if len(first.Failures) != 1 {
		t.Fatalf("failures=%+v", first.Failures)
	}

	second, err := cycle.Run(context.Background(), feed)
	if err != nil {
		t.Fatal(err)
	}
	if second.New != 0 || second.Duplicates != 2 || second.Matched != 0 {
		t.Fatalf("second=%+v", second)
	}
}

func TestCycleMissingTitleDoesNotMatch(t *testing.T) {
	database := cycleDatabase(t)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		_, _ = response.Write([]byte(`<?xml version="1.0"?><rss version="2.0"><channel><title>S</title></channel></rss>`))
	}))
	defer server.Close()
	fetcher, err := rssfetch.New(&http.Client{Timeout: 10 * time.Second}, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	downloader, err := torrentdownload.New(torrentdownload.Config{AllowedHosts: []string{"127.0.0.1", "localhost"}, Client: &http.Client{Timeout: 10 * time.Second, CheckRedirect: nil}})
	if err != nil {
		t.Fatal(err)
	}
	dedupe, err := rsssource.New(rsssourceMemory())
	if err != nil {
		t.Fatal(err)
	}
	rulesService := ruleserviceFor(t, database)
	jobStore, err := racejobstore.New(database)
	if err != nil {
		t.Fatal(err)
	}
	creator, err := racejob.New(jobStore, nil)
	if err != nil {
		t.Fatal(err)
	}
	cycle, err := New(fetcher, downloader, dedupe, matchpipeline.Deps{Rules: rulesService, JobCreator: creator}, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	result, err := cycle.Run(context.Background(), FeedRef{SiteID: "site-1", URL: server.URL + "/feed.xml"})
	if err != nil || result.Entries != 0 || result.Matched != 0 {
		t.Fatalf("empty valid feed: result=%+v err=%v", result, err)
	}
}

func rsssourceMemory() *rsssource.MemoryStore {
	return &rsssource.MemoryStore{}
}

func ruleserviceFor(t *testing.T, database *sql.DB) *ruleservice.Service {
	t.Helper()
	service, err := ruleservice.New(database, nil)
	if err != nil {
		t.Fatal(err)
	}
	definition := map[string]any{
		"version": 1,
		"root": map[string]any{
			"kind": "leaf", "field": "title", "operator": "contains", "value": map[string]any{"type": "string", "string": "Linux"},
		},
	}
	payload, err := json.Marshal(definition)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Create(context.Background(), ruleservice.CreateInput{SiteID: "site-1", Name: "linux", Enabled: true, Priority: 10, Definition: payload}); err != nil {
		t.Fatal(err)
	}
	return &service
}
