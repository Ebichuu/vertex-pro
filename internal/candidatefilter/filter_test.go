package candidatefilter

import (
	"testing"

	"github.com/vertex-pro/vertex-pro/internal/quihealth"
	"github.com/vertex-pro/vertex-pro/internal/ruleaggregate"
	"github.com/vertex-pro/vertex-pro/internal/storageverify"
)

func TestFilterAcceptsMatchingCandidateAndSortsResults(t *testing.T) {
	aggregation := ruleaggregate.Result{Winner: ruleaggregate.Match{TargetMode: "INSTANCE_GROUP", TargetGroupID: "group-a"}, AllowedProfiles: []string{"QB_522"}, StorageLabels: []string{"ssd"}}
	base := Candidate{ProfileID: "QB_522", Health: availableHealth("qb"), StorageTarget: storageverify.StorageTarget{ID: "target"}, StorageEvidence: storageverify.Result{State: storageverify.StateVerified}, StorageLabels: []string{"ssd"}, TargetGroupIDs: []string{"group-a"}}
	result := Filter(aggregation, []Candidate{{InstanceID: "qb-b", TargetGroupIDs: base.TargetGroupIDs, ProfileID: base.ProfileID, Health: base.Health, StorageTarget: base.StorageTarget, StorageEvidence: base.StorageEvidence, StorageLabels: base.StorageLabels}, {InstanceID: "qb-a", TargetGroupIDs: base.TargetGroupIDs, ProfileID: base.ProfileID, Health: base.Health, StorageTarget: base.StorageTarget, StorageEvidence: base.StorageEvidence, StorageLabels: base.StorageLabels}})
	if len(result.Accepted) != 2 || result.Accepted[0].InstanceID != "qb-a" || len(result.Rejected) != 0 {
		t.Fatalf("result=%+v", result)
	}
}

func TestFilterReturnsStructuredReasonForEachGate(t *testing.T) {
	aggregation := ruleaggregate.Result{Winner: ruleaggregate.Match{TargetMode: "FIXED_INSTANCE", TargetInstanceID: "qb-fixed"}, AllowedProfiles: []string{"QB_522"}, StorageLabels: []string{"ssd"}}
	base := Candidate{InstanceID: "qb-other", ProfileID: "QB_438", Health: availableHealth("qb-other"), StorageTarget: storageverify.StorageTarget{ID: "target"}, StorageEvidence: storageverify.Result{State: storageverify.StateUnverified}, StorageLabels: []string{"hdd"}}
	cases := []struct {
		name   string
		input  Candidate
		reason string
	}{
		{name: "fixed instance", input: base, reason: ReasonFixedInstanceMismatch},
		{name: "profile", input: func() Candidate { candidate := base; candidate.InstanceID = "qb-fixed"; return candidate }(), reason: ReasonProfileMismatch},
		{name: "health", input: func() Candidate {
			candidate := base
			candidate.InstanceID = "qb-fixed"
			candidate.ProfileID = "QB_522"
			candidate.Health.CanPlace = false
			return candidate
		}(), reason: ReasonHealthUnavailable},
		{name: "storage verification", input: func() Candidate {
			candidate := base
			candidate.InstanceID = "qb-fixed"
			candidate.ProfileID = "QB_522"
			candidate.Health = availableHealth("qb-fixed")
			return candidate
		}(), reason: ReasonStorageUnverified},
		{name: "storage label", input: func() Candidate {
			candidate := base
			candidate.InstanceID = "qb-fixed"
			candidate.ProfileID = "QB_522"
			candidate.Health = availableHealth("qb-fixed")
			candidate.StorageEvidence.State = storageverify.StateVerified
			return candidate
		}(), reason: ReasonStorageLabelMismatch},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			result := Filter(aggregation, []Candidate{testCase.input})
			if len(result.Rejected) != 1 || result.Rejected[0].Reason != testCase.reason {
				t.Fatalf("result=%+v want=%s", result, testCase.reason)
			}
		})
	}
}

func TestFilterRejectsMissingAndInvalidAggregateInputs(t *testing.T) {
	result := Filter(ruleaggregate.Result{Rejected: true, Reason: "PROFILE_CONSTRAINT_INTERSECTION_EMPTY"}, []Candidate{{InstanceID: "qb"}})
	if len(result.Rejected) != 1 || result.Rejected[0].Reason != ReasonInvalidRuleAggregation {
		t.Fatalf("result=%+v", result)
	}
	aggregation := ruleaggregate.Result{Winner: ruleaggregate.Match{TargetMode: "INSTANCE_GROUP", TargetGroupID: "group-a"}}
	result = Filter(aggregation, []Candidate{{ProfileID: "QB_522"}})
	if len(result.Rejected) != 1 || result.Rejected[0].Reason != ReasonCandidateInstanceMissing {
		t.Fatalf("missing instance result=%+v", result)
	}
	candidate := Candidate{InstanceID: "qb", ProfileID: "QB_522", Health: availableHealth("qb"), StorageTarget: storageverify.StorageTarget{ID: "target"}, StorageEvidence: storageverify.Result{State: storageverify.StateVerified}}
	result = Filter(aggregation, []Candidate{candidate})
	if len(result.Rejected) != 1 || result.Rejected[0].Reason != ReasonTargetGroupMissing {
		t.Fatalf("missing group result=%+v", result)
	}
}

func availableHealth(instanceID string) quihealth.Result {
	return quihealth.Result{InstanceID: instanceID, HealthState: quihealth.StateAvailable, ProfileState: quihealth.StateVerified, CanPlace: true, SnapshotRevision: 1}
}
