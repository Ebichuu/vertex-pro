package schedulercycle

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/matchpipeline"
	"github.com/vertex-pro/vertex-pro/internal/racejob"
	"github.com/vertex-pro/vertex-pro/internal/racejobstore"
	"github.com/vertex-pro/vertex-pro/internal/rssfetch"
	"github.com/vertex-pro/vertex-pro/internal/rsssource"
	"github.com/vertex-pro/vertex-pro/internal/rsssourcedb"
	"github.com/vertex-pro/vertex-pro/internal/ruleaggregate"
	"github.com/vertex-pro/vertex-pro/internal/ruleservice"
	"github.com/vertex-pro/vertex-pro/internal/torrentdownload"
)

func recoveryCycle(t *testing.T, database *sql.DB, server string, now *time.Time, rules *ruleservice.Service) *Cycle {
	t.Helper()
	store, _ := rsssourcedb.New(database)
	dedupe, _ := rsssource.New(store)
	fetcher, _ := rssfetch.New(nil, 1<<20)
	downloader, err := torrentdownload.New(torrentdownload.Config{AllowedHosts: []string{strings.TrimPrefix(server, "http://")}})
	if err != nil {
		t.Fatal(err)
	}
	jobs, _ := racejobstore.New(database)
	creator, _ := racejob.New(jobs, nil)
	cycle, err := New(fetcher, downloader, dedupe, matchpipeline.Deps{Rules: rules, JobCreator: creator, Now: func() time.Time { return *now }, MatchFactory: func(rule ruleservice.Rule) (ruleaggregate.Match, error) {
		return ruleaggregate.Match{TargetMode: "FIXED_INSTANCE", TargetInstanceID: "2", ParticipationWindow: time.Hour}, nil
	}}, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	return cycle
}

func TestPendingArtifactsRecoverOn304AndFeedFailureAfterRestart(t *testing.T) {
	for _, feedStatus := range []int{304, 503} {
		for _, failure := range []string{"network", "parse"} {
			t.Run(fmt.Sprintf("%d-%s", feedStatus, failure), func(t *testing.T) {
				database := cycleDatabase(t)
				rules := ruleserviceFor(t, database)
				now := time.Now()
				phase := 0
				requests := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/feed" {
						if phase > 0 {
							w.WriteHeader(feedStatus)
							return
						}
						fmt.Fprintf(w, `<rss><channel><item><guid>x</guid><title>Linux</title><pubDate>Fri, 23 Oct 2026 00:00:00 +0000</pubDate><link>http://%s/item.torrent</link></item></channel></rss>`, r.Host)
						return
					}
					requests++
					w.Header().Set("Content-Type", "application/x-bittorrent")
					if phase == 0 {
						if failure == "network" {
							w.WriteHeader(503)
						} else {
							w.Write([]byte("invalid torrent"))
						}
						return
					}
					w.Write(fixtureTorrent("Linux"))
				}))
				defer server.Close()
				feed := FeedRef{SiteID: "site-1", FeedID: "feed-1", URL: server.URL + "/feed"}
				first, err := recoveryCycle(t, database, server.URL, &now, rules).Run(context.Background(), feed)
				if err != nil || first.Failed != 1 {
					t.Fatalf("initial %+v %v", first, err)
				}
				phase = 1
				cycle := recoveryCycle(t, database, server.URL, &now, rules)
				cycle.Run(context.Background(), feed)
				if requests != 1 {
					t.Fatal("retry ignored backoff")
				}
				now = now.Add(time.Minute)
				second, err := cycle.Run(context.Background(), feed)
				if (err != nil) != (feedStatus == 503) || second.Matched != 1 || requests != 2 {
					t.Fatalf("recovery %+v %v requests=%d", second, err, requests)
				}
				var state string
				database.QueryRow(`SELECT state FROM rss_source_consumers`).Scan(&state)
				if state != "DONE" {
					t.Fatal(state)
				}
				now = now.Add(time.Hour)
				cycle.Run(context.Background(), feed)
				if requests != 2 {
					t.Fatal("completed source replayed")
				}
			})
		}
	}
}

