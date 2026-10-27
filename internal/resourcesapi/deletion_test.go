package resourcesapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
	"github.com/vertex-pro/vertex-pro/internal/deletionsync"
	"github.com/vertex-pro/vertex-pro/internal/qui/control"
)

type apiFakeQui struct {
	mu          sync.Mutex
	automations []control.Automation
}

func (fake *apiFakeQui) copyOf(automation control.Automation) control.Automation {
	encoded, _ := json.Marshal(automation)
	var result control.Automation
	_ = json.Unmarshal(encoded, &result)
	return result
}

func (fake *apiFakeQui) Automations(context.Context, string) ([]control.Automation, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	result := make([]control.Automation, 0, len(fake.automations))
	for _, automation := range fake.automations {
		result = append(result, fake.copyOf(automation))
	}
	return result, nil
}

func (fake *apiFakeQui) SaveAutomation(_ context.Context, _ string, automation control.Automation) (control.Automation, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	stored := fake.copyOf(automation)
	if stored.ID == 0 {
		stored.ID = len(fake.automations) + 1
		fake.automations = append(fake.automations, stored)
		return stored, nil
	}
	for index := range fake.automations {
		if fake.automations[index].ID == stored.ID {
			fake.automations[index] = stored
		}
	}
	return stored, nil
}

func (fake *apiFakeQui) PreviewDelete(context.Context, string, control.Automation, int, int, string) (control.PreviewResult, error) {
	return control.PreviewResult{TotalMatches: 1, Examples: []control.PreviewTorrent{{Hash: "0f1e", Name: "Old.Release", SeedingTime: 300000}}}, nil
}

func (fake *apiFakeQui) only(t *testing.T) control.Automation {
	t.Helper()
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.automations) != 1 {
		t.Fatalf("automations = %d", len(fake.automations))
	}
	return fake.copyOf(fake.automations[0])
}

