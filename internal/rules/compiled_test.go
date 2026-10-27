package rules

import (
	"reflect"
	"testing"
)

// Compiled evaluation must be indistinguishable from Evaluate/EvaluatePartial,
// including reason codes and invalid-regex handling.
func TestCompiledMatchesEvaluate(t *testing.T) {
	title := func(op, value string) Node {
		return Node{Kind: LeafKind, Field: "title", Operator: op, Value: &Value{Type: StringValue, String: stringPointer(value)}}
	}
	trees := []ConditionTree{
		{Version: CurrentVersion, Root: title(RegexOperator, `(?i)linux.*1080p`)},
		{Version: CurrentVersion, Root: title(RegexOperator, `(unclosed`)},
		{Version: CurrentVersion, Root: Node{Kind: GroupKind, Operator: OrOperator, Children: []Node{
			title(RegexOperator, `^Nope`),
			{Kind: GroupKind, Operator: AndOperator, Children: []Node{title(ContainsOperator, "Linux"), {Kind: LeafKind, Field: "category", Operator: EqualsOperator, Value: &Value{Type: StringValue, String: stringPointer("TV")}}}},
		}}},
	}
	factSets := []Facts{
		{"title": {Present: true, Value: Value{Type: StringValue, String: stringPointer("Ubuntu Linux 1080p")}}},
		{"title": {Present: true, Value: Value{Type: StringValue, String: stringPointer("Ubuntu Linux 1080p")}}, "category": {Present: true, Value: Value{Type: StringValue, String: stringPointer("TV")}}},
		{},
	}
	for treeIndex, tree := range trees {
		compiled, err := Compile(tree)
		if err != nil {
			t.Fatal(err)
		}
		for factIndex, facts := range factSets {
			want, err := Evaluate(tree, facts)
			if err != nil {
				t.Fatal(err)
			}
			if got := compiled.Evaluate(facts); !reflect.DeepEqual(got, want) {
				t.Fatalf("tree %d facts %d: compiled %+v, evaluate %+v", treeIndex, factIndex, got, want)
			}
			wantPartial, _ := EvaluatePartial(tree, facts)
			if got := compiled.EvaluatePartial(facts); got != wantPartial {
				t.Fatalf("tree %d facts %d partial: compiled %s, evaluate %s", treeIndex, factIndex, got, wantPartial)
			}
		}
	}
	if _, err := Compile(ConditionTree{Version: CurrentVersion, Root: Node{Kind: LeafKind, Field: "nope", Operator: EqualsOperator, Value: &Value{Type: StringValue, String: stringPointer("x")}}}); err == nil {
		t.Fatal("unknown field compiled")
	}
}

func TestUnavailableFieldsListsFieldsWithoutLiveSource(t *testing.T) {
	tree := ConditionTree{Version: CurrentVersion, Root: Node{Kind: GroupKind, Operator: AndOperator, Children: []Node{
		{Kind: LeafKind, Field: "promotion", Operator: EqualsOperator, Value: &Value{Type: BooleanValue, Boolean: boolPointer(true)}},
		{Kind: LeafKind, Field: "category", Operator: EqualsOperator, Value: &Value{Type: StringValue, String: stringPointer("Movies")}},
		{Kind: LeafKind, Field: "tags", Operator: ExistsOperator},
		{Kind: LeafKind, Field: "promotion", Operator: ExistsOperator},
	}}}
	if got := UnavailableFields(tree); !reflect.DeepEqual(got, []string{"promotion", "tags"}) {
		t.Fatalf("unavailable = %v", got)
	}
}
