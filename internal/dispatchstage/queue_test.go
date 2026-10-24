package dispatchstage

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
	"github.com/vertex-pro/vertex-pro/internal/dispatchcommit"
	"github.com/vertex-pro/vertex-pro/internal/qbresponse"
	"github.com/vertex-pro/vertex-pro/internal/racejobstore"
	"github.com/vertex-pro/vertex-pro/internal/ruleaggregate"
	"github.com/vertex-pro/vertex-pro/internal/startuprecover"
	"github.com/vertex-pro/vertex-pro/internal/torrentidentity"
	"github.com/vertex-pro/vertex-pro/internal/torrentparse"
)

type queueSender func(context.Context, *http.Request) (qbresponse.RawResponse, error)

func (f queueSender) Send(ctx context.Context, r *http.Request) (qbresponse.RawResponse, error) {
	return f(ctx, r)
}

func durableFixture(t *testing.T) (selectionFixture, *Stage, *atomic.Int32) {
	t.Helper()
	f := newSelectionFixture(t)
	e := f.evidence("qb-1", 1, 1<<30)
	s, _ := f.stage(t, "qb-1", e, e)
	s.config.DurableQueue = true
	s.config.Collect = func(context.Context) (Evidence, func(), error) { return e, nil, nil }
	s.config.CollectForSelection = s.config.Collect
	calls := &atomic.Int32{}
	s.config.Sender = queueSender(func(context.Context, *http.Request) (qbresponse.RawResponse, error) {
		calls.Add(1)
		return qbresponse.RawResponse{StatusCode: 200, Body: []byte("Ok.")}, nil
	})
	return f, s, calls
}
func allocateQueue(t *testing.T, s *Stage) {
	t.Helper()
	result, failures, err := s.Run(context.Background())
	if err != nil || len(failures) > 0 || len(result) == 0 {
		t.Fatalf("allocation=%v failures=%v err=%v", result, failures, err)
	}
}

func TestDurableQueueRecoversUnsentButNeverReplaysStarted(t *testing.T) {
	for _, phase := range []string{"queued", "claimed", "started", "returned"} {
		t.Run(phase, func(t *testing.T) {
			f, s, calls := durableFixture(t)
			allocateQueue(t, s)
			if calls.Load() != 0 {
				t.Fatal("allocation performed HTTP")
			}
			if phase == "claimed" {
				s.active["attempt-job-1"] = true
			}
			if phase == "started" {
				_, err := dispatchcommit.Begin(context.Background(), f.database, dispatchcommit.BeginRequest{AttemptID: "attempt-job-1", SubmitIntentID: "intent-job-1", WaiterID: "waiter-attempt-job-1", Now: f.now})
				if err != nil {
					t.Fatal(err)
				}
			}
			if phase == "returned" {
				if worked, err := s.SendNext(context.Background()); err != nil || !worked {
					t.Fatalf("send=%v %v", worked, err)
				}
			}
			var seq int
			var name, path string
			if err := f.database.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &path); err != nil {
				t.Fatal(err)
			}
			if err := f.database.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := db.Open(context.Background(), db.Config{Path: filepath.Clean(path)})
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			if _, err := startuprecover.Recover(context.Background(), reopened, startuprecover.Input{Now: f.now, ConfirmationWindow: 10 * time.Minute, ReconciliationWindow: time.Hour}); err != nil {
				t.Fatal(err)
			}
			restored, err := New(reopened, s.config)
			if err != nil {
				t.Fatal(err)
			}
			// No notification survives the restart; draining durable state is sufficient.
			worked, err := restored.SendNext(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			want := int32(1)
			if phase == "started" {
				want = 0
			}
			if calls.Load() != want || worked != (phase == "queued" || phase == "claimed") {
				t.Fatalf("phase=%s worked=%v calls=%d", phase, worked, calls.Load())
			}
			if worked, err := restored.SendNext(context.Background()); err != nil || worked {
				t.Fatalf("duplicate drain=%v %v", worked, err)
			}
		})
	}
}

