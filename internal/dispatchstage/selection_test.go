package dispatchstage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/qbprofile"
	"github.com/vertex-pro/vertex-pro/internal/racejobstore"
	"github.com/vertex-pro/vertex-pro/internal/ruleaggregate"
	"github.com/vertex-pro/vertex-pro/internal/snapshot"
	"github.com/vertex-pro/vertex-pro/internal/syncmerge"
	"github.com/vertex-pro/vertex-pro/internal/torrentdownload"
)

func TestGroupSelectionUsesCachedEvidenceButDispatchRecollects(t *testing.T) {
	f := newSelectionFixture(t)
	first, firstCalls := f.stage(t, "qb-1", f.evidence("qb-1", 1, 1024), f.evidence("qb-1", 2, 1024))
	second, secondCalls := f.stage(t, "qb-2", f.evidence("qb-2", 1, 0), f.evidence("qb-2", 2, 1024))
	Connect(first, second)
	results, failures, err := first.Run(context.Background())
	if err != nil || len(failures) != 0 || len(results) != 1 {
		t.Fatalf("results=%v failures=%v err=%v", results, failures, err)
	}
	firstCalls.check(t, 1, 1, 1)
	secondCalls.check(t, 1, 0, 0)
	var revision int64
	var digest, instance string
	if err := f.database.QueryRow(`SELECT instance_id,pre_add_snapshot_revision,pre_add_snapshot_digest FROM submit_intents`).Scan(&instance, &revision, &digest); err != nil {
		t.Fatal(err)
	}
	if instance != "qb-1" || revision != 2 || digest != "qb-1-digest-2" {
		t.Fatalf("Add used selection evidence: instance=%s revision=%d digest=%s", instance, revision, digest)
	}
}

func TestGroupSelectionExistingHashWithUnknownCapacityNeverReroutes(t *testing.T) {
	for _, unknown := range []string{"torrent liability", "physical space"} {
		t.Run(unknown, func(t *testing.T) {
			f := newSelectionFixture(t)
			cached := f.evidence("qb-1", 1, 1024)
			live := f.evidence("qb-1", 2, 1024)
			for _, evidence := range []*Evidence{&cached, &live} {
				left := int64(42)
				torrent := syncmerge.Torrent{ID: f.hash, AmountLeftState: "KNOWN", AmountLeft: &left}
				if unknown == "torrent liability" {
					torrent.AmountLeftState, torrent.AmountLeft = "UNKNOWN", nil
				} else {
					delete(evidence.Snapshot.ServerState, "free_space_on_disk")
				}
				evidence.Snapshot.Torrents[f.hash] = torrent
			}
			first, firstCalls := f.stage(t, "qb-1", cached, live)
			second, secondCalls := f.stage(t, "qb-2", f.evidence("qb-2", 1, 1024), f.evidence("qb-2", 2, 1024))
			Connect(first, second)
			results, failures, err := first.Run(context.Background())
			if err != nil || len(results) != 0 || len(failures) != 1 || !strings.Contains(failures[0], "unknown") {
				t.Fatalf("results=%v failures=%v err=%v", results, failures, err)
			}
			firstCalls.check(t, 1, 1, 0)
			secondCalls.check(t, 1, 0, 0)
			f.checkNoReservation(t)
		})
	}
}

func TestGroupSelectionFreshCapacityCanRejectCachedCandidate(t *testing.T) {
	f := newSelectionFixture(t)
	first, firstCalls := f.stage(t, "qb-1", f.evidence("qb-1", 1, 1024), f.evidence("qb-1", 2, 41))
	second, secondCalls := f.stage(t, "qb-2", f.evidence("qb-2", 1, 0), f.evidence("qb-2", 2, 1024))
	Connect(first, second)
	results, failures, err := first.Run(context.Background())
	if err != nil || len(results) != 0 || len(failures) != 1 || !strings.Contains(failures[0], "INSUFFICIENT_EFFECTIVE_FREE") {
		t.Fatalf("results=%v failures=%v err=%v", results, failures, err)
	}
	firstCalls.check(t, 1, 1, 0)
	secondCalls.check(t, 1, 0, 0)
	f.checkNoReservation(t)
}

func TestGroupSelectionFinalHashRecheckPreventsAdd(t *testing.T) {
	f := newSelectionFixture(t)
	live := f.evidence("qb-1", 2, 1024)
	left := int64(42)
	live.Snapshot.Torrents[f.hash] = syncmerge.Torrent{ID: f.hash, AmountLeftState: "KNOWN", AmountLeft: &left}
	first, firstCalls := f.stage(t, "qb-1", f.evidence("qb-1", 1, 1024), live)
	second, secondCalls := f.stage(t, "qb-2", f.evidence("qb-2", 1, 0), f.evidence("qb-2", 2, 1024))
	Connect(first, second)
	results, failures, err := first.Run(context.Background())
	if err != nil || len(results) != 0 || len(failures) != 0 {
		t.Fatalf("results=%v failures=%v err=%v", results, failures, err)
	}
	firstCalls.check(t, 1, 1, 0)
	secondCalls.check(t, 1, 0, 0)
	var identity, dispatch string
	var revision int64
	if err := f.database.QueryRow(`SELECT pre_add_identity_fact,dispatch_state,pre_add_snapshot_revision FROM submit_intents`).Scan(&identity, &dispatch, &revision); err != nil {
		t.Fatal(err)
	}
	if identity != "PRESENT" || dispatch != "NOT_STARTED" || revision != 2 {
		t.Fatalf("identity=%s dispatch=%s revision=%d", identity, dispatch, revision)
	}
}

