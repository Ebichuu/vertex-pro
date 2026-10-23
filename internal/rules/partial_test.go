package rules

import "testing"

func TestPartialEvaluationNeverRejectsUnresolvedLeaves(t *testing.T) {
	facts := Facts{"title": {Present: true, Value: Value{Type: StringValue, String: stringPointer("yes")}}}
	missing := Node{Kind: LeafKind, Field: "size", Operator: ExistsOperator}
	no := Node{Kind: LeafKind, Field: "title", Operator: EqualsOperator, Value: &Value{Type: StringValue, String: stringPointer("no")}}
	yes := no
	yes.Value = &Value{Type: StringValue, String: stringPointer("yes")}
	for _, tc := range []struct {
		name string
		node Node
		want State
	}{
		{"missing exists", missing, UnknownState},
		{"missing not exists", Node{Kind: LeafKind, Field: "size", Operator: NotExistsOperator}, UnknownState},
		{"and false", Node{Kind: GroupKind, Operator: AndOperator, Children: []Node{missing, no}}, FalseState},
		{"or unresolved", Node{Kind: GroupKind, Operator: OrOperator, Children: []Node{no, missing}}, UnknownState},
		{"or true", Node{Kind: GroupKind, Operator: OrOperator, Children: []Node{yes, missing}}, TrueState},
		{"nested", Node{Kind: GroupKind, Operator: AndOperator, Children: []Node{yes, {Kind: GroupKind, Operator: OrOperator, Children: []Node{no, missing}}}}, UnknownState},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state, err := EvaluatePartial(ConditionTree{Version: 1, Root: tc.node}, facts)
			if err != nil || state != tc.want {
				t.Fatalf("%s %v", state, err)
			}
		})
	}
}
