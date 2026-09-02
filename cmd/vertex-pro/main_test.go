package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnvBoolRequiresExplicitTruthyValue(t *testing.T) {
	for value, expected := range map[string]bool{"1": true, "true": true, "YES": true, "on": true, "": false, "0": false, "false": false, "unexpected": false} {
		t.Setenv("TEST_BOOL", value)
		if actual := envBool("TEST_BOOL"); actual != expected {
			t.Fatalf("envBool(%q) = %v, want %v", value, actual, expected)
		}
	}
}

func TestReadProfileVerificationBindingIsStrict(t *testing.T) {
	path := filepath.Join(t.TempDir(), "binding.json")
	content := `{"qb_artifact_digest":"sha256:` + strings.Repeat("0", 64) + `","qui_version":"qui-1","qui_image_digest":"sha256:` + strings.Repeat("1", 64) + `","proxy_route_fingerprint":"sha256:` + strings.Repeat("2", 64) + `","harness_image_digest":"sha256:` + strings.Repeat("3", 64) + `","vertex_commit":"commit-1","test_suite_revision":"g1-v1","verification_report_digest":"sha256:` + strings.Repeat("4", 64) + `"}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	binding, err := readProfileVerificationBinding(path)
	if err != nil {
		t.Fatal(err)
	}
	if binding.QuiVersion != "qui-1" {
		t.Fatalf("binding=%+v", binding)
	}
	if err := os.WriteFile(path, []byte(content+` {}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readProfileVerificationBinding(path); err == nil {
		t.Fatal("trailing JSON was accepted")
	}
}

func TestHealthAndReadinessEndpoints(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/readyz", func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusOK)
	})

	for _, path := range []string{"/healthz", "/readyz"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want %d", path, recorder.Code, http.StatusOK)
		}
	}
}
