package quihealth

import (
	"strings"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/qbprofile"
)

const (
	StateExpected  = "EXPECTED"
	StateVerified  = "VERIFIED"
	StateAvailable = "AVAILABLE"
	StateStale     = "STALE"
	StateOffline   = "OFFLINE"
)

type Input struct {
	InstanceID       string
	ControlReachable bool
	ControlStatus    string
	Profile          qbprofile.Profile
	ProfileError     error
	SyncReady        bool
	SyncState        string
	SnapshotRevision uint64
	SnapshotAt       time.Time
	Now              time.Time
	LastError        error
}

type Result struct {
	InstanceID       string
	HealthState      string
	ProfileState     string
	CapabilityState  string
	CanRead          bool
	CanSync          bool
	CanPlace         bool
	CanAdd           bool
	Reason           string
	SnapshotRevision uint64
}

func Aggregate(input Input) Result {
	result := Result{InstanceID: input.InstanceID, HealthState: StateOffline, ProfileState: StateExpected, CapabilityState: StateExpected, SnapshotRevision: input.SnapshotRevision}
	if strings.TrimSpace(input.InstanceID) == "" {
		result.Reason = "instance ID is required"
		return result
	}
	if !input.ControlReachable {
		result.Reason = "qui control is unreachable"
		return result
	}
	result.HealthState = strings.ToUpper(strings.TrimSpace(input.ControlStatus))
	if result.HealthState == "" {
		result.HealthState = StateExpected
	}
	if result.HealthState == StateOffline {
		result.Reason = "instance is offline"
		return result
	}
	result.CanRead = true
	if input.ProfileError != nil || input.Profile.ID == "" {
		result.Reason = firstError(input.ProfileError, "profile is unavailable")
		return result
	}
	result.ProfileState = input.Profile.ProfileVerificationState
	if result.ProfileState == "" {
		result.ProfileState = StateExpected
	}
	if result.ProfileState != StateVerified {
		result.Reason = "profile verification is not complete"
		return result
	}
	if !input.SyncReady || input.SyncState != "READY" {
		result.Reason = "sync snapshot is not ready"
		return result
	}
	if input.SnapshotRevision == 0 || input.SnapshotAt.IsZero() {
		result.Reason = "sync snapshot evidence is missing"
		return result
	}
	if !input.Now.IsZero() && input.SnapshotAt.After(input.Now) {
		result.Reason = "sync snapshot timestamp is in the future"
		return result
	}
	if !input.Now.IsZero() && input.Now.Sub(input.SnapshotAt) > 5*time.Minute {
		result.HealthState = StateStale
		result.Reason = "sync snapshot is stale"
		return result
	}
	if input.LastError != nil {
		result.Reason = input.LastError.Error()
		return result
	}
	result.HealthState = StateAvailable
	result.CapabilityState = StateVerified
	result.CanSync, result.CanPlace, result.CanAdd = true, true, true
	return result
}

func firstError(err error, fallback string) string {
	if err == nil || strings.TrimSpace(err.Error()) == "" {
		return fallback
	}
	return err.Error()
}
