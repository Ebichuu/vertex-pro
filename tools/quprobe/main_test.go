package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/vertex-pro/vertex-pro/internal/qui/control"
	"github.com/vertex-pro/vertex-pro/internal/secrets"
)

type fakeQui struct {
	mu       sync.Mutex
	calls    []string
	matches  int
	previews []map[string]any
}

func (fake *fakeQui) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.calls = append(fake.calls, request.Method+" "+request.URL.Path)
	writer.Header().Set("Content-Type", "application/json")
	switch request.Method + " " + request.URL.Path {
	case "GET /api/instances/2/automations/activity":
		_, _ = writer.Write([]byte(`[{"id":7,"action":"deleted_ratio","outcome":"success","hash":"h","createdAt":"2026-10-26T00:00:00Z"},{"id":6,"action":"deleted_condition","outcome":"dry-run","hash":"h","createdAt":"2026-10-25T00:00:00Z"}]`))
	case "POST /api/instances/2/automations/preview":
		var payload map[string]any
		_ = json.NewDecoder(request.Body).Decode(&payload)
		fake.previews = append(fake.previews, payload)
		_, _ = writer.Write([]byte(`{"totalMatches":` + map[bool]string{true: "1", false: "0"}[fake.matches > 0] + `,"examples":[]}`))
	case "POST /api/instances/2/automations/dry-run":
		writer.WriteHeader(http.StatusAccepted)
		_, _ = writer.Write([]byte(`{"status":"dry-run-completed","activities":[{"id":9,"action":"dry_run_no_match","outcome":"dry-run","createdAt":"2026-10-26T00:00:00Z"}]}`))
	default:
		http.NotFound(writer, request)
	}
}

func probeClient(t *testing.T, fake *fakeQui) *control.Client {
	t.Helper()
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	t.Setenv("QUPROBE_TEST_KEY", "key")
	client, err := control.New(context.Background(), control.Config{BaseURL: server.URL, APIKey: secrets.SecretRef{Provider: "env", Key: "QUPROBE_TEST_KEY"}, Store: secrets.EnvStore{}})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestProbeAutomationsStaysReadOnlyUnlessDryRunRequested(t *testing.T) {
	for _, dryRun := range []bool{false, true} {
		fake := &fakeQui{}
		report, err := probeAutomations(context.Background(), probeClient(t, fake), "2", dryRun)
		if err != nil {
			t.Fatal(err)
		}
		if report.ActivityEntries != 2 || report.ActivityDeletions != 2 || report.ActivityDryRuns != 1 || report.PreviewMatches != 0 {
			t.Fatalf("report = %+v", report)
		}
		want := []string{"GET /api/instances/2/automations/activity", "POST /api/instances/2/automations/preview"}
		if dryRun {
			want = append(want, "POST /api/instances/2/automations/dry-run")
			if report.DryRunStatus != "dry-run-completed" || report.DryRunActivities != 1 {
				t.Fatalf("dry-run report = %+v", report)
			}
		}
		if strings.Join(fake.calls, "|") != strings.Join(want, "|") {
			t.Fatalf("calls = %v, want %v", fake.calls, want)
		}
		name, _ := fake.previews[0]["name"].(string)
		if !strings.HasPrefix(name, "vertex-probe-") {
			t.Fatalf("probe rule name = %q", name)
		}
	}
}

func TestProbeAutomationsFailsWhenProbeRuleMatches(t *testing.T) {
	fake := &fakeQui{matches: 1}
	if _, err := probeAutomations(context.Background(), probeClient(t, fake), "2", true); err == nil || !strings.Contains(err.Error(), "unexpectedly matched") {
		t.Fatalf("error = %v", err)
	}
	for _, call := range fake.calls {
		if strings.HasSuffix(call, "/dry-run") {
			t.Fatal("dry-run ran after the probe rule matched a torrent")
		}
	}
}
