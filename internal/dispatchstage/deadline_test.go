package dispatchstage

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
	"github.com/vertex-pro/vertex-pro/internal/qbprofile"
	"github.com/vertex-pro/vertex-pro/internal/qbresponse"
	"github.com/vertex-pro/vertex-pro/internal/ruleaggregate"
	"github.com/vertex-pro/vertex-pro/internal/torrentdownload"
	"github.com/vertex-pro/vertex-pro/internal/torrentidentity"
	"github.com/vertex-pro/vertex-pro/internal/torrentparse"
)

type deadlineSender struct{ calls int }

func (s *deadlineSender) Send(context.Context, *http.Request) (qbresponse.RawResponse, error) {
	s.calls++
	return qbresponse.RawResponse{StatusCode: http.StatusOK, Body: []byte("Ok.")}, nil
}

func TestStageExpiresBeforeOrDuringExternalPreparation(t *testing.T) {
	for _, phase := range []string{"queued", "reserved", "download", "collection", "group_collection"} {
		t.Run(phase, func(t *testing.T) {
			deadline := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
			var clock atomic.Int64
			clock.Store(deadline.Add(-time.Second).UnixNano())
			if phase == "queued" || phase == "reserved" {
				clock.Store(deadline.UnixNano())
			}
			artifact := []byte("d4:infod6:lengthi42e4:name8:file.bin12:piece lengthi16384e6:pieces20:01234567890123456789ee")
			var downloads atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				downloads.Add(1)
				if phase == "download" {
					clock.Store(deadline.UnixNano())
				}
				w.Header().Set("Content-Type", "application/x-bittorrent")
				_, _ = w.Write(artifact)
			}))
			defer server.Close()
			parsedURL, _ := url.Parse(server.URL)
			downloader, err := torrentdownload.New(torrentdownload.Config{Client: server.Client(), AllowedHosts: []string{parsedURL.Host}})
			if err != nil {
				t.Fatal(err)
			}
			database := deadlineDatabase(t)
			decision := ruleaggregate.Result{Winner: ruleaggregate.Match{RuleID: "rule", TargetMode: "FIXED_INSTANCE", TargetInstanceID: "qb-1"}}
			if phase == "group_collection" {
				decision.Winner.TargetMode = "INSTANCE_GROUP"
				decision.Winner.TargetGroupID = "group-1"
				if _, err := database.Exec(`INSERT INTO instance_groups(group_id,name,members_json,updated_at) VALUES('group-1','Group','["qb-1"]','now')`); err != nil {
					t.Fatal(err)
				}
			}
			seedDeadlineJob(t, database, artifact, server.URL, deadline, decision)
			if phase == "reserved" {
				for _, query := range []string{
					`INSERT INTO storage_targets(storage_target_id,instance_id,path,storage_verification_state,created_at,updated_at) VALUES('target-1','qb-1','/data','VERIFIED','now','now')`,
					`INSERT INTO placement_attempts(attempt_id,job_id,identity_key,attempt_no,instance_id,storage_target_id,attempt_state,dispatch_state,allocation_epoch,reserved_bytes,started_at) SELECT 'attempt-1',job_id,identity_key,1,'qb-1','target-1','RESERVED','NOT_STARTED',1,42,'now' FROM race_jobs`,
					`INSERT INTO capacity_reservations(reservation_id,attempt_id,storage_target_id,reserved_bytes,allocation_epoch,reservation_state) VALUES('reservation-1','attempt-1','target-1',42,1,'ACTIVE')`,
					`INSERT INTO submit_intents(submit_intent_id,attempt_id,instance_id,identity_key,request_fingerprint,target_fingerprint,dispatch_state,pre_add_sync_generation,pre_add_snapshot_revision,pre_add_snapshot_digest,pre_add_identity_fact,created_at) SELECT 'intent-1','attempt-1','qb-1',identity_key,'request','target','NOT_STARTED',1,1,'digest','ABSENT','now' FROM race_jobs`,
					`UPDATE race_jobs SET job_state='RESERVED',current_attempt_id='attempt-1'`,
				} {
					if _, err := database.Exec(query); err != nil {
						t.Fatal(err)
					}
				}
			}
			sender := &deadlineSender{}
			collected, released := 0, 0
			stage, err := New(database, Config{
				Storage:    StorageConfig{InstanceID: "qb-1", StorageTargetID: "target-1", Path: "/data"},
				Downloader: downloader, Sender: sender, Profile: qbprofile.Profile{ProfileVerificationState: "VERIFIED"},
				Now: func() time.Time { return time.Unix(0, clock.Load()).UTC() },
				Collect: func(context.Context) (Evidence, func(), error) {
					collected++
					clock.Store(deadline.UnixNano())
					return Evidence{}, func() { released++ }, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			if phase == "group_collection" {
				Connect(stage)
			}
			results, failures, err := stage.Run(context.Background())
			if err != nil || len(results) != 0 || len(failures) != 0 {
				t.Fatalf("results=%v failures=%v error=%v", results, failures, err)
			}
			var state, reason string
			var attempts int
			if err := database.QueryRow(`SELECT job_state,placement_wait_reason,(SELECT count(*) FROM placement_attempts) FROM race_jobs`).Scan(&state, &reason, &attempts); err != nil {
				t.Fatal(err)
			}
			wantAttempts := 0
			if phase == "reserved" {
				wantAttempts = 1
				var reserve string
				if err := database.QueryRow(`SELECT reservation_state FROM capacity_reservations`).Scan(&reserve); err != nil {
					t.Fatal(err)
				}
				if reserve != "RELEASED" {
					t.Fatalf("reservation=%s", reserve)
				}
			}
			if state != "EXPIRED" || reason != "PARTICIPATION_DEADLINE_EXPIRED" || attempts != wantAttempts || sender.calls != 0 {
				t.Fatalf("state=%s reason=%s attempts=%d sends=%d", state, reason, attempts, sender.calls)
			}
			wantDownloads, wantCollect := int64(1), 0
			if phase == "queued" || phase == "reserved" || phase == "group_collection" {
				wantDownloads = 0
			}
			if phase == "collection" || phase == "group_collection" {
				wantCollect = 1
			}
			if downloads.Load() != wantDownloads || collected != wantCollect || released != wantCollect {
				t.Fatalf("downloads=%d collected=%d released=%d", downloads.Load(), collected, released)
			}
			if _, failures, err := stage.Run(context.Background()); err != nil || len(failures) != 0 {
				t.Fatalf("repeat failures=%v error=%v", failures, err)
			}
			if downloads.Load() != wantDownloads || collected != wantCollect {
				t.Fatal("expired job was prepared again")
			}
		})
	}
}

