package racegate

import "testing"

func TestEvaluateCoversPreparationPermutations(t *testing.T) {
	tests := []struct {
		name        string
		input       Input
		jobState    string
		reason      string
		reservation bool
	}{
		{name: "all prepared and final available", input: Input{RuleState: "MATCHED", TorrentState: "READY", PlacementState: "PREPARED", FinalCandidate: FinalCandidateAvailable, CurrentJobState: "READY"}, jobState: "READY", reason: "AVAILABLE", reservation: true},
		{name: "all prepared but final unknown", input: Input{RuleState: "MATCHED", TorrentState: "READY", PlacementState: "PREPARED", FinalCandidate: FinalCandidateUnknown, CurrentJobState: "READY"}, jobState: "READY", reason: "FINAL_RECHECK_REQUIRED"},
		{name: "all prepared but capacity unavailable", input: Input{RuleState: "MATCHED", TorrentState: "READY", PlacementState: "PREPARED", FinalCandidate: FinalCandidateUnavailable, CurrentJobState: "READY"}, jobState: "READY", reason: "FINAL_RECHECK_REQUIRED"},
		{name: "placement waiting capacity", input: Input{RuleState: "MATCHED", TorrentState: "READY", PlacementState: "WAITING_CAPACITY", FinalCandidate: FinalCandidateAvailable, CurrentJobState: "WAITING_CAPACITY"}, jobState: "WAITING_CAPACITY", reason: "WAITING_CAPACITY"},
		{name: "rule rejected", input: Input{RuleState: "REJECTED", TorrentState: "READY", PlacementState: "PREPARED", FinalCandidate: FinalCandidateAvailable, CurrentJobState: "PREPARING"}, jobState: "PREPARE_FAILED", reason: "RULE_REJECTED"},
		{name: "torrent failed", input: Input{RuleState: "MATCHED", TorrentState: "FAILED", PlacementState: "PREPARED", FinalCandidate: FinalCandidateAvailable, CurrentJobState: "PREPARING"}, jobState: "PREPARE_FAILED", reason: "TORRENT_FAILED"},
		{name: "rule incomplete", input: Input{RuleState: "RUNNING", TorrentState: "READY", PlacementState: "PREPARED", FinalCandidate: FinalCandidateAvailable, CurrentJobState: "PREPARING"}, jobState: "PREPARING", reason: "PREPARATION_INCOMPLETE"},
		{name: "torrent incomplete", input: Input{RuleState: "MATCHED", TorrentState: "FETCHING", PlacementState: "PREPARED", FinalCandidate: FinalCandidateAvailable, CurrentJobState: "PREPARING"}, jobState: "PREPARING", reason: "PREPARATION_INCOMPLETE"},
		{name: "placement incomplete", input: Input{RuleState: "MATCHED", TorrentState: "READY", PlacementState: "PENDING", FinalCandidate: FinalCandidateAvailable, CurrentJobState: "PREPARING"}, jobState: "PREPARING", reason: "PREPARATION_INCOMPLETE"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decision, err := Evaluate(test.input)
			if err != nil {
				t.Fatal(err)
			}
			if decision.JobState != test.jobState || decision.ReasonCode != test.reason || decision.ReservationEligible != test.reservation {
				t.Fatalf("decision=%+v", decision)
			}
		})
	}
}

func TestEvaluateRequiresCurrentReadyJobAndNoAttempt(t *testing.T) {
	base := Input{RuleState: "MATCHED", TorrentState: "READY", PlacementState: "PREPARED", FinalCandidate: FinalCandidateAvailable}
	for _, test := range []struct {
		name   string
		input  Input
		reason string
	}{
		{name: "job is still preparing", input: Input{RuleState: base.RuleState, TorrentState: base.TorrentState, PlacementState: base.PlacementState, FinalCandidate: base.FinalCandidate, CurrentJobState: "PREPARING"}, reason: "JOB_NOT_READY"},
		{name: "current attempt exists", input: Input{RuleState: base.RuleState, TorrentState: base.TorrentState, PlacementState: base.PlacementState, FinalCandidate: base.FinalCandidate, CurrentJobState: "READY", CurrentAttemptID: "attempt-1"}, reason: "CURRENT_ATTEMPT_EXISTS"},
	} {
		t.Run(test.name, func(t *testing.T) {
			decision, err := Evaluate(test.input)
			if err != nil {
				t.Fatal(err)
			}
			if decision.JobState != "READY" || decision.ReasonCode != test.reason || decision.ReservationEligible {
				t.Fatalf("decision=%+v", decision)
			}
		})
	}
}

func TestEvaluateRejectsInvalidStates(t *testing.T) {
	base := Input{RuleState: "MATCHED", TorrentState: "READY", PlacementState: "PREPARED", FinalCandidate: FinalCandidateAvailable, CurrentJobState: "READY"}
	tests := []Input{
		{RuleState: "BROKEN", TorrentState: base.TorrentState, PlacementState: base.PlacementState, FinalCandidate: base.FinalCandidate, CurrentJobState: base.CurrentJobState},
		{RuleState: base.RuleState, TorrentState: "BROKEN", PlacementState: base.PlacementState, FinalCandidate: base.FinalCandidate, CurrentJobState: base.CurrentJobState},
		{RuleState: base.RuleState, TorrentState: base.TorrentState, PlacementState: "BROKEN", FinalCandidate: base.FinalCandidate, CurrentJobState: base.CurrentJobState},
		{RuleState: base.RuleState, TorrentState: base.TorrentState, PlacementState: base.PlacementState, FinalCandidate: "BROKEN", CurrentJobState: base.CurrentJobState},
	}
	for index, input := range tests {
		t.Run(string(rune('a'+index)), func(t *testing.T) {
			if _, err := Evaluate(input); err == nil {
				t.Fatal("expected invalid state error")
			}
		})
	}
}
