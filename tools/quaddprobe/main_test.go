package main

import "testing"

func TestInactiveState(t *testing.T) {
	tests := map[string]bool{
		"stoppedDL":          true,
		"pausedUP":           true,
		" stoppedDL ":        true,
		"checkingResumeData": false,
		"downloading":        false,
		"":                   false,
	}
	for state, expected := range tests {
		if actual := inactiveState(state); actual != expected {
			t.Fatalf("inactiveState(%q) = %v, want %v", state, actual, expected)
		}
	}
}