func deadlineDatabase(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.Open(context.Background(), db.Config{Path: filepath.Join(t.TempDir(), "deadline.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	runner, err := db.DefaultRunner()
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Apply(context.Background(), database); err != nil {
		t.Fatal(err)
	}
	return database
}

func seedDeadlineJob(t *testing.T, database *sql.DB, artifact []byte, sourceURL string, deadline time.Time, decision ruleaggregate.Result) {
	t.Helper()
	parsed, err := torrentparse.Parse(artifact, torrentparse.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	identity, err := torrentidentity.FromTorrent(parsed)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(decision)
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`INSERT INTO sites(site_id,name,base_url,enabled,created_at,updated_at) VALUES('site','Site','https://example.test',1,'now','now')`,
		`INSERT INTO rules(rule_id,site_id,name,enabled,definition_json,created_at,updated_at) VALUES('rule','site','Rule',1,'{}','now','now')`,
	} {
		if _, err := database.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.Exec(`INSERT INTO rss_sources(site_id,source_key,guid,url,title,fingerprint,first_seen_at,updated_at) VALUES('site','source','guid',?,'Torrent','fp','now','now')`, sourceURL); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO race_jobs(job_id,source_id,identity_key,job_state,participation_deadline,observation_deadline,created_at,updated_at) VALUES('job-1','site/source',?,'DISCOVERED',?,?,'now','now')`, identity.V1, deadline.Format(time.RFC3339Nano), deadline.Add(time.Hour).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO job_rule_snapshots(job_id,aggregation_json,content_size,created_at) VALUES('job-1',?,42,'now')`, string(raw)); err != nil {
		t.Fatal(err)
	}
}
