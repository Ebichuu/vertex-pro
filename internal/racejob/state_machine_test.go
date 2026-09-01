package racejob

import (
	"strings"
	"testing"
	"time"
)

func TestTransitionCoversPreparationAndReadyGuards(t *testing.T) {
	now := time.Date(2026, 8, 25, 1, 0, 0, 0, time.UTC)
	job := Job{ID: "job-1", JobState: StateDiscovered, RuleState: "PENDING", TorrentState: "PENDING", PlacementState: "PENDING", PreparationRevision: 0}
	job, event, err := Transition(job, StatePreparing, TransitionInput{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if job.JobState != StatePreparing || job.PreparationRevision != 1 || event.From != StateDiscovered || event.To != StatePreparing {
		t.Fatalf("job=%+v event=%+v", job, event)
	}
	job.RuleState, job.TorrentState, job.PlacementState = "MATCHED", "READY", "PREPARED"
	if _, _, err := Transition(job, StateReady, TransitionInput{Now: now}); err == nil {
		t.Fatal("missing capacity guard error = nil")
	}
	job, _, err = Transition(job, StateReady, TransitionInput{Now: now, CapacityAvailable: true})
	if err != nil {
		t.Fatal(err)
	}
	if job.JobState != StateReady || job.CurrentAttemptID != nil {
		t.Fatalf("job=%+v", job)
	}
}

func TestTransitionEnforcesAttemptPointerInvariant(t *testing.T) {
	now := time.Date(2026, 8, 25, 1, 0, 0, 0, time.UTC)
	job := Job{ID: "job-1", JobState: StateReady, RuleState: "MATCHED", TorrentState: "READY", PlacementState: "PREPARED"}
	if _, _, err := Transition(job, StateReserved, TransitionInput{Now: now}); err == nil || !strings.Contains(err.Error(), "CURRENT_ATTEMPT_REQUIRED") {
		t.Fatalf("error=%v", err)
	}
	job, _, err := Transition(job, StateReserved, TransitionInput{Now: now, CurrentAttemptID: "attempt-1"})
	if err != nil {
		t.Fatal(err)
	}
	if job.CurrentAttemptID == nil || *job.CurrentAttemptID != "attempt-1" {
		t.Fatalf("job=%+v", job)
	}
	if _, _, err := Transition(job, StateAddSent, TransitionInput{Now: now}); err == nil || !strings.Contains(err.Error(), "CURRENT_ATTEMPT_REQUIRED") {
		t.Fatalf("error=%v", err)
	}
	job, _, err = Transition(job, StateAddFailed, TransitionInput{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if job.CurrentAttemptID != nil {
		t.Fatalf("job=%+v", job)
	}
}

func TestTransitionRejectsIllegalAndTerminalReuse(t *testing.T) {
	now := time.Date(2026, 8, 25, 1, 0, 0, 0, time.UTC)
	job := Job{ID: "job-1", JobState: StateReconciledNoEffect}
	if _, _, err := Transition(job, StateReserved, TransitionInput{Now: now, CurrentAttemptID: "attempt-2"}); err == nil {
		t.Fatal("terminal reuse error = nil")
	}
	job.JobState = StateReady
	if _, _, err := Transition(job, StateReady, TransitionInput{Now: now, CapacityAvailable: true}); err == nil {
		t.Fatal("same-state error = nil")
	}
}

func TestTransitionRequeuesClosedChainsToReady(t *testing.T) {
	now := time.Date(2026, 8, 25, 1, 0, 0, 0, time.UTC)
	for _, from := range []string{StateAddFailed, StateReconciledNoEffect} {
		job := Job{ID: "job-1", JobState: from, RuleState: "MATCHED", TorrentState: "READY", PlacementState: "PREPARED"}
		next, event, err := Transition(job, StateReady, TransitionInput{Now: now, CapacityAvailable: true})
		if err != nil {
			t.Fatal(err)
		}
		if next.JobState != StateReady || next.CurrentAttemptID != nil || event.From != from || event.To != StateReady {
			t.Fatalf("from %s: job=%+v event=%+v", from, next, event)
		}
		next, _, err = Transition(job, StateExpired, TransitionInput{Now: now})
		if err != nil || next.JobState != StateExpired {
			t.Fatalf("from %s: job=%+v err=%v", from, next, err)
		}
	}
}
