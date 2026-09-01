package capacitysnapshot

import (
	"testing"
	"time"
)

func TestValidateAcceptsFreshSnapshot(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	freeBytes := int64(10_000)
	revision := int64(4)
	result := Validate(Input{PhysicalFreeBytes: &freeBytes, CollectedAt: now.Add(-5 * time.Minute), Revision: &revision, Source: "QUI_FULL_SYNC", Digest: "sha256:snapshot"}, now, time.Hour)
	if result.State != StateTrusted || result.Snapshot == nil || result.Snapshot.PhysicalFreeBytes != freeBytes {
		t.Fatalf("result = %#v, want trusted snapshot", result)
	}
}

func TestValidateFailsClosedForMissingOrNegativeFreeBytes(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	revision := int64(1)
	cases := []struct {
		name   string
		free   *int64
		reason string
	}{
		{name: "missing", free: nil, reason: "PHYSICAL_FREE_MISSING"},
		{name: "negative", free: int64Pointer(-1), reason: "PHYSICAL_FREE_NEGATIVE"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			result := Validate(Input{PhysicalFreeBytes: testCase.free, CollectedAt: now, Revision: &revision, Source: "QUI_FULL_SYNC", Digest: "sha256:snapshot"}, now, time.Hour)
			if result.State != StateUntrusted {
				t.Fatalf("state = %q, want untrusted", result.State)
			}
			assertReason(t, result.Reasons, testCase.reason)
		})
	}
}

func TestValidateFailsClosedForStaleAndFutureSnapshots(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	freeBytes := int64(10_000)
	revision := int64(1)
	cases := []struct {
		name           string
		collectedAt    time.Time
		expectedReason string
	}{
		{name: "stale", collectedAt: now.Add(-2 * time.Hour), expectedReason: "SNAPSHOT_STALE"},
		{name: "future", collectedAt: now.Add(time.Minute), expectedReason: "COLLECTED_AT_IN_FUTURE"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			result := Validate(Input{PhysicalFreeBytes: &freeBytes, CollectedAt: testCase.collectedAt, Revision: &revision, Source: "QUI_FULL_SYNC", Digest: "sha256:snapshot"}, now, time.Hour)
			if result.State != StateUntrusted {
				t.Fatalf("state = %q, want untrusted", result.State)
			}
			assertReason(t, result.Reasons, testCase.expectedReason)
		})
	}
}

func TestValidateRejectsFallbackAndMissingSourceMetadata(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	freeBytes := int64(10_000)
	revision := int64(1)
	result := Validate(Input{PhysicalFreeBytes: &freeBytes, CollectedAt: now, Revision: &revision, Source: "fallback", Fallback: true}, now, time.Hour)
	if result.State != StateUntrusted {
		t.Fatalf("state = %q, want untrusted", result.State)
	}
	assertReason(t, result.Reasons, "SNAPSHOT_FALLBACK_REJECTED")
	assertReason(t, result.Reasons, "SNAPSHOT_DIGEST_MISSING")
}

func TestValidateRejectsMissingOrNegativeRevision(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	freeBytes := int64(10_000)
	cases := []struct {
		name   string
		rev    *int64
		reason string
	}{
		{name: "missing", rev: nil, reason: "SNAPSHOT_REVISION_MISSING"},
		{name: "negative", rev: int64Pointer(-1), reason: "SNAPSHOT_REVISION_NEGATIVE"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			result := Validate(Input{PhysicalFreeBytes: &freeBytes, CollectedAt: now, Revision: testCase.rev, Source: "QUI_FULL_SYNC", Digest: "sha256:snapshot"}, now, time.Hour)
			if result.State != StateUntrusted {
				t.Fatalf("state = %q, want untrusted", result.State)
			}
			assertReason(t, result.Reasons, testCase.reason)
		})
	}
}

func int64Pointer(value int64) *int64 {
	return &value
}

func assertReason(t *testing.T, reasons []string, expected string) {
	t.Helper()
	for _, reason := range reasons {
		if reason == expected {
			return
		}
	}
	t.Fatalf("reasons = %#v, missing %q", reasons, expected)
}
