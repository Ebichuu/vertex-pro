package quihealth

import (
	"errors"
	"github.com/vertex-pro/vertex-pro/internal/qbprofile"
	"testing"
	"time"
)

func TestAggregateFailClosedTruthTable(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	base := Input{InstanceID: "qb-1", ControlReachable: true, ControlStatus: "READY", Profile: qbprofile.Profile{ID: "QB_522", ProfileVerificationState: StateVerified}, SyncReady: true, SyncState: "READY", SnapshotRevision: 1, SnapshotAt: now.Add(-time.Minute), Now: now}
	tests := []struct {
		name       string
		input      Input
		wantHealth string
		wantPlace  bool
		wantReason string
	}{
		{"available", base, StateAvailable, true, ""},
		{"unreachable", func() Input { v := base; v.ControlReachable = false; return v }(), StateOffline, false, "unreachable"},
		{"expected profile", func() Input { v := base; v.Profile.ProfileVerificationState = StateExpected; return v }(), "READY", false, "verification"},
		{"sync not ready", func() Input { v := base; v.SyncReady = false; return v }(), "READY", false, "snapshot"},
		{"stale snapshot", func() Input { v := base; v.SnapshotAt = now.Add(-6 * time.Minute); return v }(), StateStale, false, "stale"},
		{"probe error", func() Input { v := base; v.ProfileError = errors.New("probe failed"); return v }(), "READY", false, "probe failed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := Aggregate(test.input)
			if result.HealthState != test.wantHealth || result.CanPlace != test.wantPlace {
				t.Fatalf("result = %+v", result)
			}
			if test.wantReason != "" && !contains(result.Reason, test.wantReason) {
				t.Fatalf("reason=%q want=%q", result.Reason, test.wantReason)
			}
		})
	}
}

func TestAggregateDoesNotPromoteExpectedToVerified(t *testing.T) {
	now := time.Now()
	input := Input{InstanceID: "qb-1", ControlReachable: true, ControlStatus: "READY", Profile: qbprofile.Profile{ID: "QB_438", ProfileVerificationState: StateExpected}, SyncReady: true, SyncState: "READY", SnapshotRevision: 4, SnapshotAt: now.Add(-time.Second), Now: now}
	result := Aggregate(input)
	if result.ProfileState != StateExpected || result.CapabilityState != StateExpected || result.CanAdd || result.CanPlace {
		t.Fatalf("expected profile promoted = %+v", result)
	}
}

func contains(value, part string) bool {
	for i := 0; i+len(part) <= len(value); i++ {
		if value[i:i+len(part)] == part {
			return true
		}
	}
	return false
}
