package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadSourcesHasSameDigestForLFAndCRLF(t *testing.T) {
	writeSources := func(root, newline string) {
		t.Helper()
		contents := map[string]string{
			"contracts/openapi/openapi.json":     "{\n  \"paths\": {}\n}\n",
			"contracts/error_codes.yaml":         "errors:\n  TEST: { http_status: 500, retryable: false }\n",
			"contracts/event_types.yaml":         "events:\n  test.event:\n",
			"contracts/user_status_mapping.yaml": "visible_labels:\n  TEST: Test\n",
			"contracts/state_enums.yaml":         "states:\n  test:\n    values: [ONE]\n",
		}
		for name, content := range contents {
			path := filepath.Join(root, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(strings.ReplaceAll(content, "\n", newline)), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}

	lfRoot := t.TempDir()
	crlfRoot := t.TempDir()
	writeSources(lfRoot, "\n")
	writeSources(crlfRoot, "\r\n")
	lfData, lfSources, err := loadSources(lfRoot)
	if err != nil {
		t.Fatal(err)
	}
	crlfData, crlfSources, err := loadSources(crlfRoot)
	if err != nil {
		t.Fatal(err)
	}
	if digest(lfSources) != digest(crlfSources) || renderGo(lfData, digest(lfSources)) != renderGo(crlfData, digest(crlfSources)) {
		t.Fatal("contract generation differs by source line endings")
	}
}
