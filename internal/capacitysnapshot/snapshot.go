package capacitysnapshot

import (
	"strings"
	"time"
)

type State string

const (
	StateTrusted   State = "TRUSTED"
	StateUntrusted State = "CAPACITY_UNCERTAIN"
)

type Input struct {
	PhysicalFreeBytes *int64
	CollectedAt       time.Time
	Revision          *int64
	Source            string
	Digest            string
	Fallback          bool
}

type Snapshot struct {
	PhysicalFreeBytes int64
	CollectedAt       time.Time
	Revision          int64
	Source            string
	Digest            string
}

type Result struct {
	State    State
	Snapshot *Snapshot
	Reasons  []string
}

func Validate(input Input, now time.Time, maxAge time.Duration) Result {
	reasons := make([]string, 0)
	if input.PhysicalFreeBytes == nil {
		reasons = append(reasons, "PHYSICAL_FREE_MISSING")
	} else if *input.PhysicalFreeBytes < 0 {
		reasons = append(reasons, "PHYSICAL_FREE_NEGATIVE")
	}
	if input.CollectedAt.IsZero() {
		reasons = append(reasons, "COLLECTED_AT_MISSING")
	} else if now.Before(input.CollectedAt) {
		reasons = append(reasons, "COLLECTED_AT_IN_FUTURE")
	} else if maxAge <= 0 || now.Sub(input.CollectedAt) > maxAge {
		reasons = append(reasons, "SNAPSHOT_STALE")
	}
	if input.Revision == nil {
		reasons = append(reasons, "SNAPSHOT_REVISION_MISSING")
	} else if *input.Revision < 0 {
		reasons = append(reasons, "SNAPSHOT_REVISION_NEGATIVE")
	}
	if strings.TrimSpace(input.Source) == "" {
		reasons = append(reasons, "SNAPSHOT_SOURCE_MISSING")
	}
	if strings.TrimSpace(input.Digest) == "" {
		reasons = append(reasons, "SNAPSHOT_DIGEST_MISSING")
	}
	if input.Fallback {
		reasons = append(reasons, "SNAPSHOT_FALLBACK_REJECTED")
	}
	if len(reasons) > 0 {
		return Result{State: StateUntrusted, Reasons: reasons}
	}
	snapshot := &Snapshot{PhysicalFreeBytes: *input.PhysicalFreeBytes, CollectedAt: input.CollectedAt, Revision: *input.Revision, Source: input.Source, Digest: input.Digest}
	return Result{State: StateTrusted, Snapshot: snapshot}
}
