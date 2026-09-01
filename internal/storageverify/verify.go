package storageverify

import (
	"errors"
	"path/filepath"
	"strings"
	"time"
)

type VerificationState string

const (
	StateUnverified VerificationState = "UNVERIFIED"
	StateVerified   VerificationState = "VERIFIED"
)

type StorageTarget struct {
	ID                    string
	ManagedRoot           string
	QBDefaultSavePath     string
	FilesystemKey         string
	ConfigurationRevision int64
}

type Evidence struct {
	ID                    string
	StorageTargetID       string
	Method                string
	ManagedRoot           string
	QBDefaultSavePath     string
	FilesystemKey         string
	EvidenceDigest        string
	VerifiedBy            string
	VerifiedAt            time.Time
	ConfigurationRevision int64
	State                 VerificationState
}

type Result struct {
	State   VerificationState
	Reasons []string
}

func Validate(target StorageTarget, evidence Evidence, now time.Time, maxAge time.Duration) Result {
	reasons := make([]string, 0)
	if strings.TrimSpace(target.ID) == "" {
		reasons = append(reasons, "STORAGE_TARGET_ID_MISSING")
	}
	if !absolutePath(target.ManagedRoot) {
		reasons = append(reasons, "MANAGED_ROOT_INVALID")
	}
	if !absolutePath(target.QBDefaultSavePath) {
		reasons = append(reasons, "QB_DEFAULT_SAVE_PATH_INVALID")
	}
	if evidence.ID == "" {
		reasons = append(reasons, "VERIFICATION_ID_MISSING")
	}
	if evidence.StorageTargetID != target.ID {
		reasons = append(reasons, "STORAGE_TARGET_MISMATCH")
	}
	if evidence.Method != "OPERATOR_DEVICE_EVIDENCE" {
		reasons = append(reasons, "VERIFICATION_METHOD_UNSUPPORTED")
	}
	if evidence.ManagedRoot != target.ManagedRoot {
		reasons = append(reasons, "MANAGED_ROOT_MISMATCH")
	}
	if evidence.QBDefaultSavePath != target.QBDefaultSavePath {
		reasons = append(reasons, "QB_DEFAULT_SAVE_PATH_MISMATCH")
	}
	if strings.TrimSpace(target.FilesystemKey) == "" || evidence.FilesystemKey == "" || evidence.FilesystemKey != target.FilesystemKey {
		reasons = append(reasons, "FILESYSTEM_KEY_MISMATCH")
	}
	if strings.TrimSpace(evidence.EvidenceDigest) == "" {
		reasons = append(reasons, "EVIDENCE_DIGEST_MISSING")
	}
	if strings.TrimSpace(evidence.VerifiedBy) == "" {
		reasons = append(reasons, "VERIFIED_BY_MISSING")
	}
	if evidence.ConfigurationRevision != target.ConfigurationRevision {
		reasons = append(reasons, "CONFIGURATION_REVISION_MISMATCH")
	}
	if evidence.State != StateVerified {
		reasons = append(reasons, "VERIFICATION_NOT_VERIFIED")
	}
	if maxAge <= 0 || evidence.VerifiedAt.IsZero() || now.Before(evidence.VerifiedAt) || now.Sub(evidence.VerifiedAt) > maxAge {
		reasons = append(reasons, "VERIFICATION_EXPIRED")
	}
	if len(reasons) > 0 {
		return Result{State: StateUnverified, Reasons: reasons}
	}
	return Result{State: StateVerified}
}

func absolutePath(value string) bool {
	if strings.TrimSpace(value) == "" {
		return false
	}
	cleaned := filepath.Clean(value)
	return (filepath.IsAbs(value) || strings.HasPrefix(value, "/") || (len(value) >= 3 && value[1] == ':')) && cleaned != "." && !strings.Contains(value, "\\x00")
}

var ErrStorageUnverified = errors.New("storage target is unverified")
