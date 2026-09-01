package contracts

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestErrorCodesContract(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	path := filepath.Join(filepath.Dir(filename), "..", "..", "contracts", "error_codes.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read error code contract: %v", err)
	}
	content := string(data)
	if strings.HasPrefix(content, "\ufeff") {
		t.Fatal("error code contract must be UTF-8 without BOM")
	}
	if !strings.Contains(content, "schema_version: 1") || !strings.Contains(content, "client_rule:") {
		t.Fatal("error code contract metadata is incomplete")
	}
	expected := map[string]string{
		"INVALID_REQUEST":              "http_status: 400",
		"VALIDATION_FAILED":            "http_status: 422",
		"UNAUTHENTICATED":              "http_status: 401",
		"FORBIDDEN":                    "http_status: 403",
		"NOT_FOUND":                    "http_status: 404",
		"REVISION_CONFLICT":            "http_status: 409",
		"IDEMPOTENCY_KEY_REUSED":       "http_status: 409",
		"CURSOR_EXPIRED":               "http_status: 409",
		"JOB_STATE_CONFLICT":           "http_status: 409",
		"IDENTITY_ACTIVE_CHAIN_EXISTS": "http_status: 409",
		"QUI_UNREACHABLE":              "http_status: 502",
		"QB_PROXY_UNREACHABLE":         "http_status: 502",
		"RATE_LIMITED":                 "http_status: 429",
		"INTERNAL_ERROR":               "http_status: 500",
	}
	for code, status := range expected {
		if !strings.Contains(content, code+": { "+status) {
			t.Errorf("%s missing expected %s", code, status)
		}
	}
	for _, required := range []string{"retryable: true", "retryable: false", "details: object", "INTERNAL_ERROR"} {
		if !strings.Contains(content, required) {
			t.Errorf("missing error contract requirement %q", required)
		}
	}
}
