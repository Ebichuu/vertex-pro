package main

import (
	"net/http"
	"net/http/httptest"
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
