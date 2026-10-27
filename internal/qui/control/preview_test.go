package control

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/vertex-pro/vertex-pro/internal/secrets"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "qui-bfe94b5e", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// contractServer serves one route and records the decoded request body. It
// fails the test if anything reaches qui's forced-execution endpoint.
func contractServer(t *testing.T, method, path string, status int, body []byte, request *map[string]any) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
		if strings.HasSuffix(incoming.URL.Path, "/automations/apply") {
			t.Errorf("client called qui's forced-execution endpoint")
		}
		if incoming.Method != method || incoming.URL.Path != path {
			t.Errorf("request = %s %s, want %s %s", incoming.Method, incoming.URL.Path, method, path)
		}
		if incoming.Header.Get("X-API-Key") != "key" || incoming.Header.Get("Accept") != "application/json" {
			t.Errorf("request headers = %v", incoming.Header)
		}
		if method == http.MethodPost {
			if incoming.Header.Get("Content-Type") != "application/json" {
				t.Errorf("content type = %q", incoming.Header.Get("Content-Type"))
			}
			if err := json.NewDecoder(incoming.Body).Decode(request); err != nil {
				t.Errorf("decode request: %v", err)
			}
		}
		if request != nil && method == http.MethodGet {
			(*request) = map[string]any{"query": incoming.URL.RawQuery}
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(status)
		_, _ = writer.Write(body)
	}))
	t.Cleanup(server.Close)
	client, err := New(context.Background(), Config{BaseURL: server.URL, APIKey: secrets.SecretRef{Provider: "env", Key: "X"}, Store: staticStore{value: "key"}})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func probeRule() Automation {
	return Automation{Name: "vertex-deletion-probe", TrackerPattern: "*", Enabled: true, IntervalSeconds: 60, Conditions: map[string]any{
		"schemaVersion": "1",
		"delete": map[string]any{"enabled": true, "mode": "deleteWithFilesPreserveCrossSeeds", "condition": map[string]any{
			"operator": "AND", "conditions": []any{
				map[string]any{"field": "TAGS", "operator": "CONTAINS", "value": "vertex-managed"},
				map[string]any{"field": "SEEDING_TIME", "operator": "GREATER_THAN", "value": "259200"},
			},
		}},
	}}
}

func TestPreviewDeleteContract(t *testing.T) {
	var request map[string]any
	client := contractServer(t, http.MethodPost, "/api/instances/2/automations/preview", http.StatusOK, fixture(t, "preview_delete.json"), &request)
	result, err := client.PreviewDelete(context.Background(), "2", probeRule(), 25, 0, PreviewViewNeeded)
	if err != nil {
		t.Fatal(err)
	}
	if request["name"] != "vertex-deletion-probe" || request["previewLimit"] != float64(25) || request["previewView"] != "needed" {
		t.Fatalf("preview payload = %+v", request)
	}
	if _, sent := request["previewOffset"]; sent {
		t.Fatal("zero offset should be omitted")
	}
	deleteAction := request["conditions"].(map[string]any)["delete"].(map[string]any)
	if deleteAction["enabled"] != true || deleteAction["mode"] != "deleteWithFilesPreserveCrossSeeds" {
		t.Fatalf("delete action = %+v", deleteAction)
	}
	if result.TotalMatches != 3 || len(result.Examples) != 2 {
		t.Fatalf("result = %+v", result)
	}
	first, second := result.Examples[0], result.Examples[1]
	if first.Hash != "0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c" || first.SeedingTime != 302400 || first.Score == nil || *first.Score != 214.7 || first.HardlinkScope != "torrents_only" || first.Progress != 1 {
		t.Fatalf("first example = %+v", first)
	}
	if !second.IsUnregistered || second.Score != nil {
		t.Fatalf("second example = %+v", second)
	}
}

func TestDryRunForcesDryRunAndRejectsLiveOutcomes(t *testing.T) {
	var request map[string]any
	client := contractServer(t, http.MethodPost, "/api/instances/2/automations/dry-run", http.StatusAccepted, fixture(t, "dry_run.json"), &request)
	rule := probeRule()
	rule.ID, rule.DryRun, rule.Enabled = 9, false, false
	result, err := client.DryRun(context.Background(), "2", rule)
	if err != nil {
		t.Fatal(err)
	}
	if request["dryRun"] != true || request["enabled"] != true {
		t.Fatalf("dry-run payload = %+v", request)
	}
	if _, sent := request["id"]; sent {
		t.Fatal("dry-run must not reference a saved automation")
	}
	if result.Status != "dry-run-completed" || len(result.ActivityIDs) != 2 || len(result.Activities) != 2 || !result.Activities[0].IsDeletion() || !result.Activities[0].IsDryRun() {
		t.Fatalf("dry-run result = %+v", result)
	}

	live := strings.Replace(string(fixture(t, "dry_run.json")), `"outcome": "dry-run"`, `"outcome": "success"`, 1)
	client = contractServer(t, http.MethodPost, "/api/instances/2/automations/dry-run", http.StatusAccepted, []byte(live), &request)
	if _, err := client.DryRun(context.Background(), "2", probeRule()); err == nil || !strings.Contains(err.Error(), "non-dry-run") {
		t.Fatalf("live outcome error = %v", err)
	}
}

