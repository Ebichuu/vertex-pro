package attemptstate

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/racejob"
)

const (
	StateReserved             = "RESERVED"
	StateAddSent              = "ADD_SENT"
	StateAwaitingConfirmation = "AWAITING_CONFIRMATION"
	StateHashSeen             = "HASH_SEEN"
	StateAlreadyPresent       = "ALREADY_PRESENT"
	StateAddFailed            = "ADD_FAILED"
	StateUnknown              = "UNKNOWN"
	StateReconciledConfirmed  = "RECONCILED_CONFIRMED"
	StateReconciledNoEffect   = "RECONCILED_NO_EFFECT"
	StateExpiredUnknown       = "EXPIRED_UNKNOWN"
	DispatchNotStarted        = "NOT_STARTED"
	DispatchStarted           = "STARTED"
	SideEffectFailedNoEffect  = "FAILED_NO_EFFECT"
	SideEffectPossible        = "POSSIBLE_EFFECT"
	SideEffectObserved        = "OBSERVED_EFFECT"
)

type Attempt struct {
	ID                           string
	JobID                        string
	IdentityKey                  string
	AttemptNo                    int
	InstanceID                   string
	StorageTargetID              string
	AllocationEpoch              int64
	ReservedBytes                int64
	StartedAt                    time.Time
	State                        string
	DispatchState                string
	SideEffectFact               string
	QBTorrentID                  string
	ConfirmationSyncGeneration   *int64
	ConfirmationSnapshotRevision *int64
	ConfirmationEvidenceDigest   string
}

type TransitionInput struct {
	Now                          time.Time
	DispatchState                string
	SideEffectFact               string
	QBTorrentID                  string
	ConfirmationSyncGeneration   *int64
	ConfirmationSnapshotRevision *int64
	ConfirmationEvidenceDigest   string
}

type Event struct {
	AttemptID, From, To string
	OccurredAt          time.Time
}

var allowed = map[string]map[string]struct{}{
	StateReserved:             {StateAddSent: {}, StateAlreadyPresent: {}, StateAddFailed: {}, StateUnknown: {}},
	StateAddSent:              {StateAwaitingConfirmation: {}, StateUnknown: {}, StateAddFailed: {}},
	StateAwaitingConfirmation: {StateHashSeen: {}, StateAlreadyPresent: {}, StateAddFailed: {}, StateUnknown: {}},
	StateHashSeen:             {StateReconciledConfirmed: {}},
	StateAlreadyPresent:       {StateReconciledNoEffect: {}},
	StateUnknown:              {StateReconciledConfirmed: {}, StateReconciledNoEffect: {}, StateExpiredUnknown: {}},
	StateExpiredUnknown:       {StateReconciledConfirmed: {}, StateReconciledNoEffect: {}},
}

func Transition(attempt Attempt, to string, input TransitionInput) (Attempt, Event, error) {
	if err := validateAttempt(attempt); err != nil {
		return Attempt{}, Event{}, err
	}
	if strings.TrimSpace(to) == "" || input.Now.IsZero() {
		return Attempt{}, Event{}, errors.New("target state and transition time are required")
	}
	if _, ok := allowed[attempt.State][to]; !ok {
		return Attempt{}, Event{}, fmt.Errorf("ATTEMPT_TRANSITION_NOT_ALLOWED:%s->%s", attempt.State, to)
	}
	if input.DispatchState == DispatchNotStarted && attempt.DispatchState == DispatchStarted {
		return Attempt{}, Event{}, errors.New("DISPATCH_STATE_DOWNGRADE_NOT_ALLOWED")
	}
	next := attempt
	next.State = to
	if input.DispatchState != "" {
		next.DispatchState = input.DispatchState
	}
	if input.SideEffectFact != "" {
		next.SideEffectFact = input.SideEffectFact
	}
	if input.QBTorrentID != "" {
		next.QBTorrentID = input.QBTorrentID
	}
	if input.ConfirmationSyncGeneration != nil {
		next.ConfirmationSyncGeneration = input.ConfirmationSyncGeneration
	}
	if input.ConfirmationSnapshotRevision != nil {
		next.ConfirmationSnapshotRevision = input.ConfirmationSnapshotRevision
	}
	if input.ConfirmationEvidenceDigest != "" {
		next.ConfirmationEvidenceDigest = input.ConfirmationEvidenceDigest
	}
	if err := validateMutableFacts(next); err != nil {
		return Attempt{}, Event{}, err
	}
	return next, Event{AttemptID: attempt.ID, From: attempt.State, To: to, OccurredAt: input.Now.UTC()}, nil
}

