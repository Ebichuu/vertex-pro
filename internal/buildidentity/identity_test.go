package buildidentity

import (
	"runtime/debug"
	"testing"
)

func TestFromSettingsRequiresRevisionAndCleanWorktree(t *testing.T) {
	clean := fromSettings([]debug.BuildSetting{{Key: "vcs.revision", Value: "abc123"}, {Key: "vcs.modified", Value: "false"}})
	if !clean.Available || !clean.Clean || clean.Revision != "abc123" {
		t.Fatalf("clean identity=%+v", clean)
	}
	dirty := fromSettings([]debug.BuildSetting{{Key: "vcs.revision", Value: "abc123"}, {Key: "vcs.modified", Value: "true"}})
	if !dirty.Available || dirty.Clean {
		t.Fatalf("dirty identity=%+v", dirty)
	}
	missingModified := fromSettings([]debug.BuildSetting{{Key: "vcs.revision", Value: "abc123"}})
	if !missingModified.Available || missingModified.Clean {
		t.Fatalf("missing modified fact identity=%+v", missingModified)
	}
	missing := fromSettings(nil)
	if missing.Available || missing.Clean {
		t.Fatalf("missing identity=%+v", missing)
	}
}
