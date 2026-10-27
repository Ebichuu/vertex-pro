package deletionsync

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
	"github.com/vertex-pro/vertex-pro/internal/deletionpolicy"
	"github.com/vertex-pro/vertex-pro/internal/qui/control"
	"github.com/vertex-pro/vertex-pro/internal/syncmerge"
)

// fakeQui stores automations the way qui does: by ID, returning what it
// persisted. mutate lets a test change what qui persists on save.
type fakeQui struct {
	mu          sync.Mutex
	next        int
	automations []control.Automation
	saves       int
	previews    int
	mutate      func(*control.Automation)
}

func roundTrip(automation control.Automation) control.Automation {
	encoded, _ := json.Marshal(automation)
	var result control.Automation
	_ = json.Unmarshal(encoded, &result)
	return result
}

func (fake *fakeQui) Automations(context.Context, string) ([]control.Automation, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	result := make([]control.Automation, len(fake.automations))
	for index, automation := range fake.automations {
		result[index] = roundTrip(automation)
	}
	return result, nil
}

func (fake *fakeQui) SaveAutomation(_ context.Context, _ string, automation control.Automation) (control.Automation, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.saves++
	stored := roundTrip(automation)
	if fake.mutate != nil {
		fake.mutate(&stored)
	}
	if stored.ID == 0 {
		fake.next++
		stored.ID = fake.next
		fake.automations = append(fake.automations, stored)
		return roundTrip(stored), nil
	}
	for index := range fake.automations {
		if fake.automations[index].ID == stored.ID {
			fake.automations[index] = stored
			return roundTrip(stored), nil
		}
	}
	return control.Automation{}, errors.New("automation not found")
}

func (fake *fakeQui) PreviewDelete(_ context.Context, _ string, automation control.Automation, _, _ int, _ string) (control.PreviewResult, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.previews++
	return control.PreviewResult{TotalMatches: 2, Examples: []control.PreviewTorrent{{Hash: "aa", Name: automation.Name}}}, nil
}

func (fake *fakeQui) byName(t *testing.T, name string) control.Automation {
	t.Helper()
	fake.mu.Lock()
	defer fake.mu.Unlock()
	for _, automation := range fake.automations {
		if automation.Name == name {
			return roundTrip(automation)
		}
	}
	t.Fatalf("no automation %s", name)
	return control.Automation{}
}

type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }

