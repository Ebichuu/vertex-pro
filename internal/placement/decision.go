package placement

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	DecisionAlgorithmVersion = "race-placement-v1"
	freeRatioScale           = int64(1_000_000)

	TargetFixedInstance = "FIXED_INSTANCE"
	TargetInstanceGroup = "INSTANCE_GROUP"
)

type DecisionWeights struct {
	InstanceWeight                   int64
	FreeRatioWeight                  int64
	InflightPenaltyWeight            int64
	CurrentDownloadLoadPenaltyWeight int64
	RecentAddLatencyPenaltyWeight    int64
}

type DecisionInput struct {
	IdentityKey      string
	TargetMode       string
	TargetInstanceID string
	RequiredBytes    int64
	Weights          DecisionWeights
	Candidates       []DecisionCandidate
}

type DecisionCandidate struct {
	InstanceID             string
	StorageTargetID        string
	CanPlace               bool
	HealthState            string
	ProfileState           string
	StorageState           string
	CapacityState          string
	InstanceWeight         int64
	FreeRatioPPM           int64
	EffectiveFreeBytes     int64
	Inflight               int64
	MaxInflight            int64
	CurrentDownloadLoad    int64
	RecentAddLatencyMillis int64
}

type ScoreBreakdown struct {
	InstanceTerm               int64
	FreeRatioTerm              int64
	InflightPenalty            int64
	CurrentDownloadLoadPenalty int64
	RecentAddLatencyPenalty    int64
}

type ScoredCandidate struct {
	Candidate DecisionCandidate
	Score     int64
	Breakdown ScoreBreakdown
	TieBreak  uint64
}

type DecisionRejection struct {
	Candidate DecisionCandidate
	Reason    string
}

type DecisionResult struct {
	AlgorithmVersion string
	Selected         *ScoredCandidate
	Ranked           []ScoredCandidate
	Rejected         []DecisionRejection
}

// Decide ranks already-known candidates without performing I/O or changing state.
// The caller supplies the latest trusted capacity and health facts as a snapshot.
func Decide(input DecisionInput) (DecisionResult, error) {
	if strings.TrimSpace(input.IdentityKey) == "" {
		return DecisionResult{}, errors.New("identity key is required")
	}
	if input.RequiredBytes < 0 {
		return DecisionResult{}, errors.New("required bytes must not be negative")
	}
	if input.TargetMode != TargetFixedInstance && input.TargetMode != TargetInstanceGroup {
		return DecisionResult{}, errors.New("target mode is invalid")
	}
	if input.TargetMode == TargetFixedInstance && strings.TrimSpace(input.TargetInstanceID) == "" {
		return DecisionResult{}, errors.New("fixed target instance is required")
	}
	if err := validateWeights(input.Weights); err != nil {
		return DecisionResult{}, err
	}

	result := DecisionResult{AlgorithmVersion: DecisionAlgorithmVersion, Ranked: make([]ScoredCandidate, 0, len(input.Candidates)), Rejected: make([]DecisionRejection, 0)}
	for _, candidate := range input.Candidates {
		if reason := rejectCandidate(input, candidate); reason != "" {
			result.Rejected = append(result.Rejected, DecisionRejection{Candidate: candidate, Reason: reason})
			continue
		}
		scored, err := scoreCandidate(input.IdentityKey, candidate, input.Weights)
		if err != nil {
			result.Rejected = append(result.Rejected, DecisionRejection{Candidate: candidate, Reason: "PLACEMENT_SCORE_OVERFLOW"})
			continue
		}
		result.Ranked = append(result.Ranked, scored)
	}
	sort.SliceStable(result.Ranked, func(left, right int) bool {
		if result.Ranked[left].Score != result.Ranked[right].Score {
			return result.Ranked[left].Score > result.Ranked[right].Score
		}
		return result.Ranked[left].TieBreak < result.Ranked[right].TieBreak
	})
	sort.SliceStable(result.Rejected, func(left, right int) bool {
		if result.Rejected[left].Candidate.InstanceID != result.Rejected[right].Candidate.InstanceID {
			return result.Rejected[left].Candidate.InstanceID < result.Rejected[right].Candidate.InstanceID
		}
		return result.Rejected[left].Reason < result.Rejected[right].Reason
	})
	if len(result.Ranked) > 0 {
		selected := result.Ranked[0]
		result.Selected = &selected
	}
	return result, nil
}

func validateWeights(weights DecisionWeights) error {
	for name, value := range map[string]int64{
		"instance":              weights.InstanceWeight,
		"free ratio":            weights.FreeRatioWeight,
		"inflight penalty":      weights.InflightPenaltyWeight,
		"download load penalty": weights.CurrentDownloadLoadPenaltyWeight,
		"add latency penalty":   weights.RecentAddLatencyPenaltyWeight,
	} {
		if value <= 0 {
			return fmt.Errorf("%s weight must be positive", name)
		}
	}
	return nil
}

