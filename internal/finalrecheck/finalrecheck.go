package finalrecheck

import "github.com/vertex-pro/vertex-pro/internal/racegate"

const (
	StateEligible                    = "ELIGIBLE"
	StateRecheckRequired             = "RECHECK_REQUIRED"
	StateUnavailable                 = "UNAVAILABLE"
	ReasonGateRejected               = "RACE_GATE_REJECTED"
	ReasonPreparationRevisionChanged = "PREPARATION_REVISION_CHANGED"
	ReasonRequiredBytesChanged       = "REQUIRED_BYTES_CHANGED"
	ReasonInstanceChanged            = "INSTANCE_CHANGED"
	ReasonStorageTargetChanged       = "STORAGE_TARGET_CHANGED"
	ReasonProfileRevisionChanged     = "PROFILE_REVISION_CHANGED"
	ReasonSyncGenerationChanged      = "SYNC_GENERATION_CHANGED"
	ReasonSnapshotRevisionChanged    = "SNAPSHOT_REVISION_CHANGED"
	ReasonCapacityRevisionChanged    = "CAPACITY_REVISION_CHANGED"
	ReasonAllocationEpochChanged     = "ALLOCATION_EPOCH_CHANGED"
	ReasonCandidateUnavailable       = "CANDIDATE_NO_LONGER_AVAILABLE"
	ReasonStorageUnverified          = "STORAGE_UNVERIFIED"
	ReasonInflightLimitReached       = "INFLIGHT_LIMIT_REACHED"
)

type Snapshot struct {
	PreparationRevision int64
	RequiredBytes       int64
	InstanceID          string
	StorageTargetID     string
	ProfileRevision     int64
	SyncGeneration      int64
	SnapshotRevision    int64
	CapacityRevision    int64
	AllocationEpoch     int64
	EffectiveFreeBytes  int64
	CandidateAvailable  bool
	StorageVerified     bool
	Inflight            int64
	MaxInflight         int64
}

type Result struct {
	State    string
	Eligible bool
	Reasons  []string
}

func Evaluate(frozen, current Snapshot, gate racegate.Input) (Result, error) {
	if _, err := racegate.Evaluate(gate); err != nil {
		return Result{}, err
	}
	reasons := make([]string, 0)
	if frozen.PreparationRevision != current.PreparationRevision {
		reasons = append(reasons, ReasonPreparationRevisionChanged)
	}
	if frozen.RequiredBytes != current.RequiredBytes {
		reasons = append(reasons, ReasonRequiredBytesChanged)
	}
	if frozen.InstanceID != current.InstanceID {
		reasons = append(reasons, ReasonInstanceChanged)
	}
	if frozen.StorageTargetID != current.StorageTargetID {
		reasons = append(reasons, ReasonStorageTargetChanged)
	}
	if frozen.ProfileRevision != current.ProfileRevision {
		reasons = append(reasons, ReasonProfileRevisionChanged)
	}
	if frozen.SyncGeneration != current.SyncGeneration {
		reasons = append(reasons, ReasonSyncGenerationChanged)
	}
	if frozen.SnapshotRevision != current.SnapshotRevision {
		reasons = append(reasons, ReasonSnapshotRevisionChanged)
	}
	if frozen.CapacityRevision != current.CapacityRevision {
		reasons = append(reasons, ReasonCapacityRevisionChanged)
	}
	if frozen.AllocationEpoch != current.AllocationEpoch {
		reasons = append(reasons, ReasonAllocationEpochChanged)
	}
	if !current.CandidateAvailable {
		reasons = append(reasons, ReasonCandidateUnavailable)
	}
	if !current.StorageVerified {
		reasons = append(reasons, ReasonStorageUnverified)
	}
	if current.MaxInflight <= 0 || current.Inflight >= current.MaxInflight {
		reasons = append(reasons, ReasonInflightLimitReached)
	}
	if current.EffectiveFreeBytes < current.RequiredBytes {
		reasons = append(reasons, ReasonCandidateUnavailable)
	}
	if len(reasons) > 0 {
		state := StateRecheckRequired
		for _, reason := range reasons {
			if reason == ReasonCandidateUnavailable || reason == ReasonStorageUnverified || reason == ReasonInflightLimitReached {
				state = StateUnavailable
				break
			}
		}
		return Result{State: state, Reasons: unique(reasons)}, nil
	}
	return Result{State: StateEligible, Eligible: true}, nil
}

func unique(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
