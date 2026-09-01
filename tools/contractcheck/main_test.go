package main

import "testing"

func TestParseManifestPaths(t *testing.T) {
	content := "schema_version: 1\ninputs:\n  - contracts/one.yaml\n  - docs/spec.md\nchecks:\n  - C1-SCHEMA-FRESH\n"
	got := parseManifestPaths(content)
	want := []string{"contracts/one.yaml", "docs/spec.md"}
	if len(got) != len(want) {
		t.Fatalf("got %d paths, want %d", len(got), len(want))
	}
	for index := range want {
		if got[index] != want[index] {
			t.Errorf("path[%d] = %q, want %q", index, got[index], want[index])
		}
	}
}

func TestAppendDetails(t *testing.T) {
	tests := []struct {
		name     string
		existing any
		failures []string
		want     []string
	}{
		{name: "string", existing: "schema missing", failures: []string{"input missing"}, want: []string{"schema missing", "input missing"}},
		{name: "slice", existing: []string{"schema missing"}, failures: []string{"input missing"}, want: []string{"schema missing", "input missing"}},
		{name: "nil", existing: nil, failures: []string{"input missing"}, want: []string{"input missing"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := appendDetails(test.existing, test.failures)
			if len(got) != len(test.want) {
				t.Fatalf("got %#v, want %#v", got, test.want)
			}
			for index := range test.want {
				if got[index] != test.want[index] {
					t.Errorf("detail[%d] = %q, want %q", index, got[index], test.want[index])
				}
			}
		})
	}
}
