package candidatefilter

import (
	"sort"
	"strings"

	"github.com/vertex-pro/vertex-pro/internal/quihealth"
	"github.com/vertex-pro/vertex-pro/internal/ruleaggregate"
	"github.com/vertex-pro/vertex-pro/internal/storageverify"
)

const (
	ReasonInvalidRuleAggregation   = "RULE_AGGREGATION_REJECTED"
	ReasonCandidateInstanceMissing = "CANDIDATE_INSTANCE_ID_MISSING"
	ReasonFixedInstanceMismatch    = "FIXED_INSTANCE_MISMATCH"
	ReasonTargetGroupMissing       = "CANDIDATE_TARGET_GROUP_MISSING"
	ReasonTargetGroupMismatch      = "TARGET_GROUP_MISMATCH"
	ReasonProfileMissing           = "CANDIDATE_PROFILE_MISSING"
	ReasonProfileMismatch          = "PROFILE_MISMATCH"
	ReasonHealthUnavailable        = "HEALTH_NOT_PLACEABLE"
	ReasonStorageTargetMissing     = "CANDIDATE_STORAGE_TARGET_MISSING"
	ReasonStorageUnverified        = "STORAGE_UNVERIFIED"
	ReasonStorageLabelMismatch     = "STORAGE_LABEL_MISMATCH"
)

type Candidate struct {
	InstanceID      string
	TargetGroupIDs  []string
	ProfileID       string
	Health          quihealth.Result
	StorageTarget   storageverify.StorageTarget
	StorageEvidence storageverify.Result
	StorageLabels   []string
}

type Rejection struct {
	Candidate Candidate
	Reason    string
}

type Result struct {
	Accepted []Candidate
	Rejected []Rejection
}

func Filter(aggregation ruleaggregate.Result, candidates []Candidate) Result {
	if aggregation.Rejected || aggregation.Winner.TargetMode == "" {
		result := Result{Rejected: make([]Rejection, 0, len(candidates))}
		for _, candidate := range candidates {
			result.Rejected = append(result.Rejected, Rejection{Candidate: cloneCandidate(candidate), Reason: ReasonInvalidRuleAggregation})
		}
		return result
	}
	result := Result{Accepted: make([]Candidate, 0, len(candidates)), Rejected: make([]Rejection, 0, len(candidates))}
	for _, candidate := range candidates {
		if reason := rejectReason(aggregation, candidate); reason != "" {
			result.Rejected = append(result.Rejected, Rejection{Candidate: cloneCandidate(candidate), Reason: reason})
			continue
		}
		result.Accepted = append(result.Accepted, cloneCandidate(candidate))
	}
	sort.SliceStable(result.Accepted, func(left, right int) bool {
		return result.Accepted[left].InstanceID < result.Accepted[right].InstanceID
	})
	sort.SliceStable(result.Rejected, func(left, right int) bool {
		if result.Rejected[left].Candidate.InstanceID != result.Rejected[right].Candidate.InstanceID {
			return result.Rejected[left].Candidate.InstanceID < result.Rejected[right].Candidate.InstanceID
		}
		return result.Rejected[left].Reason < result.Rejected[right].Reason
	})
	return result
}

func rejectReason(aggregation ruleaggregate.Result, candidate Candidate) string {
	if strings.TrimSpace(candidate.InstanceID) == "" {
		return ReasonCandidateInstanceMissing
	}
	winner := aggregation.Winner
	if winner.TargetMode == "FIXED_INSTANCE" && candidate.InstanceID != winner.TargetInstanceID {
		return ReasonFixedInstanceMismatch
	}
	if winner.TargetMode == "INSTANCE_GROUP" {
		if len(candidate.TargetGroupIDs) == 0 {
			return ReasonTargetGroupMissing
		}
		if !containsAny(candidate.TargetGroupIDs, []string{winner.TargetGroupID}) {
			return ReasonTargetGroupMismatch
		}
	}
	if strings.TrimSpace(candidate.ProfileID) == "" {
		return ReasonProfileMissing
	}
	if len(aggregation.AllowedProfiles) > 0 && !containsAny(aggregation.AllowedProfiles, []string{candidate.ProfileID}) {
		return ReasonProfileMismatch
	}
	if !candidate.Health.CanPlace || candidate.Health.HealthState != quihealth.StateAvailable || candidate.Health.ProfileState != quihealth.StateVerified {
		return ReasonHealthUnavailable
	}
	if strings.TrimSpace(candidate.StorageTarget.ID) == "" {
		return ReasonStorageTargetMissing
	}
	if candidate.StorageEvidence.State != storageverify.StateVerified {
		return ReasonStorageUnverified
	}
	if len(aggregation.StorageLabels) > 0 && !containsAny(aggregation.StorageLabels, candidate.StorageLabels) {
		return ReasonStorageLabelMismatch
	}
	return ""
}

func containsAny(values []string, wanted []string) bool {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			set[value] = struct{}{}
		}
	}
	for _, value := range wanted {
		if _, ok := set[value]; ok {
			return true
		}
	}
	return false
}

func cloneCandidate(candidate Candidate) Candidate {
	clone := candidate
	clone.TargetGroupIDs = append([]string(nil), candidate.TargetGroupIDs...)
	clone.StorageLabels = append([]string(nil), candidate.StorageLabels...)
	return clone
}
