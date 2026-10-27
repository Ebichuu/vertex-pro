package deletionsync

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/deletionpolicy"
	"github.com/vertex-pro/vertex-pro/internal/qui/control"
)

type fakeActivity struct {
	pages [][]control.Activity
	calls int
}

func (fake *fakeActivity) Activity(_ context.Context, _ string, limit int) ([]control.Activity, error) {
	page := fake.pages[min(fake.calls, len(fake.pages)-1)]
	fake.calls++
	if len(page) > limit {
		page = page[:limit]
	}
	return page, nil
}

func ruleID(value int) *int { return &value }

func activity(id int, hash, action, outcome string, rule *int, name string) control.Activity {
	return control.Activity{ID: id, InstanceID: 2, Hash: hash, TorrentName: "T" + hash[:min(2, len(hash))], TrackerDomain: "tracker.pt.example", Action: action, RuleID: rule, RuleName: name, Outcome: outcome, Reason: "Delete condition matched", Details: json.RawMessage(`{"deleteMode":"deleteWithFilesPreserveCrossSeeds"}`), CreatedAt: time.Date(2026, 10, 27, 10, 0, id%60, 0, time.UTC)}
}

const (
	hashPolicy   = "aaaa111111111111111111111111111111111111"
	hashLegacy   = "bbbb222222222222222222222222222222222222"
	hashDryRun   = "cccc333333333333333333333333333333333333"
	hashExternal = "dddd444444444444444444444444444444444444"
)

