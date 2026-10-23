package control

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/vertex-pro/vertex-pro/internal/secrets"
)

func TestAutomationRoundTripPreservesQuiReadbackFields(t *testing.T) {
	const response = `{"id":9,"instanceId":2,"name":"vertex-policy-test","trackerPattern":"*","trackerDomains":["*"],"conditions":{"schemaVersion":"1","delete":{"enabled":true,"mode":"delete","condition":{"operator":"AND","conditions":[{"field":"TAGS","operator":"EQUAL","value":"vertex-policy-test"},{"field":"SEEDING_TIME","operator":"GREATER_THAN","value":"3600"}]}}},"enabled":true,"dryRun":false,"notify":false,"sortOrder":7,"intervalSeconds":60,"createdAt":"2026-10-23T00:00:00Z","updatedAt":"2026-10-23T00:00:00Z"}`
	var saved atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.Method {
		case http.MethodGet:
			_, _ = writer.Write([]byte("[" + response + "]"))
		case http.MethodPut:
			if request.URL.Path != "/api/instances/2/automations/9" {
				t.Errorf("unexpected path %s", request.URL.Path)
			}
			var payload map[string]any
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Error(err)
				return
			}
			if payload["sortOrder"] != float64(7) || payload["notify"] != false || payload["intervalSeconds"] != float64(60) {
				t.Errorf("qui update lost persisted settings: %+v", payload)
			}
			conditions, _ := payload["conditions"].(map[string]any)
			if len(conditions) != 2 || conditions["delete"] == nil {
				t.Errorf("qui update changed actions: %+v", conditions)
			}
			saved.Store(true)
			_, _ = writer.Write([]byte(response))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	client, err := New(context.Background(), Config{BaseURL: server.URL, APIKey: secrets.SecretRef{Provider: "env", Key: "X"}, Store: staticStore{value: "key"}})
	if err != nil {
		t.Fatal(err)
	}
	list, err := client.Automations(context.Background(), "2")
	if err != nil || len(list) != 1 || list[0].SortOrder != 7 || len(list[0].TrackerDomains) != 1 {
		t.Fatalf("qui readback %+v err=%v", list, err)
	}
	list[0].Enabled = false
	if _, err := client.SaveAutomation(context.Background(), "2", list[0]); err != nil || !saved.Load() {
		t.Fatalf("qui update err=%v sent=%v", err, saved.Load())
	}
}
