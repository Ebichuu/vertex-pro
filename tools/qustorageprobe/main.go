package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/amountleft"
	"github.com/vertex-pro/vertex-pro/internal/capacitysnapshot"
	"github.com/vertex-pro/vertex-pro/internal/qui/proxy"
	"github.com/vertex-pro/vertex-pro/internal/secrets"
	"github.com/vertex-pro/vertex-pro/internal/storageverify"
)

type readOnlyTransport struct {
	base   http.RoundTripper
	writes atomic.Int64
}

func (transport *readOnlyTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Method != http.MethodGet {
		transport.writes.Add(1)
		return nil, fmt.Errorf("read-only storage probe rejected HTTP %s", request.Method)
	}
	return transport.base.RoundTrip(request)
}

type report struct {
	Status string `json:"status"`
	Safety struct {
		ReadOnlyHTTP         bool  `json:"read_only_http"`
		NetworkWriteAttempts int64 `json:"network_write_attempts"`
		PathsRedacted        bool  `json:"paths_redacted"`
		SecretsRedacted      bool  `json:"secrets_redacted"`
	} `json:"safety"`
	Evidence struct {
		DefaultSavePathPresent        bool `json:"default_save_path_present"`
		PhysicalFreePresent           bool `json:"physical_free_present"`
		PhysicalFreeValid             bool `json:"physical_free_valid"`
		SnapshotRIDPresent            bool `json:"snapshot_rid_present"`
		SnapshotFresh                 bool `json:"snapshot_fresh"`
		OperatorDeviceEvidencePresent bool `json:"operator_device_evidence_present"`
		DefaultPathMatchesManagedRoot bool `json:"default_path_matches_managed_root"`
		FilesystemKeyPresent          bool `json:"filesystem_key_present"`
	} `json:"evidence"`
	Assessment struct {
		CapacitySnapshotState    string   `json:"capacity_snapshot_state"`
		StorageVerificationState string   `json:"storage_verification_state"`
		PlacementAllowed         bool     `json:"placement_allowed"`
		ProfilePromotionAllowed  bool     `json:"profile_promotion_allowed"`
		MissingEvidence          []string `json:"missing_evidence"`
		FailClosedReason         string   `json:"fail_closed_reason"`
	} `json:"assessment"`
}

type operatorEvidenceFile struct {
	ID                    string `json:"id"`
	StorageTargetID       string `json:"storage_target_id"`
	Method                string `json:"method"`
	ManagedRoot           string `json:"managed_root"`
	QBDefaultSavePath     string `json:"qb_default_save_path"`
	FilesystemKey         string `json:"filesystem_key"`
	MountSource           string `json:"mount_source"`
	MountTarget           string `json:"mount_target"`
	FilesystemType        string `json:"filesystem_type"`
	DeviceDecimal         string `json:"device_decimal"`
	EvidenceDigest        string `json:"evidence_digest"`
	VerifiedBy            string `json:"verified_by"`
	VerifiedAt            string `json:"verified_at"`
	ConfigurationRevision int64  `json:"configuration_revision"`
	State                 string `json:"state"`
}

