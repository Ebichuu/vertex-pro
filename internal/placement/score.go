package placement

import (
	"errors"
	"math"
	"sort"
)

type Weights struct {
	InstanceWeight        int64
	FreeBytesWeight       int64
	InflightPenaltyWeight int64
}

type Candidate struct {
	InstanceID         string
	StorageTargetID    string
	Eligible           bool
	Weight             int64
	EffectiveFreeBytes int64
	Inflight           int64
	MaxInflight        int64
}

type RankedCandidate struct {
	Candidate Candidate
	Score     int64
}

type Rejection struct {
	Candidate Candidate
	Reason    string
}

type Result struct {
	Ranked   []RankedCandidate
	Rejected []Rejection
}

func Rank(candidates []Candidate, weights Weights) (Result, error) {
	if weights.InstanceWeight <= 0 || weights.FreeBytesWeight <= 0 || weights.InflightPenaltyWeight <= 0 {
		return Result{}, errors.New("placement weights must be positive")
	}
	result := Result{Ranked: make([]RankedCandidate, 0, len(candidates)), Rejected: make([]Rejection, 0)}
	for _, candidate := range candidates {
		if reason := validateCandidate(candidate); reason != "" {
			result.Rejected = append(result.Rejected, Rejection{Candidate: candidate, Reason: reason})
			continue
		}
		if !candidate.Eligible {
			result.Rejected = append(result.Rejected, Rejection{Candidate: candidate, Reason: "CANDIDATE_NOT_ELIGIBLE"})
			continue
		}
		score, ok := score(candidate, weights)
		if !ok {
			result.Rejected = append(result.Rejected, Rejection{Candidate: candidate, Reason: "PLACEMENT_SCORE_OVERFLOW"})
			continue
		}
		result.Ranked = append(result.Ranked, RankedCandidate{Candidate: candidate, Score: score})
	}
	sort.SliceStable(result.Ranked, func(left, right int) bool {
		if result.Ranked[left].Score != result.Ranked[right].Score {
			return result.Ranked[left].Score > result.Ranked[right].Score
		}
		if result.Ranked[left].Candidate.InstanceID != result.Ranked[right].Candidate.InstanceID {
			return result.Ranked[left].Candidate.InstanceID < result.Ranked[right].Candidate.InstanceID
		}
		return result.Ranked[left].Candidate.StorageTargetID < result.Ranked[right].Candidate.StorageTargetID
	})
	sort.SliceStable(result.Rejected, func(left, right int) bool {
		if result.Rejected[left].Candidate.InstanceID != result.Rejected[right].Candidate.InstanceID {
			return result.Rejected[left].Candidate.InstanceID < result.Rejected[right].Candidate.InstanceID
		}
		return result.Rejected[left].Reason < result.Rejected[right].Reason
	})
	return result, nil
}

func validateCandidate(candidate Candidate) string {
	if candidate.InstanceID == "" {
		return "INSTANCE_ID_MISSING"
	}
	if candidate.StorageTargetID == "" {
		return "STORAGE_TARGET_ID_MISSING"
	}
	if candidate.Weight < 0 {
		return "INSTANCE_WEIGHT_NEGATIVE"
	}
	if candidate.EffectiveFreeBytes < 0 {
		return "EFFECTIVE_FREE_NEGATIVE"
	}
	if candidate.Inflight < 0 {
		return "INFLIGHT_NEGATIVE"
	}
	if candidate.MaxInflight <= 0 {
		return "MAX_INFLIGHT_INVALID"
	}
	if candidate.Inflight > candidate.MaxInflight {
		return "INFLIGHT_EXCEEDS_MAX"
	}
	return ""
}

func score(candidate Candidate, weights Weights) (int64, bool) {
	positive, ok := safeMul(candidate.Weight, weights.InstanceWeight)
	if !ok {
		return 0, false
	}
	free, ok := safeMul(candidate.EffectiveFreeBytes, weights.FreeBytesWeight)
	if !ok {
		return 0, false
	}
	penalty, ok := safeMul(candidate.Inflight, weights.InflightPenaltyWeight)
	if !ok || positive > math.MaxInt64-free {
		return 0, false
	}
	total := positive + free
	if penalty > 0 && total < math.MinInt64+penalty {
		return 0, false
	}
	return total - penalty, true
}

func safeMul(left, right int64) (int64, bool) {
	if left == 0 || right == 0 {
		return 0, true
	}
	if left == math.MinInt64 && right == -1 || right == math.MinInt64 && left == -1 {
		return 0, false
	}
	result := left * right
	if result/right != left {
		return 0, false
	}
	return result, true
}
