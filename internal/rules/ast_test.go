package rules

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseAcceptsVersionedNestedConditionTree(t *testing.T) {
	text := "race"
	minimum := 1024.0
	tree := ConditionTree{Version: CurrentVersion, Root: Node{Kind: GroupKind, Operator: AndOperator, Children: []Node{{Kind: LeafKind, Field: "title", Operator: "contains", Value: &Value{Type: StringValue, String: &text}}, {Kind: GroupKind, Operator: OrOperator, Children: []Node{{Kind: LeafKind, Field: "size", Operator: "greater_than", Value: &Value{Type: NumberValue, Number: &minimum}}, {Kind: LeafKind, Field: "free", Operator: "equals", Value: &Value{Type: BooleanValue, Boolean: boolPointer(true)}}}}}}}
	data, err := json.Marshal(tree)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Version != CurrentVersion || parsed.Root.Operator != AndOperator || len(parsed.Root.Children) != 2 {
		t.Fatalf("parsed=%+v", parsed)
	}
}

func TestConditionTreeRejectsMalformedAndOversizedInputs(t *testing.T) {
	text := "value"
	valid := ConditionTree{Version: CurrentVersion, Root: Node{Kind: LeafKind, Field: "title", Operator: "contains", Value: &Value{Type: StringValue, String: &text}}}
	tests := []struct {
		name string
		data string
	}{
		{name: "unsupported version", data: `{"version":2,"root":{"kind":"leaf","field":"title","operator":"contains","value":{"type":"string","string":"x"}}}`},
		{name: "empty group", data: `{"version":1,"root":{"kind":"group","operator":"AND","children":[]}}`},
		{name: "missing leaf value", data: `{"version":1,"root":{"kind":"leaf","field":"title","operator":"contains"}}`},
		{name: "mismatched value payload", data: `{"version":1,"root":{"kind":"leaf","field":"size","operator":"greater_than","value":{"type":"number","string":"x"}}}`},
		{name: "unknown field", data: `{"version":1,"root":{"kind":"leaf","field":"title","operator":"contains","value":{"type":"string","string":"x"},"unexpected":true}}`},
		{name: "trailing JSON", data: `{"version":1,"root":{"kind":"leaf","field":"title","operator":"contains","value":{"type":"string","string":"x"}}}{}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Parse([]byte(test.data)); err == nil {
				t.Fatal("expected parse error")
			}
		})
	}
	data, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(append(data, []byte(strings.Repeat("x", MaxJSONBytes))...)); err == nil {
		t.Fatal("expected oversized input error")
	}
}

func TestConditionTreeRejectsDepthAndNodeCountOverflow(t *testing.T) {
	deep := Node{Kind: LeafKind, Field: "title", Operator: "exists", Value: &Value{Type: BooleanValue, Boolean: boolPointer(true)}}
	for index := 0; index < MaxDepth; index++ {
		deep = Node{Kind: GroupKind, Operator: AndOperator, Children: []Node{deep}}
	}
	if err := (ConditionTree{Version: CurrentVersion, Root: deep}).Validate(); err == nil {
		t.Fatal("expected depth error")
	}
	children := make([]Node, 0, MaxChildren)
	for index := 0; index < MaxChildren; index++ {
		children = append(children, Node{Kind: LeafKind, Field: "title", Operator: "exists", Value: &Value{Type: BooleanValue, Boolean: boolPointer(true)}})
	}
	root := Node{Kind: GroupKind, Operator: OrOperator, Children: children}
	root.Children = append(root.Children, Node{Kind: LeafKind, Field: "title", Operator: "exists", Value: &Value{Type: BooleanValue, Boolean: boolPointer(true)}})
	if err := (ConditionTree{Version: CurrentVersion, Root: root}).Validate(); err == nil {
		t.Fatal("expected child count error")
	}
}

func TestConditionTreeRejectsInvalidTypedValues(t *testing.T) {
	longText := strings.Repeat("x", MaxTextBytes+1)
	values := []Value{{Type: StringValue, String: &longText}, {Type: NumberValue}, {Type: BooleanValue, Boolean: boolPointer(true), String: stringPointer("extra")}, {Type: StringSetValue, Strings: []string{""}}, {Type: "date", String: stringPointer("2026-08-25")}}
	for index, value := range values {
		t.Run(string(rune('a'+index)), func(t *testing.T) {
			tree := ConditionTree{Version: CurrentVersion, Root: Node{Kind: LeafKind, Field: "field", Operator: "equals", Value: &value}}
			if err := tree.Validate(); err == nil {
				t.Fatal("expected typed value error")
			}
		})
	}
}

func boolPointer(value bool) *bool       { return &value }
func stringPointer(value string) *string { return &value }
