package attemptstate

import (
	"strings"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/racejob"
)

func TestTransitionRequiresFactsAndPreventsReuse(t *testing.T) {
	now := time.Date(2026, 8, 25, 2, 0, 0, 0, time.UTC)
	attempt := baseAttempt()
	if _, _, err := Transition(attempt, StateAddSent, TransitionInput{Now: now}); err == nil || !strings.Contains(err.Error(), "ACTIVE_ATTEMPT_FACT_INVALID") {
		t.Fatalf("error=%v", err)
	}
	attempt, _, err := Transition(attempt, StateAddSent, TransitionInput{Now: now, DispatchState: DispatchStarted, SideEffectFact: SideEffectPossible})
	if err != nil {
		t.Fatal(err)
	}
	attempt, _, err = Transition(attempt, StateAwaitingConfirmation, TransitionInput{Now: now, SideEffectFact: SideEffectPossible})
	if err != nil {
		t.Fatal(err)
	}
	attempt, _, err = Transition(attempt, StateUnknown, TransitionInput{Now: now, SideEffectFact: SideEffectPossible})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := Transition(attempt, StateReserved, TransitionInput{Now: now}); err == nil {
		t.Fatal("historical attempt reuse error = nil")
	}
}

func TestTransitionRequiresCompleteConfirmation(t *testing.T) {
	now := time.Date(2026, 8, 25, 2, 0, 0, 0, time.UTC)
	attempt := baseAttempt()
	attempt, _, err := Transition(attempt, StateAddSent, TransitionInput{Now: now, DispatchState: DispatchStarted, SideEffectFact: SideEffectPossible})
	if err != nil {
		t.Fatal(err)
	}
	attempt, _, err = Transition(attempt, StateAwaitingConfirmation, TransitionInput{Now: now, SideEffectFact: SideEffectPossible})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := Transition(attempt, StateHashSeen, TransitionInput{Now: now, SideEffectFact: SideEffectObserved, QBTorrentID: "torrent-1"}); err == nil {
		t.Fatal("incomplete confirmation error = nil")
	}
	generation, revision := int64(3), int64(4)
	attempt, _, err = Transition(attempt, StateHashSeen, TransitionInput{Now: now, SideEffectFact: SideEffectObserved, QBTorrentID: "torrent-1", ConfirmationSyncGeneration: &generation, ConfirmationSnapshotRevision: &revision, ConfirmationEvidenceDigest: "digest-1"})
	if err != nil {
		t.Fatal(err)
	}
	if attempt.State != StateHashSeen || attempt.QBTorrentID != "torrent-1" {
		t.Fatalf("attempt=%+v", attempt)
	}
}

func TestAlreadyPresentAllowsUndispatchedAndDispatchedFacts(t *testing.T) {
	now := time.Date(2026, 8, 25, 2, 0, 0, 0, time.UTC)
	attempt := baseAttempt()
	undispatched, event, err := Transition(attempt, StateAlreadyPresent, TransitionInput{Now: now, SideEffectFact: SideEffectObserved, QBTorrentID: "torrent-1"})
	if err != nil {
		t.Fatal(err)
	}
	if undispatched.State != StateAlreadyPresent || undispatched.DispatchState != DispatchNotStarted || undispatched.SideEffectFact != SideEffectObserved {
		t.Fatalf("attempt=%+v", undispatched)
	}
	if event.From != StateReserved || event.To != StateAlreadyPresent {
		t.Fatalf("event=%+v", event)
	}
	if _, _, err := Transition(undispatched, StateReconciledNoEffect, TransitionInput{Now: now, SideEffectFact: SideEffectObserved}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Transition(attempt, StateAlreadyPresent, TransitionInput{Now: now, SideEffectFact: SideEffectPossible, QBTorrentID: "torrent-1"}); err == nil || !strings.Contains(err.Error(), "ALREADY_PRESENT_FACT_INVALID") {
		t.Fatalf("possible-effect error=%v", err)
	}
	dispatched, _, err := Transition(attempt, StateAddSent, TransitionInput{Now: now, DispatchState: DispatchStarted, SideEffectFact: SideEffectPossible})
	if err != nil {
		t.Fatal(err)
	}
	dispatched, _, err = Transition(dispatched, StateAwaitingConfirmation, TransitionInput{Now: now, SideEffectFact: SideEffectPossible})
	if err != nil {
		t.Fatal(err)
	}
	discovered, _, err := Transition(dispatched, StateAlreadyPresent, TransitionInput{Now: now, SideEffectFact: SideEffectObserved, QBTorrentID: "torrent-1"})
	if err != nil {
		t.Fatal(err)
	}
	if discovered.DispatchState != DispatchStarted {
		t.Fatalf("dispatch state=%s", discovered.DispatchState)
	}
}

func TestAttachToJobRequiresReservedCurrentPointerSlot(t *testing.T) {
	job := racejob.Job{ID: "job-1", JobState: racejob.StateReserved}
	attempt := baseAttempt()
	updated, err := AttachToJob(job, attempt)
	if err != nil {
		t.Fatal(err)
	}
	if updated.CurrentAttemptID == nil || *updated.CurrentAttemptID != attempt.ID {
		t.Fatalf("job=%+v", updated)
	}
	job.CurrentAttemptID = updated.CurrentAttemptID
	if _, err := AttachToJob(job, attempt); err == nil || !strings.Contains(err.Error(), "JOB_ALREADY_HAS_CURRENT_ATTEMPT") {
		t.Fatalf("error=%v", err)
	}
	job.CurrentAttemptID = nil
	job.JobState = racejob.StateReady
	if _, err := AttachToJob(job, attempt); err == nil || !strings.Contains(err.Error(), "JOB_NOT_RESERVED_FOR_ATTACH") {
		t.Fatalf("error=%v", err)
	}
}

func baseAttempt() Attempt {
	return Attempt{ID: "attempt-1", JobID: "job-1", IdentityKey: "identity-1", AttemptNo: 1, InstanceID: "qb-1", StorageTargetID: "target-1", AllocationEpoch: 1, ReservedBytes: 100, StartedAt: time.Date(2026, 8, 25, 1, 0, 0, 0, time.UTC), State: StateReserved, DispatchState: DispatchNotStarted}
}
