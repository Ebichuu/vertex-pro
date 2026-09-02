package buildidentity

import (
	"runtime/debug"
	"strings"
)

type Identity struct {
	Revision  string
	Clean     bool
	Available bool
}

// Current returns the VCS identity embedded by the Go toolchain. A missing
// revision or a modified worktree is intentionally not eligible for persisted
// profile verification evidence.
func Current() Identity {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return Identity{}
	}
	return fromSettings(info.Settings)
}

func fromSettings(settings []debug.BuildSetting) Identity {
	identity := Identity{}
	hasModifiedFact := false
	for _, setting := range settings {
		switch setting.Key {
		case "vcs.revision":
			identity.Revision = strings.TrimSpace(setting.Value)
		case "vcs.modified":
			hasModifiedFact = true
			identity.Clean = strings.EqualFold(strings.TrimSpace(setting.Value), "false")
		}
	}
	identity.Available = identity.Revision != ""
	if !identity.Available || !hasModifiedFact {
		identity.Clean = false
	}
	return identity
}
