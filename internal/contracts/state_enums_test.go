package contracts

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestStateEnumsContract(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	path := filepath.Join(filepath.Dir(filename), "..", "..", "contracts", "state_enums.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read state enum contract: %v", err)
	}
	content := string(data)
	if strings.HasPrefix(content, "\ufeff") {
		t.Fatal("state enum contract must be UTF-8 without BOM")
	}
	if !strings.Contains(content, "schema_version: 1") {
		t.Fatal("schema_version: 1 is required")
	}

	expected := map[string][]string{
		"job_state":         {"DISCOVERED", "PREPARING", "READY", "WAITING_CAPACITY", "PREPARE_FAILED", "RESERVED", "ADD_SENT", "AWAITING_CONFIRMATION", "HASH_SEEN", "ALREADY_PRESENT", "ADD_FAILED", "UNKNOWN", "RECONCILED_CONFIRMED", "RECONCILED_NO_EFFECT", "EXPIRED_UNKNOWN", "EXPIRED"},
		"rule_state":        {"PENDING", "RUNNING", "MATCHED", "REJECTED"},
		"torrent_state":     {"PENDING", "FETCHING", "READY", "FAILED"},
		"placement_state":   {"PENDING", "PREPARED", "WAITING_CAPACITY"},
		"attempt_state":     {"RESERVED", "ADD_SENT", "AWAITING_CONFIRMATION", "HASH_SEEN", "ALREADY_PRESENT", "ADD_FAILED", "UNKNOWN", "RECONCILED_CONFIRMED", "RECONCILED_NO_EFFECT", "EXPIRED_UNKNOWN"},
		"reservation_state": {"ACTIVE", "REFLECTING", "RELEASED"},
		"capacity_state":    {"TRUSTED", "CAPACITY_UNCERTAIN", "RECONCILING"},
		"amount_left_state": {"KNOWN", "UNKNOWN"},
	}
	for domain, values := range expected {
		for _, value := range values {
			if !strings.Contains(content, value) {
				t.Errorf("%s is missing value %s", domain, value)
			}
		}
	}
	for _, required := range []string{
		"active_delivery_chain: [RESERVED, ADD_SENT, AWAITING_CONFIRMATION, UNKNOWN, HASH_SEEN, RECONCILED_CONFIRMED, EXPIRED_UNKNOWN]",
		"rule_state: [PENDING -> RUNNING, RUNNING -> MATCHED, RUNNING -> REJECTED]",
		"torrent_state: [PENDING -> FETCHING, FETCHING -> READY, FETCHING -> FAILED]",
	} {
		if !strings.Contains(content, required) {
			t.Errorf("missing state invariant %q", required)
		}
	}
}