func TestOverlappingFeedsEvaluateIndependentlyAndShareOneJob(t *testing.T) {
	database := cycleDatabase(t)
	now := time.Now()
	rules, _ := ruleservice.New(database, nil)
	if _, err := database.Exec(`INSERT INTO feeds(feed_id,site_id,name,feed_url,created_at,updated_at) VALUES('feed-2','site-1','Second','https://site.example/second','2026-01-01','2026-01-01')`); err != nil {
		t.Fatal(err)
	}
	definition := map[string]any{"version": 1, "root": map[string]any{"kind": "leaf", "field": "rss", "operator": "equals", "value": map[string]any{"type": "string", "string": "feed-2"}}}
	raw, _ := json.Marshal(definition)
	if _, err := rules.Create(context.Background(), ruleservice.CreateInput{SiteID: "site-1", Name: "Second feed", Enabled: true, Priority: 10, Definition: raw}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/item.torrent" {
			w.Header().Set("Content-Type", "application/x-bittorrent")
			w.Write(fixtureTorrent("Linux"))
			return
		}
		fmt.Fprintf(w, `<rss><channel><item><guid>same</guid><title>Linux</title><link>http://%s/item.torrent</link></item></channel></rss>`, r.Host)
	}))
	defer server.Close()
	cycle := recoveryCycle(t, database, server.URL, &now, &rules)
	first, err := cycle.Run(context.Background(), FeedRef{SiteID: "site-1", FeedID: "feed-1", URL: server.URL + "/feed"})
	if err != nil || first.Matched != 0 {
		t.Fatal(first, err)
	}
	second, err := cycle.Run(context.Background(), FeedRef{SiteID: "site-1", FeedID: "feed-2", URL: server.URL + "/feed"})
	if err != nil || second.Matched != 1 || !second.Outcomes[0].JobCreated {
		t.Fatal(second, err)
	}
	var n int
	database.QueryRow(`SELECT COUNT(*) FROM race_jobs`).Scan(&n)
	if n != 1 {
		t.Fatal("expected one job", n)
	}
	database.QueryRow(`SELECT COUNT(*) FROM rss_source_consumers WHERE state='DONE'`).Scan(&n)
	if n != 2 {
		t.Fatal("each consumer must finish", n)
	}
	job := second.Outcomes[0].Job
	// A network-uncertain existing Add chain must be reused unchanged. Recovery
	// of source consumption is not permission to retry the Add itself.
	if _, err := database.Exec(`INSERT INTO placement_attempts(attempt_id,job_id,identity_key,attempt_no,instance_id,storage_target_id,attempt_state,dispatch_state,side_effect_fact,allocation_epoch,reserved_bytes,started_at) VALUES('uncertain',?,?,1,'2','target','UNKNOWN','STARTED','POSSIBLE_EFFECT',1,1,'2026-01-01T00:00:00Z')`, job.ID, job.IdentityKey); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO submit_intents(submit_intent_id,attempt_id,instance_id,identity_key,request_fingerprint,target_fingerprint,dispatch_state,pre_add_sync_generation,pre_add_snapshot_revision,pre_add_snapshot_digest,pre_add_identity_fact,created_at,dispatch_started_at) VALUES('uncertain-intent','uncertain','2',?,'fp','target','STARTED',1,1,'digest','ABSENT','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`, job.IdentityKey); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE race_jobs SET job_state='UNKNOWN',current_attempt_id='uncertain' WHERE job_id=?`, job.ID); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash after creating the job but before committing DONE. Recovery
	// must reuse the existing chain and its immutable decision, never create Add.
	if _, err := database.Exec(`UPDATE rss_source_consumers SET state='PENDING' WHERE feed_id='feed-2'`); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	again, err := recoveryCycle(t, database, server.URL, &now, &rules).Run(context.Background(), FeedRef{SiteID: "site-1", FeedID: "feed-2", URL: server.URL + "/feed"})
	if err != nil || len(again.Outcomes) != 1 || !again.Outcomes[0].JobDuplicate {
		t.Fatal(again, err)
	}
	if again.Outcomes[0].Job.JobState != "UNKNOWN" || again.Outcomes[0].Job.CurrentAttemptID == nil || *again.Outcomes[0].Job.CurrentAttemptID != "uncertain" {
		t.Fatal("UNKNOWN chain was changed")
	}
	database.QueryRow(`SELECT COUNT(*) FROM race_jobs`).Scan(&n)
	if n != 1 {
		t.Fatal("duplicate job", n)
	}
}

func TestBaselineRemainsSkippedAcrossRestart(t *testing.T) {
	database := cycleDatabase(t)
	rules := ruleserviceFor(t, database)
	now := time.Now()
	artifactRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/item.torrent" {
			artifactRequests++
			w.Header().Set("Content-Type", "application/x-bittorrent")
			w.Write(fixtureTorrent("Linux"))
			return
		}
		fmt.Fprintf(w, `<rss><channel><item><guid>baseline</guid><title>Linux</title><link>http://%s/item.torrent</link></item></channel></rss>`, r.Host)
	}))
	defer server.Close()
	feed := FeedRef{SiteID: "site-1", FeedID: "feed-1", URL: server.URL + "/feed", BaselineOnly: true}
	first, err := recoveryCycle(t, database, server.URL, &now, rules).Run(context.Background(), feed)
	if err != nil || first.Skipped != 1 {
		t.Fatal(first, err)
	}
	feed.BaselineOnly = false
	now = now.Add(time.Hour)
	next, err := recoveryCycle(t, database, server.URL, &now, rules).Run(context.Background(), feed)
	if err != nil || next.Matched != 0 || artifactRequests != 0 {
		t.Fatal("baseline replayed", next, err)
	}
}
