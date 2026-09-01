package availability

import (
	"testing"

	"github.com/vertex-pro/vertex-pro/internal/capacityprojection"
	"github.com/vertex-pro/vertex-pro/internal/capacitysnapshot"
)

func TestEvaluateReturnsAvailableOnlyWhenAllGatesPass(t *testing.T) {
	effective := int64(900)
	result := Evaluate(Input{CandidateAccepted: true, Snapshot: trustedSnapshot(), Projection: capacityprojection.Result{State: capacityprojection.StateTrusted, EffectiveFreeBytes: &effective}, StorageVerified: true, RequiredBytes: 800, Inflight: 1, MaxInflight: 2})
	if !result.Available || result.State != StateAvailable || result.EffectiveFreeBytes != 900 || len(result.Reasons) != 0 {
		t.Fatalf("result=%+v", result)
	}
}

func TestEvaluateFailsClosedForCapacityAndStorage(t *testing.T) {
	effective := int64(100)
	cases := []struct {
		name   string
		input  Input
		reason string
		state  string
	}{
		{name: "candidate", input: Input{Snapshot: trustedSnapshot(), Projection: trustedProjection(&effective), StorageVerified: true, RequiredBytes: 1, MaxInflight: 1}, reason: ReasonCandidateRejected, state: StateUnavailable},
		{name: "snapshot", input: Input{CandidateAccepted: true, Snapshot: capacitysnapshot.Result{State: capacitysnapshot.StateUntrusted}, Projection: trustedProjection(&effective), StorageVerified: true, RequiredBytes: 1, MaxInflight: 1}, reason: ReasonSnapshotUntrusted, state: StateUncertain},
		{name: "projection", input: Input{CandidateAccepted: true, Snapshot: trustedSnapshot(), Projection: capacityprojection.Result{State: capacityprojection.StateUncertain}, StorageVerified: true, RequiredBytes: 1, MaxInflight: 1}, reason: ReasonProjectionUntrusted, state: StateUncertain},
		{name: "storage", input: Input{CandidateAccepted: true, Snapshot: trustedSnapshot(), Projection: trustedProjection(&effective), RequiredBytes: 1, MaxInflight: 1}, reason: ReasonStorageUnverified, state: StateUnavailable},
		{name: "insufficient", input: Input{CandidateAccepted: true, Snapshot: trustedSnapshot(), Projection: trustedProjection(&effective), StorageVerified: true, RequiredBytes: 101, MaxInflight: 1}, reason: ReasonInsufficientEffectiveFree, state: StateUnavailable},
		{name: "inflight", input: Input{CandidateAccepted: true, Snapshot: trustedSnapshot(), Projection: trustedProjection(&effective), StorageVerified: true, RequiredBytes: 1, Inflight: 2, MaxInflight: 2}, reason: ReasonInflightLimitReached, state: StateUnavailable},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			result := Evaluate(testCase.input)
			if result.Available || result.State != testCase.state || !hasReason(result.Reasons, testCase.reason) {
				t.Fatalf("result=%+v want=%s/%s", result, testCase.state, testCase.reason)
			}
		})
	}
}

func TestEvaluateRejectsInvalidNumericInputs(t *testing.T) {
	effective := int64(100)
	result := Evaluate(Input{CandidateAccepted: true, Snapshot: trustedSnapshot(), Projection: trustedProjection(&effective), StorageVerified: true, RequiredBytes: -1, Inflight: -1, MaxInflight: 0})
	if result.Available || !hasReason(result.Reasons, ReasonRequiredBytesNegative) || !hasReason(result.Reasons, ReasonInflightNegative) || !hasReason(result.Reasons, ReasonMaxInflightInvalid) {
		t.Fatalf("result=%+v", result)
	}
}

func trustedSnapshot() capacitysnapshot.Result {
	return capacitysnapshot.Result{State: capacitysnapshot.StateTrusted, Snapshot: &capacitysnapshot.Snapshot{PhysicalFreeBytes: 1000, Revision: 1, Source: "fixture", Digest: "sha256:fixture"}}
}
func trustedProjection(effective *int64) capacityprojection.Result {
	return capacityprojection.Result{State: capacityprojection.StateTrusted, EffectiveFreeBytes: effective}
}
func hasReason(reasons []string, expected string) bool {
	for _, reason := range reasons {
		if reason == expected {
			return true
		}
	}
	return false
}