func main() {
	if err := run(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "qui storage probe failed:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	baseURL := strings.TrimSpace(os.Getenv("VERTEX_PRO_QUI_URL"))
	if baseURL == "" {
		return errors.New("VERTEX_PRO_QUI_URL is required")
	}
	transport := &readOnlyTransport{base: http.DefaultTransport}
	client, err := proxy.New(ctx, proxy.Config{
		BaseURL:  baseURL,
		ProxyKey: secrets.SecretRef{Provider: "env", Key: "VERTEX_PRO_QUI_PROXY_KEY"},
		Store:    secrets.EnvStore{},
		HTTPClient: &http.Client{
			Timeout:   60 * time.Second,
			Transport: transport,
		},
	})
	if err != nil {
		return err
	}
	preferences, err := client.Preferences(ctx)
	if err != nil {
		return fmt.Errorf("read qB preferences: %w", err)
	}
	savePath, pathPresent := preferences["save_path"].(string)
	pathPresent = pathPresent && strings.TrimSpace(savePath) != ""

	syncBody, err := client.SyncMainData(ctx, 0)
	if err != nil {
		return fmt.Errorf("read qB Sync baseline: %w", err)
	}
	serverState, statePresent := syncBody["server_state"].(map[string]any)
	physicalRaw, physicalPresent := any(nil), false
	if statePresent {
		physicalRaw, physicalPresent = serverState["free_space_on_disk"]
	}
	physical := amountleft.ResolveFull(amountleft.Observation{Present: physicalPresent, Raw: physicalRaw})
	rid, ridPresent := uint64Value(syncBody["rid"])
	now := time.Now().UTC()
	digest := evidenceDigest(savePath, physicalRaw, rid)
	revision := int64(rid)
	capacity := capacitysnapshot.Validate(capacitysnapshot.Input{
		PhysicalFreeBytes: physical.Value,
		CollectedAt:       now,
		Revision:          &revision,
		Source:            "qb_default_save_path",
		Digest:            digest,
	}, now, 30*time.Second)
	operatorEvidence, evidencePresent, err := loadOperatorEvidence()
	if err != nil {
		return err
	}
	storageState := storageverify.StateUnverified
	missingEvidence := []string{
		"managed_root",
		"filesystem_id_or_device_id_or_mount_fingerprint",
		"operator_device_evidence_digest",
		"verified_by_and_verified_at",
		"configuration_revision_binding",
	}
	failClosedReason := "qB WebAPI proves only the default save path and its reported free space; it cannot prove that a configured managed root is on the same filesystem"
	if evidencePresent {
		verifiedAt, parseErr := time.Parse(time.RFC3339Nano, operatorEvidence.VerifiedAt)
		if parseErr != nil {
			return fmt.Errorf("parse operator evidence verified_at: %w", parseErr)
		}
		verification := storageverify.Validate(storageverify.StorageTarget{
			ID:                    operatorEvidence.StorageTargetID,
			ManagedRoot:           operatorEvidence.ManagedRoot,
			QBDefaultSavePath:     savePath,
			FilesystemKey:         operatorEvidence.FilesystemKey,
			ConfigurationRevision: operatorEvidence.ConfigurationRevision,
		}, storageverify.Evidence{
			ID:                    operatorEvidence.ID,
			StorageTargetID:       operatorEvidence.StorageTargetID,
			Method:                operatorEvidence.Method,
			ManagedRoot:           operatorEvidence.ManagedRoot,
			QBDefaultSavePath:     operatorEvidence.QBDefaultSavePath,
			FilesystemKey:         operatorEvidence.FilesystemKey,
			EvidenceDigest:        operatorEvidence.EvidenceDigest,
			VerifiedBy:            operatorEvidence.VerifiedBy,
			VerifiedAt:            verifiedAt,
			ConfigurationRevision: operatorEvidence.ConfigurationRevision,
			State:                 storageverify.VerificationState(operatorEvidence.State),
		}, now, 30*24*time.Hour)
		storageState = verification.State
		if storageState != storageverify.StateVerified {
			return fmt.Errorf("operator storage evidence is unverified: %s", strings.Join(verification.Reasons, ","))
		}
		missingEvidence = []string{}
		failClosedReason = "storage evidence is verified, but the live QB_504 product profile remains EXPECTED rather than VERIFIED"
	}

	output := report{Status: "PASS"}
	output.Safety.ReadOnlyHTTP = transport.writes.Load() == 0
	output.Safety.NetworkWriteAttempts = transport.writes.Load()
	output.Safety.PathsRedacted = true
	output.Safety.SecretsRedacted = true
	output.Evidence.DefaultSavePathPresent = pathPresent
	output.Evidence.PhysicalFreePresent = physicalPresent
	output.Evidence.PhysicalFreeValid = physical.State == amountleft.StateKnown
	output.Evidence.SnapshotRIDPresent = ridPresent
	output.Evidence.SnapshotFresh = capacity.State == capacitysnapshot.StateTrusted
	output.Evidence.OperatorDeviceEvidencePresent = evidencePresent
	output.Evidence.DefaultPathMatchesManagedRoot = evidencePresent && savePath == operatorEvidence.ManagedRoot && savePath == operatorEvidence.QBDefaultSavePath
	output.Evidence.FilesystemKeyPresent = evidencePresent && strings.TrimSpace(operatorEvidence.FilesystemKey) != ""
	output.Assessment.CapacitySnapshotState = string(capacity.State)
	output.Assessment.StorageVerificationState = string(storageState)
	output.Assessment.PlacementAllowed = false
	output.Assessment.ProfilePromotionAllowed = false
	output.Assessment.MissingEvidence = missingEvidence
	output.Assessment.FailClosedReason = failClosedReason
	if !output.Safety.ReadOnlyHTTP || !pathPresent || physical.State != amountleft.StateKnown || !ridPresent || capacity.State != capacitysnapshot.StateTrusted || output.Assessment.PlacementAllowed {
		output.Status = "FAIL"
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(output); err != nil {
		return err
	}
	if output.Status != "PASS" {
		return errors.New("storage evidence assertions failed")
	}
	return nil
}

func loadOperatorEvidence() (operatorEvidenceFile, bool, error) {
	path := strings.TrimSpace(os.Getenv("VERTEX_PRO_STORAGE_EVIDENCE_FILE"))
	if path == "" {
		return operatorEvidenceFile{}, false, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return operatorEvidenceFile{}, false, fmt.Errorf("open operator storage evidence: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 64<<10))
	decoder.DisallowUnknownFields()
	var evidence operatorEvidenceFile
	if err := decoder.Decode(&evidence); err != nil {
		return operatorEvidenceFile{}, false, fmt.Errorf("decode operator storage evidence: %w", err)
	}
	device, err := strconv.ParseUint(evidence.DeviceDecimal, 10, 64)
	if err != nil || device == 0 {
		return operatorEvidenceFile{}, false, errors.New("operator storage device id is invalid")
	}
	expectedFilesystemKey := "linux-device:" + strings.ToLower(strconv.FormatUint(device, 16))
	if evidence.FilesystemKey != expectedFilesystemKey {
		return operatorEvidenceFile{}, false, errors.New("operator storage filesystem key does not match the device id")
	}
	if strings.TrimSpace(evidence.MountSource) == "" || strings.TrimSpace(evidence.MountTarget) == "" || strings.TrimSpace(evidence.FilesystemType) == "" {
		return operatorEvidenceFile{}, false, errors.New("operator storage mount evidence is incomplete")
	}
	canonical := strings.Join([]string{evidence.ManagedRoot, evidence.QBDefaultSavePath, evidence.FilesystemKey, evidence.MountSource, evidence.MountTarget, evidence.FilesystemType, evidence.DeviceDecimal}, "\n")
	digest := sha256.Sum256([]byte(canonical))
	if !strings.EqualFold(evidence.EvidenceDigest, hex.EncodeToString(digest[:])) {
		return operatorEvidenceFile{}, false, errors.New("operator storage evidence digest mismatch")
	}
	return evidence, true, nil
}

func uint64Value(raw any) (uint64, bool) {
	switch value := raw.(type) {
	case float64:
		if value < 0 || value > math.MaxUint64 || value != math.Trunc(value) {
			return 0, false
		}
		return uint64(value), true
	case uint64:
		return value, true
	case int64:
		if value < 0 {
			return 0, false
		}
		return uint64(value), true
	default:
		return 0, false
	}
}

func evidenceDigest(path string, physical any, rid uint64) string {
	encoded, _ := json.Marshal(struct {
		Path     string `json:"path"`
		Physical any    `json:"physical"`
		RID      uint64 `json:"rid"`
	}{Path: path, Physical: physical, RID: rid})
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}