func TestQueueChecksFreshBaselineRulesAndExpiry(t *testing.T) {
	for _, change := range []string{"baseline", "disabled", "revision", "expired"} {
		t.Run(change, func(t *testing.T) {
			f, s, calls := durableFixture(t)
			allocateQueue(t, s)
			switch change {
			case "baseline":
				e := f.evidence("qb-1", 2, 1<<30)
				if _, err := f.database.Exec(`INSERT INTO sync_snapshots(instance_id,sync_generation,snapshot_revision,rid,snapshot_digest,snapshot_path,is_complete,created_at) VALUES('qb-1',1,2,2,'qb-1-digest-2','new.json',1,'now'); UPDATE sync_instance_state SET snapshot_revision=2,snapshot_digest='qb-1-digest-2'`); err != nil {
					t.Fatal(err)
				}
				s.config.Collect = func(context.Context) (Evidence, func(), error) { return e, nil, nil }
			case "disabled":
				if _, err := f.database.Exec(`UPDATE rules SET enabled=0`); err != nil {
					t.Fatal(err)
				}
			case "expired":
				s.config.Now = func() time.Time { return f.now.Add(2 * time.Hour) }
			case "revision":
				if _, err := f.database.Exec(`UPDATE rules SET revision=revision+1`); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.SendNext(context.Background()); err != nil {
				t.Fatal(err)
			}
			var queue, rev int
			var state string
			if err := f.database.QueryRow(`SELECT (SELECT count(*) FROM dispatch_queue),pre_add_snapshot_revision FROM submit_intents`).Scan(&queue, &rev); err != nil {
				t.Fatal(err)
			}
			if err := f.database.QueryRow(`SELECT reservation_state FROM capacity_reservations`).Scan(&state); err != nil {
				t.Fatal(err)
			}
			if queue != 0 {
				t.Fatal("queue was not cleaned")
			}
			if change == "baseline" {
				if calls.Load() != 1 || rev != 2 {
					t.Fatalf("calls=%d revision=%d", calls.Load(), rev)
				}
			} else if calls.Load() != 0 || state != "RELEASED" {
				t.Fatalf("calls=%d reservation=%s", calls.Load(), state)
			}
		})
	}
}

func cloneQueuedJob(t *testing.T, f selectionFixture, id, target string) {
	t.Helper()
	body := []byte("d4:infod6:lengthi42e4:name8:" + fmt.Sprintf("%08s", id) + "12:piece lengthi16384e6:pieces20:01234567890123456789ee")
	parsed, err := torrentparse.Parse(body, torrentparse.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	identity, err := torrentidentity.FromTorrent(parsed)
	if err != nil {
		t.Fatal(err)
	}
	decision := ruleaggregate.Result{Winner: ruleaggregate.Match{RuleID: "rule", RuleRevision: 1, TargetMode: "FIXED_INSTANCE", TargetInstanceID: target}}
	raw, _ := json.Marshal(decision)
	if _, err := f.database.Exec(`INSERT INTO race_jobs(job_id,source_id,identity_key,job_state,participation_deadline,observation_deadline,created_at,updated_at) VALUES(?,?,?,'DISCOVERED',?,?,'now','now')`, id, "site/"+id, identity.V1, f.now.Add(time.Hour).Format(time.RFC3339Nano), f.now.Add(2*time.Hour).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.database.Exec(`INSERT INTO job_rule_snapshots(job_id,aggregation_json,content_size,created_at) VALUES(?,?,42,'now')`, id, string(raw)); err != nil {
		t.Fatal(err)
	}
	if err := racejobstore.SaveLegacyArtifact(context.Background(), f.database, id, body, f.now); err != nil {
		t.Fatal(err)
	}
}

func TestSlowAddDoesNotHoldOtherSlotsOrInstanceAllocation(t *testing.T) {
	f, a, _ := durableFixture(t)
	started, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	a.config.Sender = queueSender(func(ctx context.Context, r *http.Request) (qbresponse.RawResponse, error) {
		select {
		case <-started:
		default:
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return qbresponse.RawResponse{}, ctx.Err()
			}
		}
		return qbresponse.RawResponse{StatusCode: 200, Body: []byte("Ok.")}, nil
	})
	allocateQueue(t, a)
	done := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _, err := a.SendNext(ctx); done <- err }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("A never entered HTTP")
	}
	cloneQueuedJob(t, f, "second", "qb-1")
	cloneQueuedJob(t, f, "third", "qb-2")
	cloneQueuedJob(t, f, "fourth", "qb-3")
	b, _ := f.stage(t, "qb-2", f.evidence("qb-2", 1, 1<<30), f.evidence("qb-2", 2, 1<<30))
	c, _ := f.stage(t, "qb-3", f.evidence("qb-3", 1, 1<<30), f.evidence("qb-3", 2, 1<<30))
	for _, s := range []*Stage{b, c} {
		s.config.DurableQueue = true
		e := f.evidence(s.config.Storage.InstanceID, 1, 1<<30)
		s.config.Collect = func(context.Context) (Evidence, func(), error) { return e, nil, nil }
		s.config.CollectForSelection = s.config.Collect
	}
	Connect(a, b, c)
	independent := make(chan error, 1)
	go func() {
		for _, s := range []*Stage{a, b, c} {
			_, failures, err := s.Run(ctx)
			if err != nil || len(failures) > 0 {
				independent <- fmt.Errorf("allocate %v %v", failures, err)
				return
			}
			if _, err := s.SendNext(ctx); err != nil {
				independent <- err
				return
			}
		}
		independent <- nil
	}()
	select {
	case err := <-independent:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("slow A held another sender or allocation lock")
	}
	// Three independent requests finish while A still owns its first HTTP slot.
	var awaiting int
	if err := f.database.QueryRow(`SELECT count(*) FROM placement_attempts WHERE attempt_state='AWAITING_CONFIRMATION'`).Scan(&awaiting); err != nil || awaiting != 3 {
		t.Fatalf("awaiting=%d err=%v", awaiting, err)
	}
	cancel()
	if err := <-done; err == nil { /* canceled fixture may still return Ok; both outcomes retain ownership */
	}
}
