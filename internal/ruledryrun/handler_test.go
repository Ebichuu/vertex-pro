package ruledryrun

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/ruleaggregate"
	"github.com/vertex-pro/vertex-pro/internal/rules"
)

func TestHandlerRunsDryRunWithoutSideEffects(t *testing.T) {
	text := "race"
	request := Request{Tree: rules.ConditionTree{Version: rules.CurrentVersion, Root: rules.Node{Kind: rules.LeafKind, Field: "title", Operator: rules.ContainsOperator, Value: &rules.Value{Type: rules.StringValue, String: &text}}}, Facts: rules.Facts{"title": {Present: true, Value: rules.Value{Type: rules.StringValue, String: stringPointer("race movie")}}}}
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	httpRequest := httptest.NewRequest(http.MethodPost, "/rules/dry-run", bytes.NewReader(data))
	recorder := httptest.NewRecorder()
	Handler{}.ServeHTTP(recorder, httpRequest)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response Response
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.State != rules.TrueState || response.Reason.Code != "VALUE_MATCH" {
		t.Fatalf("response=%+v", response)
	}
}

func TestHandlerRejectsMalformedAndWrongMethodRequests(t *testing.T) {
	for _, test := range []struct {
		name   string
		method string
		path   string
		body   string
		status int
	}{
		{name: "wrong method", method: http.MethodGet, path: "/rules/dry-run", body: "{}", status: http.StatusMethodNotAllowed},
		{name: "wrong path", method: http.MethodPost, path: "/rules", body: "{}", status: http.StatusMethodNotAllowed},
		{name: "invalid JSON", method: http.MethodPost, path: "/rules/dry-run", body: "{}{}", status: http.StatusBadRequest},
		{name: "invalid trailing JSON", method: http.MethodPost, path: "/rules/dry-run", body: "{} {", status: http.StatusBadRequest},
		{name: "unknown JSON field", method: http.MethodPost, path: "/rules/dry-run", body: `{"unexpected":true}`, status: http.StatusBadRequest},
		{name: "empty rule id", method: http.MethodPost, path: "/rules//dry-run", body: "{}", status: http.StatusMethodNotAllowed},
		{name: "nested rule path", method: http.MethodPost, path: "/rules/a/b/dry-run", body: "{}", status: http.StatusMethodNotAllowed},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			Handler{}.ServeHTTP(recorder, httptest.NewRequest(test.method, test.path, bytes.NewBufferString(test.body)))
			if recorder.Code != test.status {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestHandlerSupportsSavedRuleDryRunPathAndAggregation(t *testing.T) {
	text := "race"
	request := Request{Tree: rules.ConditionTree{Version: rules.CurrentVersion, Root: rules.Node{Kind: rules.LeafKind, Field: "title", Operator: rules.ContainsOperator, Value: &rules.Value{Type: rules.StringValue, String: &text}}}, Facts: rules.Facts{"title": {Present: true, Value: rules.Value{Type: rules.StringValue, String: stringPointer("race")}}}, Matches: []ruleaggregate.Match{{RuleID: "rule-a", RuleRevision: 1, Priority: 10, TargetMode: "FIXED_INSTANCE", TargetInstanceID: "qb-a", Tags: []string{"tag-a"}, ParticipationWindow: time.Hour}}}
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	Handler{}.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/rules/rule-a/dry-run", bytes.NewReader(data)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response Response
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Aggregation == nil || response.Aggregation.Winner.RuleID != "rule-a" {
		t.Fatalf("response=%+v", response)
	}
}

func stringPointer(value string) *string { return &value }