func testStore(t *testing.T) (Store, *clock) {
	t.Helper()
	database, err := db.Open(context.Background(), db.Config{Path: filepath.Join(t.TempDir(), "deletion.db"), MaxOpenConns: 1, MaxIdleConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	runner, _ := db.DefaultRunner()
	if err := runner.Apply(context.Background(), database); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	c := &clock{now: time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)}
	return Store{DB: database, Now: c.Now}, c
}

func brushSpec() deletionpolicy.PolicySpec {
	return deletionpolicy.PolicySpec{DeleteMode: deletionpolicy.WithFilesPreserveCrossSeeds, Triggers: []deletionpolicy.TriggerSpec{
		{All: []deletionpolicy.ThresholdSpec{{Metric: deletionpolicy.SeedingTime, Op: deletionpolicy.GreaterOrEqual, Seconds: 72 * 3600}}},
	}}
}

func TestStoreGatesActivationOnFreshPreviewOfExactRevisions(t *testing.T) {
	store, c := testStore(t)
	ctx := context.Background()
	record, err := store.CreatePolicy(ctx, "brush", " Brush ", brushSpec(), []string{"2", "7", "2"})
	if err != nil {
		t.Fatal(err)
	}
	if record.Revision != 1 || record.Live || record.Name != "Brush" || !slices.Equal(record.Instances, []string{"2", "7"}) {
		t.Fatalf("created = %+v", record)
	}
	if _, err := store.CreatePolicy(ctx, "brush", "dup", brushSpec(), []string{"2"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate create = %v", err)
	}
	bad := brushSpec()
	bad.Triggers[0].All[0].Seconds = 60
	if _, err := store.CreatePolicy(ctx, "eager", "eager", bad, []string{"2"}); !errors.Is(err, deletionpolicy.ErrInvalid) {
		t.Fatalf("unsafe policy = %v", err)
	}
	if err := store.Activate(ctx, "brush", 1); !errors.Is(err, ErrNotPreviewed) {
		t.Fatalf("activate without preview = %v", err)
	}
	safety, _ := store.Safety(ctx)
	_ = store.RecordPreview(ctx, "brush", 1, safety.Revision, "2", 4)
	if err := store.Activate(ctx, "brush", 1); !errors.Is(err, ErrNotPreviewed) {
		t.Fatalf("activate with one of two instances previewed = %v", err)
	}
	_ = store.RecordPreview(ctx, "brush", 1, safety.Revision, "7", 0)
	if err := store.Activate(ctx, "brush", 2); !errors.Is(err, ErrConflict) {
		t.Fatalf("activate wrong revision = %v", err)
	}
	c.now = c.now.Add(PreviewValidity + time.Minute)
	if err := store.Activate(ctx, "brush", 1); !errors.Is(err, ErrNotPreviewed) {
		t.Fatalf("activate with stale preview = %v", err)
	}
	_ = store.RecordPreview(ctx, "brush", 1, safety.Revision, "2", 4)
	_ = store.RecordPreview(ctx, "brush", 1, safety.Revision, "7", 0)
	if err := store.Activate(ctx, "brush", 1); err != nil {
		t.Fatalf("activate = %v", err)
	}
	if record, _ := store.Policy(ctx, "brush"); !record.Live {
		t.Fatal("policy not live after activation")
	}

	// Editing produces a new, non-live revision whose old previews don't count.
	record, err = store.UpdatePolicy(ctx, "brush", 1, "Brush", brushSpec(), []string{"2", "7"})
	if err != nil || record.Revision != 2 || record.Live {
		t.Fatalf("update = %+v %v", record, err)
	}
	if err := store.Activate(ctx, "brush", 2); !errors.Is(err, ErrNotPreviewed) {
		t.Fatalf("activate edited revision without preview = %v", err)
	}
	_ = store.RecordPreview(ctx, "brush", 2, safety.Revision, "2", 0)
	_ = store.RecordPreview(ctx, "brush", 2, safety.Revision, "7", 0)
	if err := store.Activate(ctx, "brush", 2); err != nil {
		t.Fatal(err)
	}

	// A safety change demotes live policies and invalidates their previews.
	updated, err := store.UpdateSafety(ctx, safety.Revision, deletionpolicy.SafetySpec{ProtectedSites: []string{"siteA"}})
	if err != nil || updated.Revision != safety.Revision+1 {
		t.Fatalf("safety update = %+v %v", updated, err)
	}
	if record, _ := store.Policy(ctx, "brush"); record.Live {
		t.Fatal("safety change left a policy live")
	}
	if err := store.Activate(ctx, "brush", 2); !errors.Is(err, ErrNotPreviewed) {
		t.Fatalf("activate after safety change = %v", err)
	}
	if _, err := store.UpdateSafety(ctx, safety.Revision, deletionpolicy.SafetySpec{}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale safety update = %v", err)
	}
	if _, err := store.UpdateSafety(ctx, updated.Revision, deletionpolicy.SafetySpec{HitAndRun: map[string]deletionpolicy.HitAndRunSpec{"siteA": {}}}); !errors.Is(err, deletionpolicy.ErrInvalid) {
		t.Fatalf("invalid safety = %v", err)
	}

	if err := store.ArchivePolicy(ctx, "brush", 2); err != nil {
		t.Fatal(err)
	}
	if err := store.Activate(ctx, "brush", 3); !errors.Is(err, ErrNotFound) {
		t.Fatalf("activate archived = %v", err)
	}
}

func TestReconcileCreatesDryRunThenGoesLiveOnlyAfterActivation(t *testing.T) {
	store, _ := testStore(t)
	ctx := context.Background()
	qui := &fakeQui{}
	service := Service{Store: store, Instances: map[string]Instance{"2": {Client: qui}}}
	if _, err := store.CreatePolicy(ctx, "brush", "Brush", brushSpec(), []string{"2"}); err != nil {
		t.Fatal(err)
	}
	if err := service.ReconcileAll(ctx); err != nil {
		t.Fatal(err)
	}
	created := qui.byName(t, "vertex-del-brush-triggers")
	if created.Enabled || !created.DryRun || qui.saves != 1 {
		t.Fatalf("new policy automation = %+v saves=%d", created, qui.saves)
	}
	if err := service.ReconcileAll(ctx); err != nil || qui.saves != 1 {
		t.Fatalf("idempotent reconcile err=%v saves=%d", err, qui.saves)
	}
	if err := service.Activate(ctx, "brush", 1); !errors.Is(err, ErrNotPreviewed) {
		t.Fatalf("activate without preview = %v", err)
	}
	previews, err := service.Preview(ctx, "brush", 20)
	if err != nil || len(previews["2"]) != 1 || previews["2"][0].TotalMatches != 2 || qui.saves != 1 {
		t.Fatalf("preview = %+v err=%v saves=%d", previews, err, qui.saves)
	}
	if err := service.Activate(ctx, "brush", 1); err != nil {
		t.Fatal(err)
	}
	live := qui.byName(t, "vertex-del-brush-triggers")
	if !live.Enabled || live.DryRun || live.ID != created.ID {
		t.Fatalf("activated automation = %+v", live)
	}
	bindings, _ := store.Bindings(ctx, "2")
	if len(bindings) != 1 || !bindings[0].Live || bindings[0].QuiID != created.ID {
		t.Fatalf("bindings = %+v", bindings)
	}
	if err := service.Deactivate(ctx, "brush"); err != nil {
		t.Fatal(err)
	}
	if back := qui.byName(t, "vertex-del-brush-triggers"); back.Enabled || !back.DryRun {
		t.Fatalf("deactivated automation = %+v", back)
	}
}

func activatedService(t *testing.T) (Service, Store, *fakeQui) {
	t.Helper()
	store, _ := testStore(t)
	ctx := context.Background()
	qui := &fakeQui{}
	service := Service{Store: store, Instances: map[string]Instance{"2": {Client: qui}}}
	if _, err := store.CreatePolicy(ctx, "brush", "Brush", brushSpec(), []string{"2"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Preview(ctx, "brush", 20); err != nil {
		t.Fatal(err)
	}
	if err := service.Activate(ctx, "brush", 1); err != nil {
		t.Fatal(err)
	}
	if live := qui.byName(t, "vertex-del-brush-triggers"); !live.Enabled || live.DryRun {
		t.Fatalf("setup automation not live: %+v", live)
	}
	return service, store, qui
}

func assertFailedClosed(t *testing.T, store Store, qui *fakeQui, code string) {
	t.Helper()
	automation := qui.byName(t, "vertex-del-brush-triggers")
	if automation.Enabled || !automation.DryRun {
		t.Fatalf("automation still able to delete: %+v", automation)
	}
	if record, _ := store.Policy(context.Background(), "brush"); record.Live {
		t.Fatal("policy still live")
	}
	bindings, _ := store.Bindings(context.Background(), "2")
	if len(bindings) != 1 || bindings[0].LastError != code || bindings[0].Live {
		t.Fatalf("bindings = %+v, want error %s", bindings, code)
	}
}

func TestReconcileFailsClosedWhenQuiCopyWasEdited(t *testing.T) {
	service, store, qui := activatedService(t)
	qui.mu.Lock()
	for index := range qui.automations {
		delete(qui.automations[index].Conditions, "delete")
		qui.automations[index].Conditions["delete"] = map[string]any{"enabled": true, "mode": "deleteWithFilesIncludeCrossSeeds", "condition": map[string]any{"field": "SIZE", "operator": "GREATER_THAN", "value": "0"}}
	}
	qui.mu.Unlock()
	if _, err := service.Reconcile(context.Background(), "2"); err != nil {
		t.Fatal(err)
	}
	assertFailedClosed(t, store, qui, CodeTampered)
	// The rewrite restored Vertex's own conditions, not the edited ones.
	automation := qui.byName(t, "vertex-del-brush-triggers")
	if !strings.Contains(canonical(automation.Conditions), "vertex-managed") || strings.Contains(canonical(automation.Conditions), "IncludeCrossSeeds") {
		t.Fatalf("conditions after rewrite = %s", canonical(automation.Conditions))
	}
}

func TestReconcileFailsClosedWhenQuiCopyWasDeleted(t *testing.T) {
	service, store, qui := activatedService(t)
	qui.mu.Lock()
	qui.automations = nil
	qui.mu.Unlock()
	if _, err := service.Reconcile(context.Background(), "2"); err != nil {
		t.Fatal(err)
	}
	assertFailedClosed(t, store, qui, CodeMissing)
}

func TestReconcileDisablesUnboundAutomationUsingVertexName(t *testing.T) {
	store, _ := testStore(t)
	ctx := context.Background()
	qui := &fakeQui{next: 40, automations: []control.Automation{{ID: 41, Name: "vertex-del-brush-triggers", TrackerPattern: "*", Enabled: true, IntervalSeconds: 60, Conditions: map[string]any{"schemaVersion": "1", "delete": map[string]any{"enabled": true, "mode": "delete"}}}}}
	service := Service{Store: store, Instances: map[string]Instance{"2": {Client: qui}}}
	if _, err := store.CreatePolicy(ctx, "brush", "Brush", brushSpec(), []string{"2"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Reconcile(ctx, "2"); err != nil {
		t.Fatal(err)
	}
	automation := qui.byName(t, "vertex-del-brush-triggers")
	if automation.ID != 41 || automation.Enabled || !automation.DryRun || !strings.Contains(canonical(automation.Conditions), "vertex-managed") {
		t.Fatalf("taken-over automation = %+v", automation)
	}
	bindings, _ := store.Bindings(ctx, "2")
	if len(bindings) != 1 || bindings[0].LastError != CodeUnboundName {
		t.Fatalf("bindings = %+v", bindings)
	}
}

func TestReconcileFailsClosedOnReadbackMismatch(t *testing.T) {
	service, store, qui := activatedService(t)
	ctx := context.Background()
	// qui starts persisting something other than what was sent.
	qui.mutate = func(automation *control.Automation) { automation.IntervalSeconds = 9999 }
	if err := service.Deactivate(ctx, "brush"); err == nil {
		t.Fatal("readback mismatch not reported")
	}
	qui.mutate = nil
	if _, err := service.Reconcile(ctx, "2"); err != nil {
		t.Fatal(err)
	}
	assertFailedClosed(t, store, qui, CodeTampered)
}

func TestReconcileRetiresArchivedAndDescopedPoliciesWithoutDeleting(t *testing.T) {
	service, store, qui := activatedService(t)
	ctx := context.Background()
	if err := store.ArchivePolicy(ctx, "brush", 1); err != nil {
		t.Fatal(err)
	}
	outcomes, err := service.Reconcile(ctx, "2")
	if err != nil {
		t.Fatal(err)
	}
	if len(outcomes) != 1 || outcomes[0].Action != "retired" {
		t.Fatalf("outcomes = %+v", outcomes)
	}
	retired := qui.byName(t, "vertex-del-brush-triggers")
	if retired.Enabled || !retired.DryRun {
		t.Fatalf("retired automation = %+v", retired)
	}
	saves := qui.saves
	if _, err := service.Reconcile(ctx, "2"); err != nil || qui.saves != saves {
		t.Fatalf("second retire err=%v saves %d -> %d", err, saves, qui.saves)
	}
}

func TestPreviewRequiresConnectedScope(t *testing.T) {
	store, _ := testStore(t)
	ctx := context.Background()
	service := Service{Store: store, Instances: map[string]Instance{"2": {Client: &fakeQui{}}}}
	if _, err := store.CreatePolicy(ctx, "brush", "Brush", brushSpec(), []string{"2", "9"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Preview(ctx, "brush", 20); !errors.Is(err, ErrInstanceUnavailable) {
		t.Fatalf("preview with unconnected instance = %v", err)
	}
}

type fakeTags struct {
	calls [][2][]string
}

func (fake *fakeTags) AddTags(_ context.Context, hashes, tags []string) error {
	fake.calls = append(fake.calls, [2][]string{slices.Clone(hashes), slices.Clone(tags)})
	return nil
}

func seedAttempt(t *testing.T, database *sql.DB, id, hash, source, state, fact string) {
	t.Helper()
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO race_jobs(job_id,source_id,identity_key,job_state,participation_deadline,observation_deadline,created_at,updated_at) VALUES(?,?,?,'DISCOVERED','2026-10-28T00:00:00Z','2026-10-28T00:00:00Z','2026-10-27T00:00:00Z','2026-10-27T00:00:00Z')`, []any{"job-" + id, source, hash}},
	}
	if state == "HASH_SEEN" || state == "RECONCILED_CONFIRMED" {
		statements = append(statements, struct {
			query string
			args  []any
		}{`INSERT INTO placement_attempts(attempt_id,job_id,identity_key,attempt_no,instance_id,storage_target_id,attempt_state,dispatch_state,side_effect_fact,allocation_epoch,qb_torrent_id,confirmation_sync_generation,confirmation_snapshot_revision,confirmation_evidence_digest,reserved_bytes,started_at) VALUES(?,?,?,1,'2','target',?,'STARTED',?,1,?,1,1,'d',4096,'2026-10-27T00:00:00Z')`, []any{id, "job-" + id, hash, state, fact, hash}})
	} else {
		statements = append(statements, struct {
			query string
			args  []any
		}{`INSERT INTO placement_attempts(attempt_id,job_id,identity_key,attempt_no,instance_id,storage_target_id,attempt_state,dispatch_state,side_effect_fact,allocation_epoch,qb_torrent_id,reserved_bytes,started_at) VALUES(?,?,?,1,'2','target',?,'STARTED',?,1,?,4096,'2026-10-27T00:00:00Z')`, []any{id, "job-" + id, hash, state, fact, hash}})
	}
	for _, statement := range statements {
		if _, err := database.Exec(statement.query, statement.args...); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
}

func TestBackfillTagsOnlyTorrentsVertexProvablyAdded(t *testing.T) {
	store, _ := testStore(t)
	ctx := context.Background()
	const (
		added     = "1111111111111111111111111111111111111111"
		confirmed = "2222222222222222222222222222222222222222"
		tagged    = "3333333333333333333333333333333333333333"
		gone      = "4444444444444444444444444444444444444444"
		preexist  = "5555555555555555555555555555555555555555"
		badSite   = "6666666666666666666666666666666666666666"
	)
	seedAttempt(t, store.DB, "a1", added, "hdsky/k1", "HASH_SEEN", "OBSERVED_EFFECT")
	seedAttempt(t, store.DB, "a2", confirmed, "ourbits/k2", "RECONCILED_CONFIRMED", "OBSERVED_EFFECT")
	seedAttempt(t, store.DB, "a3", tagged, "hdsky/k3", "HASH_SEEN", "OBSERVED_EFFECT")
	seedAttempt(t, store.DB, "a4", gone, "hdsky/k4", "HASH_SEEN", "OBSERVED_EFFECT")
	seedAttempt(t, store.DB, "a5", preexist, "hdsky/k5", "ALREADY_PRESENT", "OBSERVED_EFFECT")
	seedAttempt(t, store.DB, "a6", badSite, "bad site/k6", "HASH_SEEN", "OBSERVED_EFFECT")
	snapshot := syncmerge.Snapshot{Torrents: map[string]*syncmerge.Torrent{
		added:     {ID: added, Tags: "brush"},
		confirmed: {ID: confirmed, Tags: "vertex-managed"},
		tagged:    {ID: tagged, Tags: "Vertex-Managed, vertex-site-hdsky"},
		preexist:  {ID: preexist, Tags: "mine"},
		badSite:   {ID: badSite},
	}}
	tags := &fakeTags{}
	service := Service{Store: store, Instances: map[string]Instance{"2": {Tags: tags, Snapshot: func() (syncmerge.Snapshot, bool) { return snapshot, true }}}}
	plan, err := service.BackfillPlan(ctx, "2")
	if err != nil {
		t.Fatal(err)
	}
	want := []BackfillItem{
		{Hash: added, Site: "hdsky", Missing: []string{"vertex-managed", "vertex-site-hdsky"}},
		{Hash: confirmed, Site: "ourbits", Missing: []string{"vertex-site-ourbits"}},
		{Hash: badSite, Site: "bad site", Missing: []string{"vertex-managed"}},
	}
	slices.SortFunc(plan, func(a, b BackfillItem) int { return strings.Compare(a.Hash, b.Hash) })
	slices.SortFunc(want, func(a, b BackfillItem) int { return strings.Compare(a.Hash, b.Hash) })
	if len(plan) != len(want) {
		t.Fatalf("plan = %+v", plan)
	}
	for index := range want {
		if plan[index].Hash != want[index].Hash || !slices.Equal(plan[index].Missing, want[index].Missing) {
			t.Fatalf("plan[%d] = %+v, want %+v", index, plan[index], want[index])
		}
	}
	if len(tags.calls) != 0 {
		t.Fatal("planning wrote tags")
	}
	applied, err := service.BackfillApply(ctx, "2")
	if err != nil || applied != 3 || len(tags.calls) != 3 {
		t.Fatalf("applied=%d err=%v calls=%v", applied, err, tags.calls)
	}
	for _, call := range tags.calls {
		if slices.Contains(call[0], preexist) || slices.Contains(call[0], gone) || slices.Contains(call[0], tagged) {
			t.Fatalf("backfill touched an ineligible torrent: %v", call)
		}
	}
}