func auditFixture(t *testing.T) (Store, *fakeActivity) {
	t.Helper()
	store, _ := testStore(t)
	ctx := context.Background()
	if _, err := store.CreatePolicy(ctx, "brush", "Brush", brushSpec(), []string{"2"}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutBinding(ctx, Binding{Instance: "2", PolicyID: "brush", Role: deletionpolicy.RoleTriggers, QuiID: 31, PolicyRevision: 1, SafetyRevision: 1, Live: true, Payload: "{}"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB.Exec(`INSERT INTO rules(rule_id,site_id,name,enabled,priority,revision,state,definition_json,created_at,updated_at) VALUES('rule-legacy',NULL,'Legacy',1,1,2,'PENDING','{}','2026-10-27T00:00:00Z','2026-10-27T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB.Exec(`INSERT INTO qui_policy_bindings(instance_id,rule_id,rule_revision,qui_id,tag,payload_json,enabled,updated_at) VALUES('2','rule-legacy',2,77,'vertex-policy-x','{}',1,'2026-10-27T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	seedAttempt(t, store.DB, "a1", hashPolicy, "hdsky/k1", "HASH_SEEN", "OBSERVED_EFFECT")
	seedAttempt(t, store.DB, "a2", hashLegacy, "hdsky/k2", "HASH_SEEN", "OBSERVED_EFFECT")
	page := []control.Activity{
		activity(120, hashPolicy, control.ActivityDeletedCondition, control.OutcomeSuccess, ruleID(31), "vertex-del-brush-triggers"),
		activity(119, hashLegacy, control.ActivityDeleteFailed, control.OutcomeFailed, ruleID(77), "vertex-policy-x"),
		activity(118, hashDryRun, control.ActivityDeletedCondition, control.OutcomeDryRun, ruleID(1000000002), "probe"),
		activity(117, "", "tags_changed", control.OutcomeSuccess, ruleID(5), "user-tags"),
		activity(116, hashExternal, control.ActivityDeletedRatio, control.OutcomeSuccess, ruleID(99), "someone-else"),
	}
	return store, &fakeActivity{pages: [][]control.Activity{page}}
}

func TestAuditImportsRealDeletionsOnceAndLinksJobs(t *testing.T) {
	store, reader := auditFixture(t)
	ctx := context.Background()
	importer := AuditImporter{Store: store, Client: reader, Instance: "2"}
	result, err := importer.Import(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if result.Fetched != 5 || result.Imported != 3 || result.Gap {
		t.Fatalf("result = %+v", result)
	}
	again, err := importer.Import(ctx)
	if err != nil || again.Imported != 0 {
		t.Fatalf("re-import = %+v %v", again, err)
	}
	entries, err := store.Audit(ctx, "2", 0, 50)
	if err != nil || len(entries) != 3 {
		t.Fatalf("audit = %+v %v", entries, err)
	}
	byHash := map[string]AuditEntry{}
	for _, entry := range entries {
		byHash[entry.Hash] = entry
	}
	policy := byHash[hashPolicy]
	if policy.Source != SourcePolicy || policy.PolicyID != "brush" || policy.PolicyRole != "triggers" || policy.PolicyRevision != 1 || policy.JobID != "job-a1" || policy.Outcome != "success" {
		t.Fatalf("policy deletion = %+v", policy)
	}
	legacy := byHash[hashLegacy]
	if legacy.Source != SourceLegacyRule || legacy.VertexRuleID != "rule-legacy" || legacy.Outcome != "failed" || legacy.JobID != "job-a2" {
		t.Fatalf("legacy deletion = %+v", legacy)
	}
	external := byHash[hashExternal]
	if external.Source != SourceExternal || external.JobID != "" || external.PolicyID != "" {
		t.Fatalf("external deletion = %+v", external)
	}
	if _, dry := byHash[hashDryRun]; dry {
		t.Fatal("dry-run activity was imported")
	}
	linked, err := store.AuditForJob(ctx, "job-a1")
	if err != nil || len(linked) != 1 || linked[0].ActivityID != 120 {
		t.Fatalf("job audit = %+v %v", linked, err)
	}
	older, err := store.Audit(ctx, "2", 119, 50)
	if err != nil || len(older) != 1 || older[0].ActivityID != 116 {
		t.Fatalf("paged audit = %+v %v", older, err)
	}
}

func TestAuditDetectsWhenAFullPageSkipsPastTheLastSeenID(t *testing.T) {
	store, reader := auditFixture(t)
	ctx := context.Background()
	importer := AuditImporter{Store: store, Client: reader, Instance: "2", PageSize: 5}
	if result, err := importer.Import(ctx); err != nil || result.Gap {
		t.Fatalf("first import (no previous cursor) = %+v %v", result, err)
	}
	// A full page that still reaches the last seen ID: continuous.
	reader.pages = [][]control.Activity{{
		activity(124, hashExternal, "tags_changed", control.OutcomeSuccess, nil, ""),
		activity(123, hashExternal, "tags_changed", control.OutcomeSuccess, nil, ""),
		activity(122, hashExternal, "tags_changed", control.OutcomeSuccess, nil, ""),
		activity(121, hashExternal, "tags_changed", control.OutcomeSuccess, nil, ""),
		activity(120, hashPolicy, control.ActivityDeletedCondition, control.OutcomeSuccess, ruleID(31), "vertex-del-brush-triggers"),
	}}
	reader.calls = 0
	if result, err := importer.Import(ctx); err != nil || result.Gap {
		t.Fatalf("continuous page = %+v %v", result, err)
	}
	if _, gap, _ := store.AuditGap(ctx, "2"); gap {
		t.Fatal("gap recorded for a continuous page")
	}
	// A full page entirely newer than the last seen ID (124): possible gap.
	reader.pages = [][]control.Activity{{
		activity(140, hashExternal, control.ActivityDeletedRatio, control.OutcomeSuccess, ruleID(99), "x"),
		activity(139, hashExternal, "tags_changed", control.OutcomeSuccess, nil, ""),
		activity(138, hashExternal, "tags_changed", control.OutcomeSuccess, nil, ""),
		activity(137, hashExternal, "tags_changed", control.OutcomeSuccess, nil, ""),
		activity(136, hashExternal, "tags_changed", control.OutcomeSuccess, nil, ""),
	}}
	reader.calls = 0
	result, err := importer.Import(ctx)
	if err != nil || !result.Gap || result.Imported != 1 {
		t.Fatalf("gap page = %+v %v", result, err)
	}
	if _, gap, err := store.AuditGap(ctx, "2"); err != nil || !gap {
		t.Fatalf("gap not recorded: %v", err)
	}
	if work, err := store.HasAuditWork(ctx); err != nil || !work {
		t.Fatalf("audit work = %v %v", work, err)
	}
}
