package placement

import (
	"math"
	"testing"
	"time"
)

func TestDecideRanksRaceReadinessAndReturnsBreakdown(t *testing.T) {
	weights := DecisionWeights{
		InstanceWeight:                   10,
		FreeRatioWeight:                  100,
		InflightPenaltyWeight:            5,
		CurrentDownloadLoadPenaltyWeight: 2,
		RecentAddLatencyPenaltyWeight:    1,
	}
	candidates := []DecisionCandidate{
		availableCandidate("qb-slow", "target-slow"),
		availableCandidate("qb-fast", "target-fast"),
	}
	candidates[0].InstanceWeight = 10
	candidates[0].FreeRatioPPM = 700_000
	candidates[0].CurrentDownloadLoad = 20
	candidates[0].RecentAddLatencyMillis = 80
	candidates[1].InstanceWeight = 8
	candidates[1].FreeRatioPPM = 900_000
	candidates[1].CurrentDownloadLoad = 4
	candidates[1].RecentAddLatencyMillis = 20
	candidates[1].Inflight = 1

	result, err := Decide(DecisionInput{
		IdentityKey:   "sha1:seed-1",
		TargetMode:    TargetInstanceGroup,
		RequiredBytes: 1_000,
		Weights:       weights,
		Candidates:    candidates,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.AlgorithmVersion != DecisionAlgorithmVersion || result.Selected == nil {
		t.Fatalf("result=%+v", result)
	}
	if result.Selected.Candidate.InstanceID != "qb-fast" {
		t.Fatalf("selected=%+v", result.Selected)
	}
	if len(result.Ranked) != 2 || len(result.Rejected) != 0 {
		t.Fatalf("ranked=%+v rejected=%+v", result.Ranked, result.Rejected)
	}
	breakdown := result.Selected.Breakdown
	if breakdown.InstanceTerm != 80 || breakdown.FreeRatioTerm != 90 || breakdown.InflightPenalty != 5 || breakdown.CurrentDownloadLoadPenalty != 8 || breakdown.RecentAddLatencyPenalty != 20 {
		t.Fatalf("breakdown=%+v", breakdown)
	}
	if result.Selected.Score != 137 {
		t.Fatalf("score=%d", result.Selected.Score)
	}
}

func TestDecideRejectsUnsafeCandidates(t *testing.T) {
	base := availableCandidate("qb", "target")
	cases := []struct {
		name   string
		mutate func(*DecisionCandidate)
		reason string
	}{
		{"health", func(candidate *DecisionCandidate) { candidate.CanPlace = false }, "HEALTH_NOT_PLACEABLE"},
		{"profile", func(candidate *DecisionCandidate) { candidate.ProfileState = "EXPECTED" }, "PROFILE_NOT_VERIFIED"},
		{"storage", func(candidate *DecisionCandidate) { candidate.StorageState = "UNVERIFIED" }, "STORAGE_UNVERIFIED"},
		{"capacity", func(candidate *DecisionCandidate) { candidate.CapacityState = "CAPACITY_UNCERTAIN" }, "CAPACITY_UNCERTAIN"},
		{"space", func(candidate *DecisionCandidate) { candidate.EffectiveFreeBytes = 99 }, "INSUFFICIENT_EFFECTIVE_FREE"},
		{"inflight", func(candidate *DecisionCandidate) { candidate.Inflight = candidate.MaxInflight }, "INFLIGHT_LIMIT_REACHED"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			candidate := base
			test.mutate(&candidate)
			result, err := Decide(DecisionInput{
				IdentityKey:   "identity",
				TargetMode:    TargetInstanceGroup,
				RequiredBytes: 100,
				Weights:       testWeights(),
				Candidates:    []DecisionCandidate{candidate},
			})
			if err != nil {
				t.Fatal(err)
			}
			if result.Selected != nil || len(result.Ranked) != 0 || len(result.Rejected) != 1 || result.Rejected[0].Reason != test.reason {
				t.Fatalf("result=%+v", result)
			}
		})
	}
}

func TestDecideFixedTargetNeverFallsBack(t *testing.T) {
	fixed := availableCandidate("qb-fixed", "target-fixed")
	other := availableCandidate("qb-other", "target-other")
	result, err := Decide(DecisionInput{
		IdentityKey:      "identity",
		TargetMode:       TargetFixedInstance,
		TargetInstanceID: "qb-fixed",
		RequiredBytes:    100,
		Weights:          testWeights(),
		Candidates:       []DecisionCandidate{other, fixed},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Selected == nil || result.Selected.Candidate.InstanceID != "qb-fixed" {
		t.Fatalf("selected=%+v", result.Selected)
	}
	if len(result.Rejected) != 1 || result.Rejected[0].Candidate.InstanceID != "qb-other" || result.Rejected[0].Reason != "FIXED_INSTANCE_MISMATCH" {
		t.Fatalf("rejected=%+v", result.Rejected)
	}

	fixed.CapacityState = "CAPACITY_UNCERTAIN"
	result, err = Decide(DecisionInput{
		IdentityKey:      "identity",
		TargetMode:       TargetFixedInstance,
		TargetInstanceID: "qb-fixed",
		RequiredBytes:    100,
		Weights:          testWeights(),
		Candidates:       []DecisionCandidate{other, fixed},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Selected != nil || len(result.Ranked) != 0 {
		t.Fatalf("fixed target unexpectedly fell back: %+v", result)
	}
}

func TestDecideIsIndependentOfCandidateOrder(t *testing.T) {
	firstCandidate := availableCandidate("qb-a", "target-a")
	secondCandidate := availableCandidate("qb-b", "target-b")
	first, err := Decide(DecisionInput{IdentityKey: "identity", TargetMode: TargetInstanceGroup, RequiredBytes: 100, Weights: testWeights(), Candidates: []DecisionCandidate{secondCandidate, firstCandidate}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Decide(DecisionInput{IdentityKey: "identity", TargetMode: TargetInstanceGroup, RequiredBytes: 100, Weights: testWeights(), Candidates: []DecisionCandidate{firstCandidate, secondCandidate}})
	if err != nil {
		t.Fatal(err)
	}
	if first.Selected == nil || second.Selected == nil || first.Selected.Candidate.InstanceID != second.Selected.Candidate.InstanceID {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
	if first.Selected.TieBreak != second.Selected.TieBreak {
		t.Fatalf("tie breaks differ: %d %d", first.Selected.TieBreak, second.Selected.TieBreak)
	}
}

func TestDecideRejectsOverflow(t *testing.T) {
	candidate := availableCandidate("qb", "target")
	candidate.InstanceWeight = math.MaxInt64
	result, err := Decide(DecisionInput{
		IdentityKey:   "identity",
		TargetMode:    TargetInstanceGroup,
		RequiredBytes: 100,
		Weights:       testWeights(),
		Candidates:    []DecisionCandidate{candidate},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Selected != nil || len(result.Rejected) != 1 || result.Rejected[0].Reason != "PLACEMENT_SCORE_OVERFLOW" {
		t.Fatalf("result=%+v", result)
	}
}

func TestPrioritizePutsOfficialItemsFirstWithoutMutatingInput(t *testing.T) {
	now := time.Date(2026, 8, 31, 21, 0, 0, 0, time.UTC)
	items := []QueueItem{
		{IdentityKey: "ordinary-old", DiscoveredAt: now},
		{IdentityKey: "official-new", Official: true, DiscoveredAt: now.Add(time.Minute)},
		{IdentityKey: "official-old", Official: true, DiscoveredAt: now.Add(-time.Minute)},
	}
	ordered, err := Prioritize(items)
	if err != nil {
		t.Fatal(err)
	}
	if ordered[0].IdentityKey != "official-old" || ordered[1].IdentityKey != "official-new" || ordered[2].IdentityKey != "ordinary-old" {
		t.Fatalf("ordered=%+v", ordered)
	}
	if items[0].IdentityKey != "ordinary-old" {
		t.Fatalf("input was mutated: %+v", items)
	}
}

func availableCandidate(instanceID, targetID string) DecisionCandidate {
	return DecisionCandidate{
		InstanceID:             instanceID,
		StorageTargetID:        targetID,
		CanPlace:               true,
		HealthState:            "AVAILABLE",
		ProfileState:           "VERIFIED",
		StorageState:           "VERIFIED",
		CapacityState:          "TRUSTED",
		InstanceWeight:         1,
		FreeRatioPPM:           500_000,
		EffectiveFreeBytes:     10_000,
		MaxInflight:            4,
		CurrentDownloadLoad:    1,
		RecentAddLatencyMillis: 1,
	}
}

func testWeights() DecisionWeights {
	return DecisionWeights{InstanceWeight: 10, FreeRatioWeight: 100, InflightPenaltyWeight: 5, CurrentDownloadLoadPenaltyWeight: 2, RecentAddLatencyPenaltyWeight: 1}
}
