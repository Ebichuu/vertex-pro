package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestModelGenerationUsesRequiredReferencesAndArrayItems(t *testing.T) {
	var document map[string]any
	if err := json.Unmarshal([]byte(`{"components":{"schemas":{
		"Child":{"x-typescript":true,"type":"object","additionalProperties":false,"required":["enabled"],"properties":{"enabled":{"type":"boolean"},"note":{"type":"string"}}},
		"Parent":{"x-typescript":true,"type":"object","additionalProperties":false,"required":["items"],"properties":{"items":{"type":"array","items":{"$ref":"#/components/schemas/Child"}}}}
	}}}`), &document); err != nil {
		t.Fatal(err)
	}
	output, err := renderModels(document)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"enabled": boolean`, `"note"?: string`, `"items": Array<Child>`} {
		if !strings.Contains(output, want) {
			t.Fatalf("missing %s in %s", want, output)
		}
	}
	if _, err := modelType(map[string]any{"type": "unsupported"}, nil); err == nil {
		t.Fatal("unsupported shape silently accepted")
	}
	if _, err := modelType(map[string]any{"$ref": "#/components/schemas/Missing"}, nil); err == nil {
		t.Fatal("missing reference accepted")
	}
}
