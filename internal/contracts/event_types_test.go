package contracts

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestEventTypesContract(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	path := filepath.Join(filepath.Dir(filename), "..", "..", "contracts", "event_types.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read event type contract: %v", err)
	}
	content := string(data)
	if strings.HasPrefix(content, "\ufeff") {
		t.Fatal("event type contract must be UTF-8 without BOM")
	}
	for _, required := range []string{
		"schema_version: 1",
		"stream.ready:",
		"stream.reset_required:",
		"job.preparation.changed:",
		"job.state.changed:",
		"attempt.state.changed:",
		"command.state.changed:",
		"capacity.state.changed:",
		"instance.health.changed:",
		"stats.realtime:",
		"system.alert:",
	} {
		if !strings.Contains(content, required) {
			t.Errorf("missing event contract entry %q", required)
		}
	}
	for _, required := range []string{
		"persisted_days_minimum: 7",
		"replay_limit_per_connection: 5000",
		"expired_or_over_limit: emit_stream.reset_required_then_close",
		"no_last_event_id: emit_stream.ready_then_live_only",
		"interval_seconds: 15",
		"consumes_event_id: false",
	} {
		if !strings.Contains(content, required) {
			t.Errorf("missing event boundary %q", required)
		}
	}
	if !strings.Contains(content, "stats.realtime:\n    persisted: false\n    replayable: false") {
		t.Fatal("stats.realtime must be transient and non-replayable")
	}
	for _, event := range []string{"job.state.changed", "attempt.state.changed", "command.state.changed", "capacity.state.changed", "instance.health.changed", "system.alert"} {
		block := event + ":\n    persisted: true\n    replayable: true"
		if !strings.Contains(content, block) {
			t.Errorf("%s must be persisted and replayable", event)
		}
	}
}
