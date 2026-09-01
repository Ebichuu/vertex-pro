package racejob

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	StateDiscovered           = "DISCOVERED"
	StatePreparing            = "PREPARING"
	StateReady                = "READY"
	StateWaitingCapacity      = "WAITING_CAPACITY"
	StatePrepareFailed        = "PREPARE_FAILED"
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
	StateExpired              = "EXPIRED"
)

type TransitionInput struct {
	CurrentAttemptID  string
	Now               time.Time
	CapacityAvailable bool
}

type TransitionEvent struct {
	JobID      string
	From       string
	To         string
	Revision   int
	OccurredAt time.Time
}

var allowedJobTransitions = map[string]map[string]struct{}{
	StateDiscovered:           {StatePreparing: {}},
	StatePreparing:            {StateReady: {}, StateWaitingCapacity: {}, StatePrepareFailed: {}, StateExpired: {}},
	StateWaitingCapacity:      {StateReady: {}, StatePrepareFailed: {}, StateExpired: {}},
	StateReady:                {StateReserved: {}, StateExpired: {}},
	StateReserved:             {StateAddSent: {}, StateAddFailed: {}, StateUnknown: {}, StateHashSeen: {}, StateAlreadyPresent: {}},
	StateAddSent:              {StateAwaitingConfirmation: {}, StateUnknown: {}, StateAddFailed: {}},
	StateAwaitingConfirmation: {StateHashSeen: {}, StateAlreadyPresent: {}, StateUnknown: {}, StateAddFailed: {}},
	StateHashSeen:             {StateReconciledConfirmed: {}, StateExpiredUnknown: {}},
	StateAlreadyPresent:       {StateReconciledConfirmed: {}, StateExpired: {}},
	StateAddFailed:            {StateReady: {}, StateExpired: {}},
	StateUnknown:              {StateReconciledConfirmed: {}, StateReconciledNoEffect: {}, StateExpiredUnknown: {}},
	StateReconciledConfirmed:  {StateExpired: {}},
	StateReconciledNoEffect:   {StateReady: {}, StateExpired: {}},
	StateExpiredUnknown:       {StateReconciledConfirmed: {}, StateReconciledNoEffect: {}, StateExpired: {}},
}

func Transition(job Job, to string, input TransitionInput) (Job, TransitionEvent, error) {
	if strings.TrimSpace(job.ID) == "" {
		return Job{}, TransitionEvent{}, errors.New("job ID is required")
	}
	if strings.TrimSpace(job.JobState) == "" || strings.TrimSpace(to) == "" {
		return Job{}, TransitionEvent{}, errors.New("job state and target state are required")
	}
	if input.Now.IsZero() {
		return Job{}, TransitionEvent{}, errors.New("transition time is required")
	}
	if job.JobState == to {
		return Job{}, TransitionEvent{}, errors.New("same-state transition is not allowed")
	}
	if _, ok := allowedJobTransitions[job.JobState][to]; !ok {
		return Job{}, TransitionEvent{}, fmt.Errorf("JOB_TRANSITION_NOT_ALLOWED:%s->%s", job.JobState, to)
	}
	if to == StateReady && (job.RuleState != "MATCHED" || job.TorrentState != "READY" || job.PlacementState != "PREPARED" || !input.CapacityAvailable) {
		return Job{}, TransitionEvent{}, errors.New("READY_GUARD_FAILED")
	}
	if to == StateWaitingCapacity && job.RuleState != "MATCHED" {
		return Job{}, TransitionEvent{}, errors.New("WAITING_CAPACITY_RULE_GUARD_FAILED")
	}
	if requiresAttemptPointer(to) && strings.TrimSpace(input.CurrentAttemptID) == "" {
		return Job{}, TransitionEvent{}, errors.New("CURRENT_ATTEMPT_REQUIRED")
	}
	if !requiresAttemptPointer(to) && strings.TrimSpace(input.CurrentAttemptID) != "" {
		return Job{}, TransitionEvent{}, errors.New("CURRENT_ATTEMPT_FORBIDDEN")
	}
	next := job
	next.JobState = to
	next.CurrentAttemptID = nil
	if requiresAttemptPointer(to) {
		attemptID := strings.TrimSpace(input.CurrentAttemptID)
		next.CurrentAttemptID = &attemptID
	}
	next.PreparationRevision++
	next.UpdatedAt = input.Now.UTC()
	event := TransitionEvent{JobID: job.ID, From: job.JobState, To: to, Revision: next.PreparationRevision, OccurredAt: next.UpdatedAt}
	return next, event, nil
}

func requiresAttemptPointer(state string) bool {
	switch state {
	case StateReserved, StateAddSent, StateAwaitingConfirmation, StateUnknown, StateHashSeen, StateReconciledConfirmed, StateExpiredUnknown:
		return true
	default:
		return false
	}
}