func TestActivityContract(t *testing.T) {
	var request map[string]any
	client := contractServer(t, http.MethodGet, "/api/instances/2/automations/activity", http.StatusOK, fixture(t, "activity.json"), &request)
	activities, err := client.Activity(context.Background(), "2", 500)
	if err != nil {
		t.Fatal(err)
	}
	if request["query"] != "limit=500" || len(activities) != 4 {
		t.Fatalf("query = %v activities = %d", request["query"], len(activities))
	}
	deleted, failed, simulated, tags := activities[0], activities[1], activities[2], activities[3]
	if !deleted.IsDeletion() || deleted.IsDryRun() || deleted.Outcome != OutcomeSuccess || deleted.RuleID == nil || *deleted.RuleID != 31 || deleted.CreatedAt.IsZero() {
		t.Fatalf("deleted = %+v", deleted)
	}
	var details struct {
		DeleteMode string `json:"deleteMode"`
	}
	if err := json.Unmarshal(deleted.Details, &details); err != nil || details.DeleteMode != "deleteWithFilesPreserveCrossSeeds" {
		t.Fatalf("details = %s err=%v", deleted.Details, err)
	}
	if !failed.IsDeletion() || failed.Outcome != OutcomeFailed || !simulated.IsDeletion() || !simulated.IsDryRun() || tags.IsDeletion() || tags.Hash != "" {
		t.Fatalf("classification failed: %+v %+v %+v", failed, simulated, tags)
	}

	incomplete := `[{"id":0,"action":"deleted_condition","outcome":"success","createdAt":"2026-10-26T00:00:00Z"}]`
	client = contractServer(t, http.MethodGet, "/api/instances/2/automations/activity", http.StatusOK, []byte(incomplete), nil)
	if _, err := client.Activity(context.Background(), "2", 10); err == nil {
		t.Fatal("entry without ID accepted")
	}
}

func TestPreviewSurfacesQuiValidationMessageOutsideError(t *testing.T) {
	var request map[string]any
	client := contractServer(t, http.MethodPost, "/api/instances/2/automations/preview", http.StatusBadRequest, fixture(t, "error_preview_no_action.json"), &request)
	_, err := client.PreviewDelete(context.Background(), "2", probeRule(), 0, 0, "")
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusBadRequest || httpErr.ValidationMessage != "Preview requires either delete or category action to be enabled" {
		t.Fatalf("error = %#v", err)
	}
	if strings.Contains(err.Error(), "Preview requires") {
		t.Fatal("response body leaked into Error()")
	}
}

func TestDeleteRuleValidationNeverReachesQui(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	client, err := New(context.Background(), Config{BaseURL: server.URL, APIKey: secrets.SecretRef{Provider: "env", Key: "X"}, Store: staticStore{value: "key"}})
	if err != nil {
		t.Fatal(err)
	}
	noDelete := probeRule()
	noDelete.Conditions = map[string]any{"schemaVersion": "1", "delete": map[string]any{"enabled": false}}
	both := probeRule()
	both.Conditions["category"] = map[string]any{"enabled": true, "category": "x"}
	unnamed := probeRule()
	unnamed.Name = " "
	ctx := context.Background()
	for name, call := range map[string]func() error{
		"no delete action": func() error { _, err := client.PreviewDelete(ctx, "2", noDelete, 0, 0, ""); return err },
		"delete+category":  func() error { _, err := client.PreviewDelete(ctx, "2", both, 0, 0, ""); return err },
		"unnamed dry-run":  func() error { _, err := client.DryRun(ctx, "2", unnamed); return err },
		"bad view":         func() error { _, err := client.PreviewDelete(ctx, "2", probeRule(), 0, 0, "all"); return err },
		"negative offset":  func() error { _, err := client.PreviewDelete(ctx, "2", probeRule(), 0, -1, ""); return err },
		"missing instance": func() error { _, err := client.Activity(ctx, "", 10); return err },
		"limit too large":  func() error { _, err := client.Activity(ctx, "2", MaxActivityLimit+1); return err },
	} {
		if err := call(); err == nil {
			t.Errorf("%s: error = nil", name)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid requests reached qui %d times", calls.Load())
	}
}
