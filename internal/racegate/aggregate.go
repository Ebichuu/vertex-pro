package racegate

import "errors"

type FinalCandidateState string

const (
	FinalCandidateUnknown     FinalCandidateState = "UNKNOWN"
	FinalCandidateAvailable   FinalCandidateState = "AVAILABLE"
	FinalCandidateUnavailable FinalCandidateState = "UNAVAILABLE"
)

type Input struct {
	RuleState        string
	TorrentState     string
	PlacementState   string
	FinalCandidate   FinalCandidateState
	CurrentJobState  string
	CurrentAttemptID string
}

type Decision struct {
	JobState            string
	ReasonCode          string
	ReservationEligible bool
}

func Evaluate(input Input) (Decision, error) {
	if err := validate(input); err != nil {
		return Decision{}, err
	}
	if input.RuleState == "REJECTED" {
		return Decision{JobState: "PREPARE_FAILED", ReasonCode: "RULE_REJECTED"}, nil
	}
	if input.TorrentState == "FAILED" {
		return Decision{JobState: "PREPARE_FAILED", ReasonCode: "TORRENT_FAILED"}, nil
	}
	if input.PlacementState == "WAITING_CAPACITY" {
		return Decision{JobState: "WAITING_CAPACITY", ReasonCode: "WAITING_CAPACITY"}, nil
	}
	if input.RuleState != "MATCHED" || input.TorrentState != "READY" || input.PlacementState != "PREPARED" {
		return Decision{JobState: "PREPARING", ReasonCode: "PREPARATION_INCOMPLETE"}, nil
	}
	decision := Decision{JobState: "READY", ReasonCode: "FINAL_RECHECK_REQUIRED"}
	if input.FinalCandidate != FinalCandidateAvailable {
		return decision, nil
	}
	if input.CurrentJobState != "READY" {
		decision.ReasonCode = "JOB_NOT_READY"
		return decision, nil
	}
	if input.CurrentAttemptID != "" {
		decision.ReasonCode = "CURRENT_ATTEMPT_EXISTS"
		return decision, nil
	}
	decision.ReasonCode = "AVAILABLE"
	decision.ReservationEligible = true
	return decision, nil
}

func validate(input Input) error {
	if !validRuleState(input.RuleState) {
		return errors.New("invalid rule state")
	}
	if !validTorrentState(input.TorrentState) {
		return errors.New("invalid torrent state")
	}
	if !validPlacementState(input.PlacementState) {
		return errors.New("invalid placement state")
	}
	if input.FinalCandidate != FinalCandidateUnknown && input.FinalCandidate != FinalCandidateAvailable && input.FinalCandidate != FinalCandidateUnavailable {
		return errors.New("invalid final candidate state")
	}
	if input.CurrentJobState != "" && input.CurrentJobState != "PREPARING" && input.CurrentJobState != "READY" && input.CurrentJobState != "WAITING_CAPACITY" && input.CurrentJobState != "PREPARE_FAILED" {
		return errors.New("invalid current job state")
	}
	return nil
}

func validRuleState(state string) bool {
	switch state {
	case "PENDING", "RUNNING", "MATCHED", "REJECTED":
		return true
	default:
		return false
	}
}

func validTorrentState(state string) bool {
	switch state {
	case "PENDING", "FETCHING", "READY", "FAILED":
		return true
	default:
		return false
	}
}

func validPlacementState(state string) bool {
	switch state {
	case "PENDING", "PREPARED", "WAITING_CAPACITY":
		return true
	default:
		return false
	}
}
