package deletionvalue

import (
	"errors"
	"math/big"
	"sort"
	"strings"
	"time"
)

type Candidate struct {
	ID                       string
	InstanceID               string
	QBTorrentID              string
	SizeBytes                int64
	UploadRateBytesPerSecond int64
	PeerCount                int64
	ProtectedUntil           time.Time
}

type Policy struct {
	MinUploadRateBytesPerSecond int64
	MinPeerCount                int64
}

type Evaluation struct {
	Candidate    Candidate
	Eligible     bool
	Reason       string
	ValuePerByte float64
}

type Result struct {
	Evaluations []Evaluation
}

const (
	ReasonInitialProtection = "INITIAL_PROTECTION"
	ReasonActiveUpload      = "ACTIVE_UPLOAD"
	ReasonPeerDemand        = "PEER_DEMAND"
	ReasonLowFutureValue    = "LOW_FUTURE_UPLOAD_VALUE"
)

// Rank is a read-only cleanup recommendation. It never calls qB/qui and never
// decides or performs deletion. The caller must re-evaluate after one action.
func Rank(now time.Time, policy Policy, candidates []Candidate) (Result, error) {
	if now.IsZero() {
		return Result{}, errors.New("now is required")
	}
	if policy.MinUploadRateBytesPerSecond < 0 || policy.MinPeerCount < 0 {
		return Result{}, errors.New("deletion policy thresholds must not be negative")
	}
	result := Result{Evaluations: make([]Evaluation, 0, len(candidates))}
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate.ID) == "" || strings.TrimSpace(candidate.InstanceID) == "" || strings.TrimSpace(candidate.QBTorrentID) == "" {
			return Result{}, errors.New("deletion candidate identity is required")
		}
		if candidate.SizeBytes <= 0 || candidate.UploadRateBytesPerSecond < 0 || candidate.PeerCount < 0 {
			return Result{}, errors.New("deletion candidate metrics are invalid")
		}
		evaluation := Evaluation{Candidate: candidate, Eligible: true, Reason: ReasonLowFutureValue, ValuePerByte: float64(candidate.UploadRateBytesPerSecond) / float64(candidate.SizeBytes)}
		switch {
		case !candidate.ProtectedUntil.IsZero() && now.Before(candidate.ProtectedUntil):
			evaluation.Eligible = false
			evaluation.Reason = ReasonInitialProtection
		case candidate.UploadRateBytesPerSecond >= policy.MinUploadRateBytesPerSecond && policy.MinUploadRateBytesPerSecond > 0:
			evaluation.Eligible = false
			evaluation.Reason = ReasonActiveUpload
		case candidate.PeerCount >= policy.MinPeerCount && policy.MinPeerCount > 0:
			evaluation.Eligible = false
			evaluation.Reason = ReasonPeerDemand
		}
		result.Evaluations = append(result.Evaluations, evaluation)
	}
	sort.SliceStable(result.Evaluations, func(left, right int) bool {
		leftValue, rightValue := result.Evaluations[left], result.Evaluations[right]
		if leftValue.Eligible != rightValue.Eligible {
			return leftValue.Eligible
		}
		if leftValue.Eligible {
			comparison := compareRatio(leftValue.Candidate.UploadRateBytesPerSecond, leftValue.Candidate.SizeBytes, rightValue.Candidate.UploadRateBytesPerSecond, rightValue.Candidate.SizeBytes)
			if comparison != 0 {
				return comparison < 0
			}
		}
		return leftValue.Candidate.ID < rightValue.Candidate.ID
	})
	return result, nil
}

func compareRatio(leftNumerator, leftDenominator, rightNumerator, rightDenominator int64) int {
	left := new(big.Int).Mul(big.NewInt(leftNumerator), big.NewInt(rightDenominator))
	right := new(big.Int).Mul(big.NewInt(rightNumerator), big.NewInt(leftDenominator))
	return left.Cmp(right)
}