func deletionRouter(t *testing.T) (http.Handler, *apiFakeQui) {
	t.Helper()
	database, err := db.Open(context.Background(), db.Config{Path: filepath.Join(t.TempDir(), "api.db"), MaxOpenConns: 1, MaxIdleConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	runner, _ := db.DefaultRunner()
	if err := runner.Apply(context.Background(), database); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	qui := &apiFakeQui{}
	service := deletionsync.Service{Store: deletionsync.Store{DB: database, Now: func() time.Time { return now }}, Instances: map[string]deletionsync.Instance{"2": {Client: qui}}}
	return Router(Deps{Database: database, Now: func() time.Time { return now }, Deletion: func() (deletionsync.Service, bool) { return service, true }}), qui
}

func call(t *testing.T, router http.Handler, method, path, ifMatch string, body any) (int, map[string]any) {
	t.Helper()
	var payload []byte
	if body != nil {
		payload, _ = json.Marshal(body)
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	if ifMatch != "" {
		request.Header.Set("If-Match", ifMatch)
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	var decoded map[string]any
	_ = json.Unmarshal(recorder.Body.Bytes(), &decoded)
	return recorder.Code, decoded
}

func errorCode(body map[string]any) string {
	envelope, _ := body["error"].(map[string]any)
	code, _ := envelope["code"].(string)
	return code
}

func brushPolicy(seconds int) map[string]any {
	return map[string]any{"id": "brush", "name": "Brush", "instances": []string{"2"}, "spec": map[string]any{
		"delete_mode": "with_files_preserve_cross_seeds",
		"triggers":    []any{map[string]any{"all": []any{map[string]any{"metric": "seeding_time", "op": ">=", "seconds": seconds}}}},
	}}
}

type apiFakeActivity struct{ page []control.Activity }

func (fake apiFakeActivity) Activity(context.Context, string, int) ([]control.Activity, error) {
	return fake.page, nil
}

func TestDeletionAuditListAndJobDetailOverHTTP(t *testing.T) {
	database, err := db.Open(context.Background(), db.Config{Path: filepath.Join(t.TempDir(), "audit.db"), MaxOpenConns: 1, MaxIdleConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	runner, _ := db.DefaultRunner()
	if err := runner.Apply(context.Background(), database); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	const hash = "aaaa111111111111111111111111111111111111"
	for _, statement := range []string{
		`INSERT INTO race_jobs(job_id,source_id,identity_key,job_state,participation_deadline,observation_deadline,created_at,updated_at) VALUES('job-1','hdsky/k1','` + hash + `','DISCOVERED','2026-10-28T00:00:00Z','2026-10-28T00:00:00Z','2026-10-27T00:00:00Z','2026-10-27T00:00:00Z')`,
		`INSERT INTO placement_attempts(attempt_id,job_id,identity_key,attempt_no,instance_id,storage_target_id,attempt_state,dispatch_state,side_effect_fact,allocation_epoch,qb_torrent_id,confirmation_sync_generation,confirmation_snapshot_revision,confirmation_evidence_digest,reserved_bytes,started_at) VALUES('a1','job-1','` + hash + `',1,'2','target','HASH_SEEN','STARTED','OBSERVED_EFFECT',1,'` + hash + `',1,1,'d',4096,'2026-10-27T00:00:00Z')`,
	} {
		if _, err := database.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	rule := 99
	activities := apiFakeActivity{page: []control.Activity{
		{ID: 12, Hash: hash, TorrentName: "Old.Release", Action: control.ActivityDeletedRatio, RuleID: &rule, RuleName: "external", Outcome: control.OutcomeSuccess, Reason: "Ratio reached", CreatedAt: now},
		{ID: 11, Hash: hash, Action: control.ActivityDeletedCondition, Outcome: control.OutcomeDryRun, CreatedAt: now},
		{ID: 10, Hash: "bbbb", Action: control.ActivityDeleteFailed, Outcome: control.OutcomeFailed, CreatedAt: now},
	}}
	service := deletionsync.Service{Store: deletionsync.Store{DB: database, Now: func() time.Time { return now }}, Instances: map[string]deletionsync.Instance{"2": {Client: &apiFakeQui{}, Activity: activities}}}
	if result, err := service.ImportAudit(context.Background(), "2"); err != nil || result.Imported != 2 {
		t.Fatalf("import = %+v %v", result, err)
	}
	router := Router(Deps{Database: database, Now: func() time.Time { return now }, Deletion: func() (deletionsync.Service, bool) { return service, true }})

	status, body := call(t, router, http.MethodGet, "/api/v1/deletion/audit/2?limit=1", "", nil)
	items, _ := body["items"].([]any)
	if status != http.StatusOK || len(items) != 1 || body["next_before"] != float64(12) {
		t.Fatalf("audit page = %d %v", status, body)
	}
	first := items[0].(map[string]any)
	if first["activity_id"] != float64(12) || first["job_id"] != "job-1" || first["source"] != "external" || first["outcome"] != "success" {
		t.Fatalf("audit entry = %v", first)
	}
	status, body = call(t, router, http.MethodGet, "/api/v1/deletion/audit/2?before=12", "", nil)
	if items, _ := body["items"].([]any); status != http.StatusOK || len(items) != 1 || items[0].(map[string]any)["outcome"] != "failed" {
		t.Fatalf("older page = %d %v", status, body)
	}
	if status, _ := call(t, router, http.MethodGet, "/api/v1/deletion/audit/2?before=x", "", nil); status != http.StatusBadRequest {
		t.Fatalf("invalid before = %d", status)
	}

	status, body = call(t, router, http.MethodGet, "/api/v1/jobs/job-1", "", nil)
	if status != http.StatusOK {
		t.Fatalf("job detail = %d %v", status, body)
	}
	job := body
	if data, ok := body["data"].(map[string]any); ok {
		job = data
	}
	deletions, _ := job["deletions"].([]any)
	if len(deletions) != 1 || deletions[0].(map[string]any)["reason"] != "Ratio reached" {
		t.Fatalf("job deletions = %v", job["deletions"])
	}
}

func TestDeletionPolicyLifecycleOverHTTP(t *testing.T) {
	router, qui := deletionRouter(t)

	status, body := call(t, router, http.MethodPost, "/api/v1/deletion/policies", "", brushPolicy(259200))
	if status != http.StatusCreated || body["live"] != false || body["revision"] != float64(1) {
		t.Fatalf("create = %d %v", status, body)
	}
	if automation := qui.only(t); automation.Enabled || !automation.DryRun || automation.Name != "vertex-del-brush-triggers" {
		t.Fatalf("synced automation = %+v", automation)
	}

	status, body = call(t, router, http.MethodPost, "/api/v1/deletion/policies/brush/activate", "", map[string]any{"revision": 1})
	if status != http.StatusUnprocessableEntity || errorCode(body) != "VALIDATION_FAILED" {
		t.Fatalf("activate without preview = %d %v", status, body)
	}
	status, body = call(t, router, http.MethodPost, "/api/v1/deletion/policies/brush/preview", "", map[string]any{"limit": 10})
	if status != http.StatusOK || body["revision"] != float64(1) {
		t.Fatalf("preview = %d %v", status, body)
	}
	roles := body["instances"].(map[string]any)["2"].([]any)
	if len(roles) != 1 || roles[0].(map[string]any)["total_matches"] != float64(1) {
		t.Fatalf("preview roles = %v", roles)
	}
	status, body = call(t, router, http.MethodPost, "/api/v1/deletion/policies/brush/activate", "", map[string]any{"revision": 1})
	if status != http.StatusOK || body["live"] != true {
		t.Fatalf("activate = %d %v", status, body)
	}
	if automation := qui.only(t); !automation.Enabled || automation.DryRun {
		t.Fatalf("activated automation = %+v", automation)
	}

	status, body = call(t, router, http.MethodPut, "/api/v1/deletion/policies/brush", `"9"`, brushPolicy(172800))
	if status != http.StatusConflict || errorCode(body) != "REVISION_CONFLICT" {
		t.Fatalf("stale update = %d %v", status, body)
	}
	status, body = call(t, router, http.MethodPut, "/api/v1/deletion/policies/brush", `"1"`, brushPolicy(60))
	if status != http.StatusUnprocessableEntity || errorCode(body) != "VALIDATION_FAILED" {
		t.Fatalf("unsafe update = %d %v", status, body)
	}
	status, body = call(t, router, http.MethodPut, "/api/v1/deletion/policies/brush", `"1"`, brushPolicy(172800))
	if status != http.StatusOK || body["live"] != false || body["revision"] != float64(2) {
		t.Fatalf("update = %d %v", status, body)
	}
	if automation := qui.only(t); automation.Enabled || !automation.DryRun {
		t.Fatalf("edited automation still live: %+v", automation)
	}

	status, body = call(t, router, http.MethodGet, "/api/v1/deletion/safety", "", nil)
	if status != http.StatusOK || body["revision"] != float64(1) {
		t.Fatalf("safety = %d %v", status, body)
	}
	status, body = call(t, router, http.MethodPut, "/api/v1/deletion/safety", `"1"`, map[string]any{"spec": map[string]any{"protected_categories": []string{"keep"}, "hit_and_run": map[string]any{"hdsky": map[string]any{"min_seeding_time_seconds": 259200}}}})
	if status != http.StatusOK || body["revision"] != float64(2) {
		t.Fatalf("safety update = %d %v", status, body)
	}
	status, body = call(t, router, http.MethodPut, "/api/v1/deletion/safety", `"2"`, map[string]any{"spec": map[string]any{"unknown_field": true}})
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("unknown safety field = %d %v", status, body)
	}

	status, body = call(t, router, http.MethodGet, "/api/v1/deletion/tag-backfill/7", "", nil)
	if status != http.StatusConflict || errorCode(body) != "INSTANCE_UNAVAILABLE" {
		t.Fatalf("backfill on unconnected instance = %d %v", status, body)
	}

	status, body = call(t, router, http.MethodDelete, "/api/v1/deletion/policies/brush", `"2"`, nil)
	if status != http.StatusOK || body["archived"] != true {
		t.Fatalf("archive = %d %v", status, body)
	}
	if automation := qui.only(t); automation.Enabled || !automation.DryRun {
		t.Fatalf("archived automation = %+v", automation)
	}
	status, _ = call(t, router, http.MethodGet, "/api/v1/deletion/policies/brush", "", nil)
	if status != http.StatusNotFound {
		t.Fatalf("archived policy still readable: %d", status)
	}
}
