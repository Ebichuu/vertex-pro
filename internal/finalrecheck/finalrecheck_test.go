package finalrecheck

import (
	"testing"

	"github.com/vertex-pro/vertex-pro/internal/racegate"
)

func TestEvaluateAllowsUnchangedAvailableCandidate(t *testing.T) {
	snapshot := baseSnapshot()
	result, err := Evaluate(snapshot, snapshot, availableGate())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Eligible || result.State != StateEligible || len(result.Reasons) != 0 {
		t.Fatalf("result=%+v", result)
	}
}

func TestEvaluateRejectsChangedFrozenFacts(t *testing.T) {
	frozen := baseSnapshot()
	current := frozen
	current.PreparationRevision++
	current.RequiredBytes++
	current.InstanceID = "qb-2"
	current.StorageTargetID = "target-2"
	current.ProfileRevision++
	current.SyncGeneration++
	current.SnapshotRevision++
	current.CapacityRevision++
	current.AllocationEpoch++
	result, err := Evaluate(frozen, current, availableGate())
	if err != nil {
		t.Fatal(err)
	}
	if result.Eligible || result.State != StateRecheckRequired {
		t.Fatalf("result=%+v", result)
	}
	for _, reason := range []string{ReasonPreparationRevisionChanged, ReasonRequiredBytesChanged, ReasonInstanceChanged, ReasonStorageTargetChanged, ReasonProfileRevisionChanged, ReasonSyncGenerationChanged, ReasonSnapshotRevisionChanged, ReasonCapacityRevisionChanged, ReasonAllocationEpochChanged} {
		if !hasReason(result.Reasons, reason) {
			t.Fatalf("reasons=%v missing=%s", result.Reasons, reason)
		}
	}
}

func TestEvaluateRejectsLatestUnavailableFactsAndGateFailures(t *testing.T) {
	frozen := baseSnapshot()
	current := frozen
	current.CandidateAvailable = false
	current.StorageVerified = false
	current.Inflight = current.MaxInflight
	result, err := Evaluate(frozen, current, availableGate())
	if err != nil {
		t.Fatal(err)
	}
	if result.Eligible || result.State != StateUnavailable || !hasReason(result.Reasons, ReasonCandidateUnavailable) || !hasReason(result.Reasons, ReasonStorageUnverified) || !hasReason(result.Reasons, ReasonInflightLimitReached) {
		t.Fatalf("result=%+v", result)
	}
	_, err = Evaluate(frozen, frozen, racegate.Input{RuleState: "BROKEN", TorrentState: "READY", PlacementState: "PREPARED", FinalCandidate: racegate.FinalCandidateAvailable, CurrentJobState: "READY"})
	if err == nil {
		t.Fatal("invalid race gate error = nil")
	}
}

func baseSnapshot() Snapshot {
	return Snapshot{PreparationRevision: 3, RequiredBytes: 100, InstanceID: "qb-1", StorageTargetID: "target-1", ProfileRevision: 4, SyncGeneration: 7, SnapshotRevision: 8, CapacityRevision: 9, AllocationEpoch: 2, EffectiveFreeBytes: 900, CandidateAvailable: true, StorageVerified: true, Inflight: 1, MaxInflight: 4}
}
func availableGate() racegate.Input {
	return racegate.Input{RuleState: "MATCHED", TorrentState: "READY", PlacementState: "PREPARED", FinalCandidate: racegate.FinalCandidateAvailable, CurrentJobState: "READY"}
}
func hasReason(reasons []string, expected string) bool {
	for _, reason := range reasons {
		if reason == expected {
			return true
		}
	}
	return false
}