func AttachToJob(job racejob.Job, attempt Attempt) (racejob.Job, error) {
	if attempt.State != StateReserved || attempt.DispatchState != DispatchNotStarted {
		return racejob.Job{}, errors.New("ATTEMPT_NOT_RESERVED_FOR_ATTACH")
	}
	if attempt.JobID != job.ID {
		return racejob.Job{}, errors.New("ATTEMPT_JOB_MISMATCH")
	}
	if job.JobState != racejob.StateReserved {
		return racejob.Job{}, errors.New("JOB_NOT_RESERVED_FOR_ATTACH")
	}
	if job.CurrentAttemptID != nil {
		return racejob.Job{}, errors.New("JOB_ALREADY_HAS_CURRENT_ATTEMPT")
	}
	next := job
	id := attempt.ID
	next.CurrentAttemptID = &id
	return next, nil
}

func validateAttempt(attempt Attempt) error {
	if strings.TrimSpace(attempt.ID) == "" || strings.TrimSpace(attempt.JobID) == "" || strings.TrimSpace(attempt.IdentityKey) == "" || strings.TrimSpace(attempt.InstanceID) == "" || strings.TrimSpace(attempt.StorageTargetID) == "" {
		return errors.New("ATTEMPT_IMMUTABLE_FACT_INCOMPLETE")
	}
	if attempt.AttemptNo < 1 || attempt.AllocationEpoch < 0 || attempt.ReservedBytes < 0 || attempt.StartedAt.IsZero() {
		return errors.New("ATTEMPT_IMMUTABLE_FACT_INVALID")
	}
	return validateMutableFacts(attempt)
}

func validateMutableFacts(attempt Attempt) error {
	if attempt.State == StateReserved {
		if attempt.DispatchState != DispatchNotStarted || attempt.SideEffectFact != "" {
			return errors.New("RESERVED_FACT_INVALID")
		}
		return nil
	}
	if strings.TrimSpace(attempt.SideEffectFact) == "" {
		return errors.New("ACTIVE_ATTEMPT_FACT_INVALID")
	}
	switch attempt.State {
	case StateAddFailed:
		if attempt.DispatchState != DispatchStarted || attempt.SideEffectFact != SideEffectFailedNoEffect {
			return errors.New("ADD_FAILED_FACT_INVALID")
		}
	case StateHashSeen, StateReconciledConfirmed:
		if attempt.DispatchState != DispatchStarted || attempt.SideEffectFact != SideEffectObserved || strings.TrimSpace(attempt.QBTorrentID) == "" || attempt.ConfirmationSyncGeneration == nil || attempt.ConfirmationSnapshotRevision == nil || strings.TrimSpace(attempt.ConfirmationEvidenceDigest) == "" {
			return errors.New("CONFIRMATION_FACT_INCOMPLETE")
		}
	case StateAlreadyPresent:
		if attempt.SideEffectFact != SideEffectObserved || strings.TrimSpace(attempt.QBTorrentID) == "" {
			return errors.New("ALREADY_PRESENT_FACT_INVALID")
		}
		if attempt.DispatchState != DispatchNotStarted && attempt.DispatchState != DispatchStarted {
			return errors.New("ALREADY_PRESENT_FACT_INVALID")
		}
	case StateUnknown, StateAwaitingConfirmation, StateAddSent, StateExpiredUnknown:
		if attempt.DispatchState != DispatchStarted || (attempt.SideEffectFact != SideEffectPossible && attempt.SideEffectFact != SideEffectObserved) {
			return errors.New("POSSIBLE_EFFECT_FACT_REQUIRED")
		}
	case StateReconciledNoEffect:
		if attempt.SideEffectFact != SideEffectPossible && attempt.SideEffectFact != SideEffectObserved {
			return errors.New("POSSIBLE_EFFECT_FACT_REQUIRED")
		}
		// 经由未 dispatch 的 ALREADY_PRESENT 到达时保持 NOT_STARTED。
		if attempt.DispatchState != DispatchStarted && !(attempt.DispatchState == DispatchNotStarted && attempt.SideEffectFact == SideEffectObserved) {
			return errors.New("ACTIVE_ATTEMPT_FACT_INVALID")
		}
	}
	return nil
}