type selectionFixture struct {
	database *sql.DB
	now      time.Time
	hash     string
}

func newSelectionFixture(t *testing.T) selectionFixture {
	t.Helper()
	f := selectionFixture{database: deadlineDatabase(t), now: time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)}
	body := []byte("d4:infod6:lengthi42e4:name8:file.bin12:piece lengthi16384e6:pieces20:01234567890123456789ee")
	decision := ruleaggregate.Result{Winner: ruleaggregate.Match{RuleID: "rule", TargetMode: "INSTANCE_GROUP", TargetGroupID: "group-1"}}
	seedDeadlineJob(t, f.database, body, "https://example.test/source.torrent", f.now.Add(time.Hour), decision)
	if err := racejobstore.SaveLegacyArtifact(context.Background(), f.database, "job-1", body, f.now); err != nil {
		t.Fatal(err)
	}
	if _, err := f.database.Exec(`INSERT INTO instance_groups(group_id,name,members_json,updated_at) VALUES('group-1','Group','["qb-1","qb-2"]',?)`, f.now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if err := f.database.QueryRow(`SELECT identity_key FROM race_jobs WHERE job_id='job-1'`).Scan(&f.hash); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f selectionFixture) evidence(instance string, revision uint64, free int64) Evidence {
	return Evidence{
		Record:      snapshot.Record{InstanceID: instance, Generation: 1, Revision: revision, RID: revision, Digest: fmt.Sprintf("%s-digest-%d", instance, revision), Complete: true},
		Snapshot:    syncmerge.Snapshot{RID: revision, Torrents: map[string]syncmerge.Torrent{}, ServerState: map[string]any{"free_space_on_disk": free}},
		Preferences: map[string]any{"save_path": "/data", "temp_path_enabled": false},
		CollectedAt: f.now,
	}
}

type selectionCalls struct {
	selection, live, releases int
	sender                    deadlineSender
}

func (calls *selectionCalls) check(t *testing.T, selection, live, sends int) {
	t.Helper()
	if calls.selection != selection || calls.live != live || calls.sender.calls != sends || calls.releases != selection+live {
		t.Fatalf("selection=%d live=%d sends=%d releases=%d; want %d %d %d %d", calls.selection, calls.live, calls.sender.calls, calls.releases, selection, live, sends, selection+live)
	}
}

func (f selectionFixture) stage(t *testing.T, instance string, cached, live Evidence) (*Stage, *selectionCalls) {
	t.Helper()
	publish := func(e Evidence) error {
		stamp := f.now.Format(time.RFC3339Nano)
		if _, err := f.database.Exec(`INSERT INTO sync_instance_state(instance_id,sync_state,sync_generation,rid,snapshot_revision,snapshot_digest,updated_at) VALUES(?,'READY',?,?,?,?,?) ON CONFLICT(instance_id) DO UPDATE SET sync_state='READY',sync_generation=excluded.sync_generation,rid=excluded.rid,snapshot_revision=excluded.snapshot_revision,snapshot_digest=excluded.snapshot_digest,updated_at=excluded.updated_at`, instance, e.Record.Generation, e.Record.RID, e.Record.Revision, e.Record.Digest, stamp); err != nil {
			return err
		}
		_, err := f.database.Exec(`INSERT INTO sync_snapshots(instance_id,sync_generation,snapshot_revision,rid,snapshot_digest,snapshot_path,is_complete,created_at) VALUES(?,?,?,?,?,'fixture.json',1,?)`, instance, e.Record.Generation, e.Record.Revision, e.Record.RID, e.Record.Digest, stamp)
		return err
	}
	if err := publish(cached); err != nil {
		t.Fatal(err)
	}
	downloader, err := torrentdownload.New(torrentdownload.Config{AllowedHosts: []string{"example.test"}})
	if err != nil {
		t.Fatal(err)
	}
	calls := &selectionCalls{}
	stage, err := New(f.database, Config{
		Storage:    StorageConfig{InstanceID: instance, StorageTargetID: "target-" + instance, Path: "/data"},
		Downloader: downloader, Sender: &calls.sender,
		Profile: qbprofile.Profile{ProfileVerificationState: "VERIFIED", AddStartField: "stopped", WebAPIVersion: "2.11.0"},
		Now:     func() time.Time { return f.now },
		CollectForSelection: func(context.Context) (Evidence, func(), error) {
			calls.selection++
			return cached, func() { calls.releases++ }, nil
		},
		Collect: func(context.Context) (Evidence, func(), error) {
			calls.live++
			return live, func() { calls.releases++ }, publish(live)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return stage, calls
}

func (f selectionFixture) checkNoReservation(t *testing.T) {
	t.Helper()
	for _, table := range []string{"placement_attempts", "capacity_reservations", "submit_intents"} {
		var count int
		if err := f.database.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s has %d rows after rejected final check", table, count)
		}
	}
}
