package contracts

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestOpenAPIContractFoundation(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	path := filepath.Join(filepath.Dir(filename), "..", "..", "contracts", "openapi", "openapi.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read OpenAPI contract: %v", err)
	}

	var document struct {
		OpenAPI string `json:"openapi"`
		Info    struct {
			Title   string `json:"title"`
			Version string `json:"version"`
		} `json:"info"`
		Servers    []map[string]string        `json:"servers"`
		Paths      map[string]json.RawMessage `json:"paths"`
		Components struct {
			Parameters map[string]json.RawMessage `json:"parameters"`
			Schemas    map[string]json.RawMessage `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatalf("decode OpenAPI contract: %v", err)
	}
	if document.OpenAPI != "3.1.0" {
		t.Fatalf("openapi = %q, want 3.1.0", document.OpenAPI)
	}
	if document.Info.Title == "" || document.Info.Version == "" {
		t.Fatal("OpenAPI info title and version are required")
	}
	if len(document.Servers) != 1 || document.Servers[0]["url"] != "/api/v1" {
		t.Fatalf("servers = %#v, want one /api/v1 server", document.Servers)
	}
	for _, path := range []string{"/dashboard", "/sites", "/feeds", "/rules", "/jobs", "/commands/{command_id}", "/instances", "/events/stream"} {
		if _, ok := document.Paths[path]; !ok {
			t.Errorf("missing required path %q", path)
		}
	}
	for _, name := range []string{"Limit", "Cursor", "IdempotencyKey", "LastEventId"} {
		if _, ok := document.Components.Parameters[name]; !ok {
			t.Errorf("missing required parameter %q", name)
		}
	}
	for _, name := range []string{"Error", "Page", "Command"} {
		if _, ok := document.Components.Schemas[name]; !ok {
			t.Errorf("missing required schema %q", name)
		}
	}
}
