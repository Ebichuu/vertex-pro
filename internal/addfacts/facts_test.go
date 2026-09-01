package addfacts

import (
	"testing"

	"github.com/vertex-pro/vertex-pro/internal/qbresponse"
)

func TestClassifyNotSentAsNoEffect(t *testing.T) {
	facts, err := Classify(qbresponse.Result{APIVersion: "2.15.1", DispatchState: "NOT_STARTED", SideEffectFact: "FAILED_NO_EFFECT"})
	if err != nil {
		t.Fatal(err)
	}
	if facts.AttemptStateHint != AttemptHintAddFailed || facts.RequiresSyncConfirm || facts.Authoritative {
		t.Fatalf("facts = %+v", facts)
	}
}

func TestClassifyStartedResponseRequiresSync(t *testing.T) {
	cases := []struct {
		name        string
		result      qbresponse.Result
		stateHint   string
		observation string
		idHint      string
	}{
		{
			name:      "transport unknown",
			result:    qbresponse.Result{APIVersion: "2.15.1", DispatchState: "STARTED", SideEffectFact: "POSSIBLE_EFFECT", ResponseFormat: "transport_error"},
			stateHint: AttemptHintUnknown, observation: ObservationHintNone,
		},
		{
			name:      "json pending",
			result:    qbresponse.Result{APIVersion: "2.15.1", DispatchState: "STARTED", SideEffectFact: "POSSIBLE_EFFECT", ResponseFormat: "json", PendingCount: 1},
			stateHint: AttemptHintAddSent, observation: ObservationHintNone,
		},
		{
			name:      "id hint",
			result:    qbresponse.Result{APIVersion: "2.15.1", DispatchState: "STARTED", SideEffectFact: "POSSIBLE_EFFECT", ResponseFormat: "json", SuccessCount: 1, AddedTorrentIDs: []string{"hash-hint"}},
			stateHint: AttemptHintAddSent, observation: ObservationHintObservedEffect, idHint: "hash-hint",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			facts, err := Classify(testCase.result)
			if err != nil {
				t.Fatal(err)
			}
			if facts.AttemptStateHint != testCase.stateHint || facts.ObservationHint != testCase.observation || facts.QBTorrentIDHint != testCase.idHint || !facts.RequiresSyncConfirm || facts.Authoritative {
				t.Fatalf("facts = %+v", facts)
			}
		})
	}
}

func TestClassifyRejectsInvalidFacts(t *testing.T) {
	cases := []qbresponse.Result{
		{},
		{APIVersion: "2.15.1", DispatchState: "NOT_STARTED", SideEffectFact: "POSSIBLE_EFFECT"},
		{APIVersion: "2.15.1", DispatchState: "STARTED", SideEffectFact: "FAILED_NO_EFFECT"},
	}
	for _, result := range cases {
		if _, err := Classify(result); err == nil {
			t.Fatalf("Classify(%+v) error = nil", result)
		}
	}

}
