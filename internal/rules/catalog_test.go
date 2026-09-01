package rules

import "testing"

func TestCatalogListsDeterministicFieldsAndOperators(t *testing.T) {
	fields := Fields()
	if len(fields) < 10 || fields[0].Name != "site" || fields[0].ValueType != StringValue {
		t.Fatalf("fields=%+v", fields)
	}
	fields[0].Operators[0] = "mutated"
	fresh, ok := LookupField("site")
	if !ok || fresh.Operators[0] != EqualsOperator {
		t.Fatalf("catalog was mutated: %+v", fresh)
	}
	if _, ok := LookupField("unknown"); ok {
		t.Fatal("unknown field unexpectedly found")
	}
}

func TestValidateCatalogAcceptsSupportedCombinations(t *testing.T) {
	text := "race"
	number := 1024.0
	tests := []ConditionTree{
		{Version: CurrentVersion, Root: Node{Kind: LeafKind, Field: "title", Operator: ContainsOperator, Value: &Value{Type: StringValue, String: &text}}},
		{Version: CurrentVersion, Root: Node{Kind: LeafKind, Field: "size", Operator: GreaterThanOperator, Value: &Value{Type: NumberValue, Number: &number}}},
		{Version: CurrentVersion, Root: Node{Kind: LeafKind, Field: "promotion", Operator: EqualsOperator, Value: &Value{Type: BooleanValue, Boolean: boolPointer(true)}}},
		{Version: CurrentVersion, Root: Node{Kind: LeafKind, Field: "tags", Operator: ContainsOperator, Value: &Value{Type: StringValue, String: &text}}},
		{Version: CurrentVersion, Root: Node{Kind: LeafKind, Field: "title", Operator: ExistsOperator}},
	}
	for index, tree := range tests {
		t.Run(string(rune('a'+index)), func(t *testing.T) {
			if err := ValidateCatalog(tree); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestValidateCatalogRejectsUnknownAndInvalidCombinations(t *testing.T) {
	text := "race"
	number := 1024.0
	tests := []ConditionTree{
		{Version: CurrentVersion, Root: Node{Kind: LeafKind, Field: "unknown", Operator: EqualsOperator, Value: &Value{Type: StringValue, String: &text}}},
		{Version: CurrentVersion, Root: Node{Kind: LeafKind, Field: "title", Operator: GreaterThanOperator, Value: &Value{Type: StringValue, String: &text}}},
		{Version: CurrentVersion, Root: Node{Kind: LeafKind, Field: "size", Operator: RegexOperator, Value: &Value{Type: NumberValue, Number: &number}}},
		{Version: CurrentVersion, Root: Node{Kind: LeafKind, Field: "promotion", Operator: ContainsOperator, Value: &Value{Type: BooleanValue, Boolean: boolPointer(true)}}},
		{Version: CurrentVersion, Root: Node{Kind: LeafKind, Field: "title", Operator: EqualsOperator, Value: &Value{Type: NumberValue, Number: &number}}},
	}
	for index, tree := range tests {
		t.Run(string(rune('a'+index)), func(t *testing.T) {
			if err := ValidateCatalog(tree); err == nil {
				t.Fatal("expected catalog validation error")
			}
		})
	}
}
