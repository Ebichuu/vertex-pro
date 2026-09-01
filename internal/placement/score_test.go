package placement

import "testing"

func TestRankUsesWeightedScoreAndStableTieBreak(t *testing.T) {
	weights := Weights{InstanceWeight: 100, FreeBytesWeight: 2, InflightPenaltyWeight: 10}
	candidates := []Candidate{{InstanceID: "qb-b", StorageTargetID: "target-b", Eligible: true, Weight: 5, EffectiveFreeBytes: 100, Inflight: 1, MaxInflight: 4}, {InstanceID: "qb-a", StorageTargetID: "target-a", Eligible: true, Weight: 5, EffectiveFreeBytes: 100, Inflight: 1, MaxInflight: 4}, {InstanceID: "qb-c", StorageTargetID: "target-c", Eligible: true, Weight: 4, EffectiveFreeBytes: 400, Inflight: 0, MaxInflight: 4}}
	result, err := Rank(candidates, weights)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Ranked) != 3 || result.Ranked[0].Candidate.InstanceID != "qb-c" || result.Ranked[1].Candidate.InstanceID != "qb-a" || result.Ranked[2].Candidate.InstanceID != "qb-b" {
		t.Fatalf("ranked=%+v", result.Ranked)
	}
	if result.Ranked[0].Score != 1200 {
		t.Fatalf("score=%d", result.Ranked[0].Score)
	}
}

func TestRankIsIndependentOfInputOrder(t *testing.T) {
	weights := Weights{InstanceWeight: 10, FreeBytesWeight: 1, InflightPenaltyWeight: 5}
	first, err := Rank([]Candidate{{InstanceID: "b", StorageTargetID: "tb", Eligible: true, Weight: 1, EffectiveFreeBytes: 10, MaxInflight: 2}, {InstanceID: "a", StorageTargetID: "ta", Eligible: true, Weight: 1, EffectiveFreeBytes: 10, MaxInflight: 2}}, weights)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Rank([]Candidate{{InstanceID: "a", StorageTargetID: "ta", Eligible: true, Weight: 1, EffectiveFreeBytes: 10, MaxInflight: 2}, {InstanceID: "b", StorageTargetID: "tb", Eligible: true, Weight: 1, EffectiveFreeBytes: 10, MaxInflight: 2}}, weights)
	if err != nil {
		t.Fatal(err)
	}
	if first.Ranked[0].Candidate.InstanceID != second.Ranked[0].Candidate.InstanceID || first.Ranked[1].Candidate.InstanceID != second.Ranked[1].Candidate.InstanceID {
		t.Fatalf("first=%+v second=%+v", first.Ranked, second.Ranked)
	}
}

func TestRankRejectsIneligibleInvalidAndOverflowCandidates(t *testing.T) {
	weights := Weights{InstanceWeight: 2, FreeBytesWeight: 2, InflightPenaltyWeight: 1}
	result, err := Rank([]Candidate{{InstanceID: "bad", StorageTargetID: "target", Eligible: false, MaxInflight: 1}, {InstanceID: "negative", StorageTargetID: "target", Eligible: true, Weight: -1, MaxInflight: 1}, {InstanceID: "overflow", StorageTargetID: "target", Eligible: true, Weight: 1, EffectiveFreeBytes: 1 << 62, MaxInflight: 1}}, weights)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Ranked) != 0 || len(result.Rejected) != 3 {
		t.Fatalf("result=%+v", result)
	}
	if result.Rejected[0].Reason == "" || result.Rejected[1].Reason == "" || result.Rejected[2].Reason == "" {
		t.Fatalf("rejected=%+v", result.Rejected)
	}
}

func TestRankRejectsInvalidWeights(t *testing.T) {
	if _, err := Rank(nil, Weights{}); err == nil {
		t.Fatal("invalid weights error = nil")
	}
}
