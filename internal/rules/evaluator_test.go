package rules

import "testing"

func TestEvaluateReturnsThreeValuedResultsAndReasonTree(t *testing.T) {
	text := "race"
	tree := ConditionTree{Version: CurrentVersion, Root: Node{Kind: GroupKind, Operator: AndOperator, Children: []Node{{Kind: LeafKind, Field: "title", Operator: ContainsOperator, Value: &Value{Type: StringValue, String: &text}}, {Kind: LeafKind, Field: "category", Operator: EqualsOperator, Value: &Value{Type: StringValue, String: stringPointer("movie")}}}}}
	tests := []struct {
		name  string
		facts Facts
		state State
		code  string
	}{
		{name: "true", facts: Facts{"title": {Present: true, Value: Value{Type: StringValue, String: stringPointer("race movie")}}, "category": {Present: true, Value: Value{Type: StringValue, String: stringPointer("movie")}}}, state: TrueState, code: "AND_TRUE"},
		{name: "false", facts: Facts{"title": {Present: true, Value: Value{Type: StringValue, String: stringPointer("other")}}, "category": {Present: true, Value: Value{Type: StringValue, String: stringPointer("movie")}}}, state: FalseState, code: "AND_FALSE"},
		{name: "unknown", facts: Facts{"title": {Present: true, Value: Value{Type: StringValue, String: stringPointer("race movie")}}}, state: UnknownState, code: "AND_UNKNOWN"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := Evaluate(tree, test.facts)
			if err != nil {
				t.Fatal(err)
			}
			if result.State != test.state || result.Reason.Code != test.code {
				t.Fatalf("result=%s reason=%+v", result, result.Reason)
			}
		})
	}
}

func TestEvaluateShortCircuitsAndOrDeterministically(t *testing.T) {
	missing := Node{Kind: LeafKind, Field: "size", Operator: GreaterThanOperator, Value: &Value{Type: NumberValue, Number: floatPointer(10)}}
	falseLeaf := Node{Kind: LeafKind, Field: "title", Operator: EqualsOperator, Value: &Value{Type: StringValue, String: stringPointer("no")}}
	trueLeaf := Node{Kind: LeafKind, Field: "title", Operator: EqualsOperator, Value: &Value{Type: StringValue, String: stringPointer("yes")}}
	andTree := ConditionTree{Version: CurrentVersion, Root: Node{Kind: GroupKind, Operator: AndOperator, Children: []Node{falseLeaf, missing}}}
	orTree := ConditionTree{Version: CurrentVersion, Root: Node{Kind: GroupKind, Operator: OrOperator, Children: []Node{trueLeaf, missing}}}
	facts := Facts{"title": {Present: true, Value: Value{Type: StringValue, String: stringPointer("yes")}}}
	andResult, err := Evaluate(andTree, facts)
	if err != nil || andResult.State != FalseState || len(andResult.Reason.Children) != 1 {
		t.Fatalf("and=%+v err=%v", andResult, err)
	}
	facts["title"] = Fact{Present: true, Value: Value{Type: StringValue, String: stringPointer("yes")}}
	orResult, err := Evaluate(orTree, facts)
	if err != nil || orResult.State != TrueState || len(orResult.Reason.Children) != 1 {
		t.Fatalf("or=%+v err=%v", orResult, err)
	}
}

func TestEvaluateSupportsTypedOperators(t *testing.T) {
	number := 20.0
	timeValue := "2026-08-25T12:00:00Z"
	tree := ConditionTree{Version: CurrentVersion, Root: Node{Kind: GroupKind, Operator: AndOperator, Children: []Node{
		{Kind: LeafKind, Field: "size", Operator: BetweenOperator, Value: &Value{Type: NumberRangeValue, Numbers: []float64{10, 30}}},
		{Kind: LeafKind, Field: "tags", Operator: ContainsOperator, Value: &Value{Type: StringValue, String: stringPointer("free")}},
		{Kind: LeafKind, Field: "published_at", Operator: LessOrEqualOperator, Value: &Value{Type: TimeValue, Time: &timeValue}},
	}}}
	result, err := Evaluate(tree, Facts{"size": {Present: true, Value: Value{Type: NumberValue, Number: &number}}, "tags": {Present: true, Value: Value{Type: StringSetValue, Strings: []string{"free", "scene"}}}, "published_at": {Present: true, Value: Value{Type: TimeValue, Time: stringPointer("2026-08-25T11:00:00Z")}}})
	if err != nil || result.State != TrueState {
		t.Fatalf("result=%s err=%v", result, err)
	}
}

func TestEvaluateRejectsInvalidTreeAndUnknownRegex(t *testing.T) {
	text := "["
	tree := ConditionTree{Version: CurrentVersion, Root: Node{Kind: LeafKind, Field: "title", Operator: RegexOperator, Value: &Value{Type: StringValue, String: &text}}}
	if _, err := Evaluate(tree, Facts{"title": {Present: true, Value: Value{Type: StringValue, String: stringPointer("x")}}}); err != nil {
		t.Fatal(err)
	}
	result, err := Evaluate(tree, Facts{"title": {Present: true, Value: Value{Type: StringValue, String: stringPointer("x")}}})
	if err != nil || result.State != UnknownState || result.Reason.Code != "INVALID_REGEX" {
		t.Fatalf("result=%s err=%v", result, err)
	}
}

func floatPointer(value float64) *float64 { return &value }
