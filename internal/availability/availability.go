package availability

import (
	"github.com/vertex-pro/vertex-pro/internal/capacityprojection"
	"github.com/vertex-pro/vertex-pro/internal/capacitysnapshot"
)

const (
	StateAvailable                  = "AVAILABLE"
	StateUnavailable                = "UNAVAILABLE"
	StateUncertain                  = "CAPACITY_UNCERTAIN"
	ReasonCandidateRejected         = "CANDIDATE_REJECTED"
	ReasonSnapshotUntrusted         = "SNAPSHOT_UNTRUSTED"
	ReasonProjectionUntrusted       = "PROJECTION_UNTRUSTED"
	ReasonStorageUnverified         = "STORAGE_UNVERIFIED"
	ReasonRequiredBytesNegative     = "REQUIRED_BYTES_NEGATIVE"
	ReasonEffectiveFreeMissing      = "EFFECTIVE_FREE_MISSING"
	ReasonInsufficientEffectiveFree = "INSUFFICIENT_EFFECTIVE_FREE"
	ReasonInflightNegative          = "INFLIGHT_NEGATIVE"
	ReasonMaxInflightInvalid        = "MAX_INFLIGHT_INVALID"
	ReasonInflightLimitReached      = "INFLIGHT_LIMIT_REACHED"
)

type Input struct {
	CandidateAccepted bool
	Snapshot          capacitysnapshot.Result
	Projection        capacityprojection.Result
	StorageVerified   bool
	RequiredBytes     int64
	Inflight          int64
	MaxInflight       int64
}

type Result struct {
	State              string
	Available          bool
	EffectiveFreeBytes int64
	Reasons            []string
}

func Evaluate(input Input) Result {
	reasons := make([]string, 0)
	if !input.CandidateAccepted {
		reasons = append(reasons, ReasonCandidateRejected)
	}
	if input.Snapshot.State != capacitysnapshot.StateTrusted || input.Snapshot.Snapshot == nil {
		reasons = append(reasons, ReasonSnapshotUntrusted)
	}
	if input.Projection.State != capacityprojection.StateTrusted || input.Projection.EffectiveFreeBytes == nil {
		reasons = append(reasons, ReasonProjectionUntrusted)
	}
	if !input.StorageVerified {
		reasons = append(reasons, ReasonStorageUnverified)
	}
	if input.RequiredBytes < 0 {
		reasons = append(reasons, ReasonRequiredBytesNegative)
	}
	if input.Projection.EffectiveFreeBytes == nil {
		reasons = append(reasons, ReasonEffectiveFreeMissing)
	}
	if input.Projection.EffectiveFreeBytes != nil && *input.Projection.EffectiveFreeBytes < input.RequiredBytes {
		reasons = append(reasons, ReasonInsufficientEffectiveFree)
	}
	if input.Inflight < 0 {
		reasons = append(reasons, ReasonInflightNegative)
	}
	if input.MaxInflight <= 0 {
		reasons = append(reasons, ReasonMaxInflightInvalid)
	}
	if input.MaxInflight > 0 && input.Inflight >= input.MaxInflight {
		reasons = append(reasons, ReasonInflightLimitReached)
	}
	if len(reasons) > 0 {
		state := StateUnavailable
		for _, reason := range reasons {
			if reason == ReasonSnapshotUntrusted || reason == ReasonProjectionUntrusted || reason == ReasonEffectiveFreeMissing {
				state = StateUncertain
				break
			}
		}
		return Result{State: state, Reasons: reasons}
	}
	return Result{State: StateAvailable, Available: true, EffectiveFreeBytes: *input.Projection.EffectiveFreeBytes}
}
