package deletionvalue

import (
	"testing"
	"time"
)

func TestRankFiltersProtectedActiveAndDemand(t *testing.T) {
	now := time.Date(2026, 8, 31, 22, 0, 0, 0, time.UTC)
	result, err := Rank(now, Policy{MinUploadRateBytesPerSecond: 100, MinPeerCount: 1}, []Candidate{
		{ID: "protected", InstanceID: "qb-a", QBTorrentID: "hash-protected", SizeBytes: 1000, ProtectedUntil: now.Add(time.Hour)},
		{ID: "active", InstanceID: "qb-a", QBTorrentID: "hash-active", SizeBytes: 1000, UploadRateBytesPerSecond: 100},
		{ID: "demand", InstanceID: "qb-a", QBTorrentID: "hash-demand", SizeBytes: 1000, PeerCount: 1},
		{ID: "review", InstanceID: "qb-a", QBTorrentID: "hash-review", SizeBytes: 1000, UploadRateBytesPerSecond: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Evaluations) != 4 {
		t.Fatalf("evaluations=%d", len(result.Evaluations))
	}
	wantReasons := map[string]string{
		"protected": ReasonInitialProtection,
		"active":    ReasonActiveUpload,
		"demand":    ReasonPeerDemand,
		"review":    ReasonLowFutureValue,
	}
	for _, evaluation := range result.Evaluations {
		if evaluation.Reason != wantReasons[evaluation.Candidate.ID] {
			t.Fatalf("%s reason=%s", evaluation.Candidate.ID, evaluation.Reason)
		}
		if evaluation.Candidate.ID == "review" && !evaluation.Eligible {
			t.Fatal("low-value candidate should be eligible for review")
		}
	}
	if result.Evaluations[0].Candidate.ID != "review" {
		t.Fatalf("first evaluation=%s, want review candidate", result.Evaluations[0].Candidate.ID)
	}
}

func TestRankOrdersEligibleCandidatesByFutureValuePerByte(t *testing.T) {
	now := time.Date(2026, 8, 31, 22, 0, 0, 0, time.UTC)
	result, err := Rank(now, Policy{}, []Candidate{
		{ID: "higher-ratio", InstanceID: "qb-a", QBTorrentID: "hash-high", SizeBytes: 1000, UploadRateBytesPerSecond: 20},
		{ID: "lower-ratio", InstanceID: "qb-a", QBTorrentID: "hash-low", SizeBytes: 1000, UploadRateBytesPerSecond: 1},
		{ID: "same-ratio-b", InstanceID: "qb-a", QBTorrentID: "hash-b", SizeBytes: 2000, UploadRateBytesPerSecond: 2},
		{ID: "same-ratio-a", InstanceID: "qb-a", QBTorrentID: "hash-a", SizeBytes: 2000, UploadRateBytesPerSecond: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"lower-ratio", "same-ratio-a", "same-ratio-b", "higher-ratio"}
	for index, id := range want {
		if result.Evaluations[index].Candidate.ID != id {
			t.Fatalf("evaluation[%d]=%s, want %s", index, result.Evaluations[index].Candidate.ID, id)
		}
	}
}

func TestRankDoesNotProtectAtExactBoundary(t *testing.T) {
	now := time.Date(2026, 8, 31, 22, 0, 0, 0, time.UTC)
	result, err := Rank(now, Policy{}, []Candidate{{ID: "boundary", InstanceID: "qb-a", QBTorrentID: "hash-boundary", SizeBytes: 100, ProtectedUntil: now}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Evaluations[0].Eligible || result.Evaluations[0].Reason != ReasonLowFutureValue {
		t.Fatalf("boundary evaluation=%+v", result.Evaluations[0])
	}
}

func TestRankRejectsInvalidCandidate(t *testing.T) {
	_, err := Rank(time.Now(), Policy{}, []Candidate{{ID: "bad", InstanceID: "qb-a", QBTorrentID: "hash", SizeBytes: 0}})
	if err == nil {
		t.Fatal("invalid candidate error=nil")
	}
}
