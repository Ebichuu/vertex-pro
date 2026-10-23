package resourcesapi

import (
	"context"
	"errors"
	"fmt"
	"github.com/vertex-pro/vertex-pro/internal/rssservice"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestResourceArchiveCASAndCredentialRedaction(t *testing.T) {
	database := openMigrated(t)
	now := time.Now().UTC()
	service, _ := rssservice.NewWithClock(database, func() time.Time { return now })
	site, err := service.CreateSite(context.Background(), rssservice.CreateSiteInput{ID: "test", Name: "Test", BaseURL: "https://example.test", Enabled: true, CredentialRef: "env:VERTEX_SITE_TEST"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.CreateFeed(context.Background(), rssservice.CreateFeedInput{ID: "feed", SiteID: site.ID, Name: "Feed", FeedURL: "https://example.test/private-key/rss?passkey=fixture-secret", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	router := Router(Deps{Database: database, Now: func() time.Time { return now.Add(time.Second) }})
	read := httptest.NewRecorder()
	router.ServeHTTP(read, httptest.NewRequest("GET", "/api/v1/sites", nil))
	if read.Code != 200 || strings.Contains(read.Body.String(), "private-key") || strings.Contains(read.Body.String(), "fixture-secret") {
		t.Fatalf("unredacted or invalid response %s", read.Body.String())
	}
	for _, tc := range []struct {
		etag string
		code int
	}{{`"stale"`, 409}, {fmt.Sprintf("%q", site.UpdatedAt), 200}, {fmt.Sprintf("%q", site.UpdatedAt), 409}} {
		request := httptest.NewRequest("DELETE", "/api/v1/sites/test", nil)
		request.Header.Set("If-Match", tc.etag)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != tc.code {
			t.Fatalf("archive status=%d body=%s", response.Code, response.Body.String())
		}
	}
	var sites, feeds int
	database.QueryRow(`SELECT COUNT(*) FROM sites WHERE archived=1 AND enabled=0`).Scan(&sites)
	database.QueryRow(`SELECT COUNT(*) FROM feeds WHERE archived=1 AND enabled=0`).Scan(&feeds)
	if sites != 1 || feeds != 1 {
		t.Fatalf("archived site=%d feeds=%d", sites, feeds)
	}
}

func TestArchiveKeepsDisabledSiteVisibleUntilPolicySyncSucceeds(t *testing.T) {
	database := openMigrated(t)
	now := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
	service, _ := rssservice.NewWithClock(database, func() time.Time { return now })
	site, err := service.CreateSite(context.Background(), rssservice.CreateSiteInput{ID: "site-sync", Name: "Site", BaseURL: "https://example.test", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	fail := true
	router := Router(Deps{Database: database, Now: func() time.Time { return now }, SyncPolicies: func(context.Context) error {
		if fail {
			return errors.New("offline")
		}
		return nil
	}})
	archive := func(etag string, want int) {
		request := httptest.NewRequest("DELETE", "/api/v1/sites/site-sync", nil)
		request.Header.Set("If-Match", fmt.Sprintf("%q", etag))
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != want {
			t.Fatalf("status=%d want=%d body=%s", response.Code, want, response.Body.String())
		}
		if want == 502 && (!strings.Contains(response.Body.String(), "QUI_UNREACHABLE") || !strings.Contains(response.Body.String(), "本地配置已停用")) {
			t.Fatalf("missing pending status: %s", response.Body.String())
		}
	}
	archive(site.UpdatedAt, 502)
	var enabled, archived int
	var revision string
	if err := database.QueryRow(`SELECT enabled,archived,updated_at FROM sites WHERE site_id=?`, site.ID).Scan(&enabled, &archived, &revision); err != nil {
		t.Fatal(err)
	}
	if enabled != 0 || archived != 0 || revision == site.UpdatedAt {
		t.Fatalf("failed archive state enabled=%d archived=%d revision=%q", enabled, archived, revision)
	}
	archive(site.UpdatedAt, 409)
	fail = false
	archive(revision, 200)
	if err := database.QueryRow(`SELECT archived FROM sites WHERE site_id=?`, site.ID).Scan(&archived); err != nil || archived != 1 {
		t.Fatalf("archive state=%d err=%v", archived, err)
	}
}

func TestArchiveKeepsDisabledRuleVisibleUntilPolicySyncSucceeds(t *testing.T) {
	database := openMigrated(t)
	stamp := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	if _, err := database.Exec(`INSERT INTO rules(rule_id,name,enabled,revision,state,definition_json,created_at,updated_at) VALUES('rule-sync','Rule',1,1,'PENDING','{}',?,?)`, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	fail := true
	router := Router(Deps{Database: database, SyncPolicies: func(context.Context) error {
		if fail {
			return errors.New("offline")
		}
		return nil
	}})
	archive := func(revision int, want int) {
		request := httptest.NewRequest("DELETE", "/api/v1/rules/rule-sync", nil)
		request.Header.Set("If-Match", fmt.Sprintf("%q", strconv.Itoa(revision)))
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != want {
			t.Fatalf("status=%d want=%d body=%s", response.Code, want, response.Body.String())
		}
	}
	archive(1, 502)
	var enabled, archived, revision int
	if err := database.QueryRow(`SELECT enabled,archived,revision FROM rules WHERE rule_id='rule-sync'`).Scan(&enabled, &archived, &revision); err != nil {
		t.Fatal(err)
	}
	if enabled != 0 || archived != 0 || revision != 2 {
		t.Fatalf("failed archive state enabled=%d archived=%d revision=%d", enabled, archived, revision)
	}
	archive(1, 409)
	fail = false
	archive(revision, 200)
	if err := database.QueryRow(`SELECT archived FROM rules WHERE rule_id='rule-sync'`).Scan(&archived); err != nil || archived != 1 {
		t.Fatalf("archive state=%d err=%v", archived, err)
	}
}

func TestSiteUpdateInvalidatesETagWithUnchangedClock(t *testing.T) {
	database := openMigrated(t)
	now := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
	service, _ := rssservice.NewWithClock(database, func() time.Time { return now })
	site, err := service.CreateSite(context.Background(), rssservice.CreateSiteInput{ID: "fixed-clock", Name: "Site", BaseURL: "https://example.test", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	router := Router(Deps{Database: database, Now: func() time.Time { return now }})
	for _, expected := range []int{200, 409} {
		request := httptest.NewRequest("PUT", "/api/v1/sites/fixed-clock", strings.NewReader(`{"enabled":false}`))
		request.Header.Set("If-Match", fmt.Sprintf("%q", site.UpdatedAt))
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != expected {
			t.Fatalf("status=%d want=%d body=%s", response.Code, expected, response.Body.String())
		}
	}
}

func TestGroupsRejectUnknownMembersAndStaleWrites(t *testing.T) {
	database := openMigrated(t)
	if _, err := database.Exec(`INSERT INTO instance_metadata(instance_id,name,enabled,updated_at) VALUES('2','Two',1,'now'),('7','Seven',1,'now')`); err != nil {
		t.Fatal(err)
	}
	router := Router(Deps{Database: database})
	for _, tc := range []struct {
		body   string
		status int
	}{
		{`{"id":"g","name":"Group","members":["missing"]}`, 422},
		{`{"id":"g","name":"Group","members":["2","7"]}`, 200},
		{`{"id":"g","name":"Group","members":["2"]}`, 409},
		{`{"id":"g","name":"Group","members":["2"],"revision":1}`, 200},
	} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest("POST", "/api/v1/instance-groups", strings.NewReader(tc.body)))
		if response.Code != tc.status {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	}
}
