package addfacts

import (
	"errors"
	"strings"

	"github.com/vertex-pro/vertex-pro/internal/qbresponse"
)

const (
	DispatchNotStarted = "NOT_STARTED"
	DispatchStarted    = "STARTED"

	SideEffectFailedNoEffect = "FAILED_NO_EFFECT"
	SideEffectPossible       = "POSSIBLE_EFFECT"

	AttemptHintAddFailed = "ADD_FAILED"
	AttemptHintAddSent   = "ADD_SENT"
	AttemptHintUnknown   = "UNKNOWN"

	ObservationHintNone           = "NONE"
	ObservationHintObservedEffect = "OBSERVED_EFFECT"
)

type Facts struct {
	DispatchState       string
	SideEffectFact      string
	AttemptStateHint    string
	ObservationHint     string
	QBTorrentIDHint     string
	RequiresSyncConfirm bool
	Authoritative       bool
}

func Classify(result qbresponse.Result) (Facts, error) {
	if strings.TrimSpace(result.APIVersion) == "" {
		return Facts{}, errors.New("api version is required")
	}
	if result.DispatchState == DispatchNotStarted {
		if result.SideEffectFact != SideEffectFailedNoEffect {
			return Facts{}, errors.New("not-started response must be FAILED_NO_EFFECT")
		}
		return Facts{
			DispatchState:    DispatchNotStarted,
			SideEffectFact:   SideEffectFailedNoEffect,
			AttemptStateHint: AttemptHintAddFailed,
			ObservationHint:  ObservationHintNone,
			Authoritative:    false,
		}, nil
	}
	if result.DispatchState != DispatchStarted || result.SideEffectFact != SideEffectPossible {
		return Facts{}, errors.New("response has unsupported dispatch or side-effect fact")
	}

	facts := Facts{
		DispatchState:       DispatchStarted,
		SideEffectFact:      SideEffectPossible,
		AttemptStateHint:    AttemptHintUnknown,
		ObservationHint:     ObservationHintNone,
		RequiresSyncConfirm: true,
		Authoritative:       false,
	}
	if result.ResponseFormat == "legacy_text" || result.ResponseFormat == "json" || result.ResponseFormat == "empty" {
		if result.SuccessCount > 0 || result.PendingCount > 0 {
			facts.AttemptStateHint = AttemptHintAddSent
		}
	}
	if len(result.AddedTorrentIDs) > 0 {
		facts.ObservationHint = ObservationHintObservedEffect
		facts.QBTorrentIDHint = result.AddedTorrentIDs[0]
	}
	return facts, nil
}