func rejectCandidate(input DecisionInput, candidate DecisionCandidate) string {
	if strings.TrimSpace(candidate.InstanceID) == "" {
		return "INSTANCE_ID_MISSING"
	}
	if strings.TrimSpace(candidate.StorageTargetID) == "" {
		return "STORAGE_TARGET_ID_MISSING"
	}
	if input.TargetMode == TargetFixedInstance && candidate.InstanceID != input.TargetInstanceID {
		return "FIXED_INSTANCE_MISMATCH"
	}
	if !candidate.CanPlace || candidate.HealthState != "AVAILABLE" {
		return "HEALTH_NOT_PLACEABLE"
	}
	if candidate.ProfileState != "VERIFIED" {
		return "PROFILE_NOT_VERIFIED"
	}
	if candidate.StorageState != "VERIFIED" {
		return "STORAGE_UNVERIFIED"
	}
	if candidate.CapacityState != "TRUSTED" {
		return "CAPACITY_UNCERTAIN"
	}
	if candidate.InstanceWeight < 0 {
		return "INSTANCE_WEIGHT_NEGATIVE"
	}
	if candidate.FreeRatioPPM < 0 || candidate.FreeRatioPPM > freeRatioScale {
		return "FREE_RATIO_INVALID"
	}
	if candidate.EffectiveFreeBytes < 0 {
		return "EFFECTIVE_FREE_NEGATIVE"
	}
	if candidate.EffectiveFreeBytes < input.RequiredBytes {
		return "INSUFFICIENT_EFFECTIVE_FREE"
	}
	if candidate.Inflight < 0 {
		return "INFLIGHT_NEGATIVE"
	}
	if candidate.MaxInflight <= 0 {
		return "MAX_INFLIGHT_INVALID"
	}
	if candidate.Inflight >= candidate.MaxInflight {
		return "INFLIGHT_LIMIT_REACHED"
	}
	if candidate.CurrentDownloadLoad < 0 {
		return "CURRENT_DOWNLOAD_LOAD_NEGATIVE"
	}
	if candidate.RecentAddLatencyMillis < 0 {
		return "RECENT_ADD_LATENCY_NEGATIVE"
	}
	return ""
}

func scoreCandidate(identityKey string, candidate DecisionCandidate, weights DecisionWeights) (ScoredCandidate, error) {
	instanceTerm, ok := decisionSafeMul(candidate.InstanceWeight, weights.InstanceWeight)
	if !ok {
		return ScoredCandidate{}, errors.New("instance score overflow")
	}
	freeRatioTerm, ok := safeMulDiv(candidate.FreeRatioPPM, weights.FreeRatioWeight, freeRatioScale)
	if !ok {
		return ScoredCandidate{}, errors.New("free ratio score overflow")
	}
	inflightPenalty, ok := decisionSafeMul(candidate.Inflight, weights.InflightPenaltyWeight)
	if !ok {
		return ScoredCandidate{}, errors.New("inflight score overflow")
	}
	loadPenalty, ok := decisionSafeMul(candidate.CurrentDownloadLoad, weights.CurrentDownloadLoadPenaltyWeight)
	if !ok {
		return ScoredCandidate{}, errors.New("download load score overflow")
	}
	latencyPenalty, ok := decisionSafeMul(candidate.RecentAddLatencyMillis, weights.RecentAddLatencyPenaltyWeight)
	if !ok {
		return ScoredCandidate{}, errors.New("latency score overflow")
	}
	score, ok := addScoreTerms(instanceTerm, freeRatioTerm, -inflightPenalty, -loadPenalty, -latencyPenalty)
	if !ok {
		return ScoredCandidate{}, errors.New("placement score overflow")
	}
	digest := sha256.Sum256([]byte(identityKey + "\x00" + candidate.InstanceID + "\x00" + candidate.StorageTargetID))
	return ScoredCandidate{
		Candidate: candidate,
		Score:     score,
		Breakdown: ScoreBreakdown{
			InstanceTerm:               instanceTerm,
			FreeRatioTerm:              freeRatioTerm,
			InflightPenalty:            inflightPenalty,
			CurrentDownloadLoadPenalty: loadPenalty,
			RecentAddLatencyPenalty:    latencyPenalty,
		},
		TieBreak: binary.BigEndian.Uint64(digest[:8]),
	}, nil
}

func addScoreTerms(terms ...int64) (int64, bool) {
	var total int64
	for _, term := range terms {
		if term > 0 && total > int64(^uint64(0)>>1)-term {
			return 0, false
		}
		if term < 0 && total < -int64(^uint64(0)>>1)-1-term {
			return 0, false
		}
		total += term
	}
	return total, true
}

func decisionSafeMul(left, right int64) (int64, bool) {
	if left == 0 || right == 0 {
		return 0, true
	}
	if (left == -1 && right == -int64(^uint64(0)>>1)-1) || (right == -1 && left == -int64(^uint64(0)>>1)-1) {
		return 0, false
	}
	result := left * right
	return result, result/right == left
}

func safeMulDiv(left, right, divisor int64) (int64, bool) {
	if divisor <= 0 {
		return 0, false
	}
	product, ok := decisionSafeMul(left, right)
	if !ok {
		return 0, false
	}
	return product / divisor, true
}

type QueueItem struct {
	IdentityKey  string
	Official     bool
	DiscoveredAt time.Time
}

// Prioritize returns a deterministic queue order; it never mutates the input.
// Official items are allowed to jump ahead of older non-official work.
func Prioritize(items []QueueItem) ([]QueueItem, error) {
	result := append([]QueueItem(nil), items...)
	for _, item := range result {
		if strings.TrimSpace(item.IdentityKey) == "" {
			return nil, errors.New("queue identity key is required")
		}
		if item.DiscoveredAt.IsZero() {
			return nil, errors.New("queue discovery time is required")
		}
	}
	sort.SliceStable(result, func(left, right int) bool {
		if result[left].Official != result[right].Official {
			return result[left].Official
		}
		if !result[left].DiscoveredAt.Equal(result[right].DiscoveredAt) {
			return result[left].DiscoveredAt.Before(result[right].DiscoveredAt)
		}
		return result[left].IdentityKey < result[right].IdentityKey
	})
	return result, nil
}
