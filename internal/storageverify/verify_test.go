package storageverify

import (
	"testing"
	"time"
)

func TestValidateAcceptsFreshMatchingEvidence(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	target := StorageTarget{ID: "target-1", ManagedRoot: "/data/media", QBDefaultSavePath: "/data/media/downloads", FilesystemKey: "device-42", ConfigurationRevision: 7}
	evidence := Evidence{ID: "verification-1", StorageTargetID: "target-1", Method: "OPERATOR_DEVICE_EVIDENCE", ManagedRoot: "/data/media", QBDefaultSavePath: "/data/media/downloads", FilesystemKey: "device-42", EvidenceDigest: "sha256:device-42", VerifiedBy: "operator", VerifiedAt: now.Add(-10 * time.Minute), ConfigurationRevision: 7, State: StateVerified}
	result := Validate(target, evidence, now, time.Hour)
	if result.State != StateVerified || len(result.Reasons) != 0 {
		t.Fatalf("result = %#v, want verified", result)
	}
}

func TestValidateFailsClosedForPathAndRevisionMismatch(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	target := StorageTarget{ID: "target-1", ManagedRoot: "/data/media", QBDefaultSavePath: "/data/media/downloads", FilesystemKey: "device-42", ConfigurationRevision: 8}
	evidence := Evidence{ID: "verification-1", StorageTargetID: "target-1", Method: "OPERATOR_DEVICE_EVIDENCE", ManagedRoot: "/data/old", QBDefaultSavePath: "/data/media/downloads", FilesystemKey: "device-42", EvidenceDigest: "sha256:device-42", VerifiedBy: "operator", VerifiedAt: now, ConfigurationRevision: 7, State: StateVerified}
	result := Validate(target, evidence, now, time.Hour)
	if result.State != StateUnverified {
		t.Fatalf("state = %q, want unverified", result.State)
	}
	assertReason(t, result.Reasons, "MANAGED_ROOT_MISMATCH")
	assertReason(t, result.Reasons, "CONFIGURATION_REVISION_MISMATCH")
}

func TestValidateFailsClosedForExpiredEvidence(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	target := StorageTarget{ID: "target-1", ManagedRoot: "/data/media", QBDefaultSavePath: "/data/media/downloads", FilesystemKey: "device-42", ConfigurationRevision: 7}
	evidence := Evidence{ID: "verification-1", StorageTargetID: "target-1", Method: "OPERATOR_DEVICE_EVIDENCE", ManagedRoot: "/data/media", QBDefaultSavePath: "/data/media/downloads", FilesystemKey: "device-42", EvidenceDigest: "sha256:device-42", VerifiedBy: "operator", VerifiedAt: now.Add(-2 * time.Hour), ConfigurationRevision: 7, State: StateVerified}
	result := Validate(target, evidence, now, time.Hour)
	if result.State != StateUnverified {
		t.Fatalf("state = %q, want unverified", result.State)
	}
	assertReason(t, result.Reasons, "VERIFICATION_EXPIRED")
}

func TestValidateFailsClosedForUntrustedFilesystemEvidence(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	target := StorageTarget{ID: "target-1", ManagedRoot: "/data/media", QBDefaultSavePath: "/data/media/downloads", FilesystemKey: "device-42", ConfigurationRevision: 7}
	evidence := Evidence{ID: "verification-1", StorageTargetID: "target-1", Method: "CONFIG_BOOLEAN", ManagedRoot: "/data/media", QBDefaultSavePath: "/data/media/downloads", FilesystemKey: "device-42", EvidenceDigest: "sha256:device-42", VerifiedBy: "operator", VerifiedAt: now, ConfigurationRevision: 7, State: StateVerified}
	result := Validate(target, evidence, now, time.Hour)
	if result.State != StateUnverified {
		t.Fatalf("state = %q, want unverified", result.State)
	}
	assertReason(t, result.Reasons, "VERIFICATION_METHOD_UNSUPPORTED")
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
