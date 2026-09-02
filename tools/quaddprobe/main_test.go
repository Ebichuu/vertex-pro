package main

import (
	"testing"

	"github.com/vertex-pro/vertex-pro/internal/qbprofile"
)

func TestSupportsInactiveAddProfile(t *testing.T) {
	tests := []struct {
		profile  qbprofile.Profile
		expected bool
	}{
		{profile: qbprofile.Profile{ID: "QB_438", AddStartField: "paused"}, expected: true},
		{profile: qbprofile.Profile{ID: "QB_504", AddStartField: "stopped"}, expected: true},
		{profile: qbprofile.Profile{ID: "QB_522", AddStartField: "stopped"}, expected: true},
		{profile: qbprofile.Profile{ID: "QB_438", AddStartField: "stopped"}, expected: false},
		{profile: qbprofile.Profile{ID: "UNKNOWN", AddStartField: "stopped"}, expected: false},
	}
	for _, test := range tests {
		if actual := supportsInactiveAddProfile(test.profile); actual != test.expected {
			t.Fatalf("supportsInactiveAddProfile(%+v) = %v, want %v", test.profile, actual, test.expected)
		}
	}
}

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
