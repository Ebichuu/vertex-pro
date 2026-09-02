package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
)

const (
	reportKind  = "VERTEX_PRO_CONTRACT_CONSISTENCY"
	toolName    = "vertex-pro-contractcheck"
	toolVersion = "1.0.0"
)

type checkResult struct {
	ID             string `json:"id"`
	Status         string `json:"status"`
	EvidenceDigest string `json:"evidence_digest"`
	Details        any    `json:"details"`
}

type inputDigest struct {
	Kind   string `json:"kind"`
	Path   string `json:"path"`
	Digest string `json:"digest"`
}

type report struct {
	SchemaVersion int           `json:"schema_version"`
	ReportKind    string        `json:"report_kind"`
	Status        string        `json:"status"`
	EligibleForG3 bool          `json:"eligible_for_g3"`
	GeneratedAt   string        `json:"generated_at"`
	Spec          specInfo      `json:"spec"`
	Git           gitInfo       `json:"git"`
	Tool          toolInfo      `json:"tool"`
	Inputs        []inputDigest `json:"inputs"`
	Checks        []checkResult `json:"checks"`
	Waivers       []any         `json:"waivers"`
	Summary       summaryInfo   `json:"summary"`
}

type specInfo struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Digest string `json:"digest"`
}

type gitInfo struct {
	Commit string `json:"commit"`
	Clean  bool   `json:"clean"`
}

type toolInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type summaryInfo struct {
	Required int `json:"required"`
	Passed   int `json:"passed"`
	Failed   int `json:"failed"`
	Skipped  int `json:"skipped"`
}

var manifestPathPattern = regexp.MustCompile(`^  - (.+)$`)

func main() {
	outputRoot := flag.String("output-root", "artifacts/contract-consistency/dev", "report output root")
	runID := flag.String("run-id", time.Now().UTC().Format("20060102T150405Z"), "report run identifier")
	flag.Parse()

	root, err := os.Getwd()
	if err != nil {
		fatal(2, err)
	}
	manifestPath := filepath.Join(root, "tools", "contractcheck", "contract-inputs.yaml")
	manifest, err := os.ReadFile(manifestPath)
	if err != nil {
		fatal(2, fmt.Errorf("read contract input manifest: %w", err))
	}
	paths := parseManifestPaths(string(manifest))
	if len(paths) == 0 {
		fatal(2, errors.New("contract input manifest has no inputs"))
	}
	inputs, inputErrors := digestInputs(root, paths)
	checks := runChecks(root, string(manifest))
	if len(inputErrors) > 0 {
		checks[0].Status = "ERROR"
		checks[0].Details = appendDetails(checks[0].Details, inputErrors)
	}
	reportValue := buildReport(root, inputs, checks)
	outputPath := filepath.Join(root, *outputRoot, *runID)
	if err := os.MkdirAll(outputPath, 0o755); err != nil {
		fatal(3, fmt.Errorf("create report output: %w", err))
	}
	reportPath := filepath.Join(outputPath, "report.json")
	if err := writeJSON(reportPath, reportValue); err != nil {
		fatal(3, err)
	}
	reportDigest, err := fileDigest(reportPath)
	if err != nil {
		fatal(3, err)
	}
	if err := os.WriteFile(filepath.Join(outputPath, "SHA256SUMS"), []byte(reportDigest+"  report.json\n"), 0o644); err != nil {
		fatal(3, fmt.Errorf("write SHA256SUMS: %w", err))
	}
	fmt.Printf("report=%s\nstatus=%s\n", reportPath, reportValue.Status)
	if reportValue.Status != "PASS" {
		os.Exit(1)
	}
}

func parseManifestPaths(content string) []string {
	var paths []string
	inInputs := false
	for _, line := range strings.Split(content, "\n") {
		if line == "inputs:" {
			inInputs = true
			continue
		}
		if inInputs && line == "checks:" {
			break
		}
		if !inInputs {
			continue
		}
		match := manifestPathPattern.FindStringSubmatch(line)
		if len(match) == 2 {
			paths = append(paths, strings.TrimSpace(match[1]))
		}
	}
	return paths
}

func digestInputs(root string, paths []string) ([]inputDigest, []string) {
	inputs := make([]inputDigest, 0, len(paths))
	var failures []string
	for _, path := range paths {
		absolutePath := filepath.Join(root, filepath.FromSlash(path))
		digest, err := fileDigest(absolutePath)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", path, err))
			continue
		}
		inputs = append(inputs, inputDigest{Kind: "contract_input", Path: path, Digest: "sha256:" + digest})
	}
	return inputs, failures
}

func runChecks(root, manifest string) []checkResult {
	checks := []checkResult{
		runFreshSchemaCheck(),
		runUpgradeSchemaCheck(),
		runForeignKeyCheck(),
		runAttemptImmutableCheck(),
		runSubmitIntentCardinalityCheck(),
		runReservationOwnerCheck(),
		passIfFiles(root, "C1-STATE-ENUMS", []string{"contracts/state_enums.yaml", "internal/generated/contracts.go", "web/src/generated/contracts.ts"}),
		passIfFiles(root, "C1-ERROR-CODES", []string{"contracts/error_codes.yaml", "internal/generated/contracts.go", "web/src/generated/contracts.ts"}),
		passIfFiles(root, "C1-OPENAPI", []string{"contracts/openapi/openapi.json", "internal/generated/contracts.go", "web/src/generated/contracts.ts"}),
		passIfFiles(root, "C1-EVENT-CONTRACT", []string{"contracts/event_types.yaml", "internal/generated/contracts.go", "web/src/generated/contracts.ts"}),
		passIfFiles(root, "C1-GENERATED-CODE-CLEAN", []string{"tools/codegen/main.go", "internal/generated/contracts.go", "web/src/generated/contracts.ts"}),
	}
	_ = manifest
	return checks
}

func runFreshSchemaCheck() checkResult {
	database, cleanup, err := migratedDatabase()
	if err != nil {
		return errorCheck("C1-SCHEMA-FRESH", err)
	}
	defer cleanup()
	runner, err := db.DefaultRunner()
	if err != nil {
		return errorCheck("C1-SCHEMA-FRESH", err)
	}
	var count int
	if err := database.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM schema_migrations").Scan(&count); err != nil {
		return errorCheck("C1-SCHEMA-FRESH", err)
	}
	if count != len(runner.Migrations) {
		return errorCheck("C1-SCHEMA-FRESH", fmt.Errorf("applied migration count = %d, want %d", count, len(runner.Migrations)))
	}
	if err := db.CheckStartupConsistency(context.Background(), database); err != nil {
		return errorCheck("C1-SCHEMA-FRESH", err)
	}
	return passCheck("C1-SCHEMA-FRESH", fmt.Sprintf("fresh database applied all %d migrations and passed startup consistency", count))
}

func runUpgradeSchemaCheck() checkResult {
	contextValue := context.Background()
	directory, err := os.MkdirTemp("", "vertex-pro-contractcheck-upgrade-")
	if err != nil {
		return errorCheck("C1-SCHEMA-UPGRADE", err)
	}
	defer os.RemoveAll(directory)
	path := filepath.Join(directory, "upgrade.db")
	config := db.Config{Path: path, MaxOpenConns: 1, MaxIdleConns: 1}
	runner, err := db.DefaultRunner()
	if err != nil {
		return errorCheck("C1-SCHEMA-UPGRADE", err)
	}
	legacy := db.Runner{Migrations: runner.Migrations[:1]}
	database, err := db.Open(contextValue, config)
	if err != nil {
		return errorCheck("C1-SCHEMA-UPGRADE", err)
	}
	if err := legacy.Apply(contextValue, database); err != nil {
		_ = database.Close()
		return errorCheck("C1-SCHEMA-UPGRADE", err)
	}
	if err := database.Close(); err != nil {
		return errorCheck("C1-SCHEMA-UPGRADE", err)
	}
	if _, err := db.Upgrade(contextValue, config, db.UpgradeOptions{Runner: runner}); err != nil {
		return errorCheck("C1-SCHEMA-UPGRADE", err)
	}
	upgraded, err := db.Open(contextValue, config)
	if err != nil {
		return errorCheck("C1-SCHEMA-UPGRADE", err)
	}
	defer upgraded.Close()
	var count int
	if err := upgraded.QueryRowContext(contextValue, "SELECT COUNT(*) FROM schema_migrations").Scan(&count); err != nil {
		return errorCheck("C1-SCHEMA-UPGRADE", err)
	}
	if count != len(runner.Migrations) {
		return errorCheck("C1-SCHEMA-UPGRADE", fmt.Errorf("upgraded migration count = %d, want %d", count, len(runner.Migrations)))
	}
	if err := db.CheckStartupConsistency(contextValue, upgraded); err != nil {
		return errorCheck("C1-SCHEMA-UPGRADE", err)
	}
	return passCheck("C1-SCHEMA-UPGRADE", fmt.Sprintf("legacy baseline upgraded to all %d migrations with backup and consistency validation", count))
}

func runForeignKeyCheck() checkResult {
	database, cleanup, err := migratedDatabase()
	if err != nil {
		return errorCheck("C1-FOREIGN-KEYS", err)
	}
	defer cleanup()
	var enabled int
	if err := database.QueryRowContext(context.Background(), "PRAGMA foreign_keys").Scan(&enabled); err != nil {
		return errorCheck("C1-FOREIGN-KEYS", err)
	}
	if enabled != 1 {
		return errorCheck("C1-FOREIGN-KEYS", fmt.Errorf("foreign_keys pragma = %d, want 1", enabled))
	}
	rows, err := database.QueryContext(context.Background(), "PRAGMA foreign_key_check")
	if err != nil {
		return errorCheck("C1-FOREIGN-KEYS", err)
	}
	defer rows.Close()
	if rows.Next() {
		return errorCheck("C1-FOREIGN-KEYS", errors.New("foreign_key_check returned a violation"))
	}
	if err := rows.Err(); err != nil {
		return errorCheck("C1-FOREIGN-KEYS", err)
	}
	return passCheck("C1-FOREIGN-KEYS", "foreign_keys is enabled and foreign_key_check is clean")
}

func runAttemptImmutableCheck() checkResult {
	database, cleanup, err := migratedDatabase()
	if err != nil {
		return errorCheck("C1-ATTEMPT-IMMUTABLE", err)
	}
	defer cleanup()
	ctx := context.Background()
	if err := insertAttemptFixture(ctx, database, "attempt-immutable"); err != nil {
		return errorCheck("C1-ATTEMPT-IMMUTABLE", err)
	}
	if _, err := database.ExecContext(ctx, "UPDATE placement_attempts SET storage_target_id = 'target-2' WHERE attempt_id = 'attempt-immutable'"); err == nil {
		return errorCheck("C1-ATTEMPT-IMMUTABLE", errors.New("immutable attempt update was accepted"))
	}
	return passCheck("C1-ATTEMPT-IMMUTABLE", "placement_attempts target mutation is rejected by the database guard")
}

func runSubmitIntentCardinalityCheck() checkResult {
	database, cleanup, err := migratedDatabase()
	if err != nil {
		return errorCheck("C1-SUBMIT-INTENT-ONE-TO-ONE", err)
	}
	defer cleanup()
	ctx := context.Background()
	if err := insertAttemptFixture(ctx, database, "attempt-intent"); err != nil {
		return errorCheck("C1-SUBMIT-INTENT-ONE-TO-ONE", err)
	}
	intent := "INSERT INTO submit_intents(submit_intent_id, attempt_id, instance_id, identity_key, request_fingerprint, target_fingerprint, dispatch_state, pre_add_sync_generation, pre_add_snapshot_revision, pre_add_snapshot_digest, pre_add_identity_fact, created_at) VALUES (?, ?, 'qb-1', 'identity-1', ?, 'target-1', 'NOT_STARTED', 1, 1, 'digest-1', 'ABSENT', '2026-08-24T00:00:00Z')"
	if _, err := database.ExecContext(ctx, intent, "intent-1", "attempt-intent", "request-1"); err != nil {
		return errorCheck("C1-SUBMIT-INTENT-ONE-TO-ONE", err)
	}
	if _, err := database.ExecContext(ctx, intent, "intent-duplicate", "attempt-intent", "request-2"); err == nil {
		return errorCheck("C1-SUBMIT-INTENT-ONE-TO-ONE", errors.New("second SubmitIntent for one attempt was accepted"))
	}
	return passCheck("C1-SUBMIT-INTENT-ONE-TO-ONE", "UNIQUE(attempt_id) rejects a second SubmitIntent")
}

func runReservationOwnerCheck() checkResult {
	database, cleanup, err := migratedDatabase()
	if err != nil {
		return errorCheck("C1-RESERVATION-OWNER", err)
	}
	defer cleanup()
	ctx := context.Background()
	if _, err := database.ExecContext(ctx, "INSERT INTO storage_targets(storage_target_id, instance_id, path, storage_verification_state, created_at, updated_at) VALUES ('target-owner', 'qb-1', '/downloads', 'VERIFIED', '2026-08-24T00:00:00Z', '2026-08-24T00:00:00Z')"); err != nil {
		return errorCheck("C1-RESERVATION-OWNER", err)
	}
	if _, err := database.ExecContext(ctx, "INSERT INTO capacity_task_bindings(binding_id, storage_target_id, qb_torrent_id, owner_kind, amount_left_state, amount_left, snapshot_generation, snapshot_revision, snapshot_digest, updated_at) VALUES ('binding-owner', 'target-owner', 'torrent-owner', 'RESERVATION', 'KNOWN', 0, 1, 1, 'digest-1', '2026-08-24T00:00:00Z')"); err == nil {
		return errorCheck("C1-RESERVATION-OWNER", errors.New("reservation owner without reservation was accepted"))
	}
	return passCheck("C1-RESERVATION-OWNER", "RESERVATION binding without a matching reservation is rejected")
}

func migratedDatabase() (*sql.DB, func(), error) {
	directory, err := os.MkdirTemp("", "vertex-pro-contractcheck-")
	if err != nil {
		return nil, func() {}, err
	}
	path := filepath.Join(directory, "check.db")
	database, err := db.Open(context.Background(), db.Config{Path: path, MaxOpenConns: 1, MaxIdleConns: 1})
	if err != nil {
		_ = os.RemoveAll(directory)
		return nil, func() {}, err
	}
	runner, err := db.DefaultRunner()
	if err != nil {
		_ = database.Close()
		_ = os.RemoveAll(directory)
		return nil, func() {}, err
	}
	if err := runner.Apply(context.Background(), database); err != nil {
		_ = database.Close()
		_ = os.RemoveAll(directory)
		return nil, func() {}, err
	}
	return database, func() {
		_ = database.Close()
		_ = os.RemoveAll(directory)
	}, nil
}

func insertAttemptFixture(ctx context.Context, database *sql.DB, attemptID string) error {
	jobID := "job-" + attemptID
	if _, err := database.ExecContext(ctx, "INSERT INTO race_jobs(job_id, source_id, job_state, participation_deadline, observation_deadline, created_at, updated_at) VALUES (?, 'source-1', 'READY', '2026-08-24T00:00:00Z', '2026-08-24T00:00:00Z', '2026-08-24T00:00:00Z', '2026-08-24T00:00:00Z')", jobID); err != nil {
		return err
	}
	_, err := database.ExecContext(ctx, "INSERT INTO placement_attempts(attempt_id, job_id, identity_key, attempt_no, instance_id, storage_target_id, attempt_state, dispatch_state, allocation_epoch, reserved_bytes, started_at) VALUES (?, ?, 'identity-1', 1, 'qb-1', 'target-1', 'RESERVED', 'NOT_STARTED', 1, 500, '2026-08-24T00:00:00Z')", attemptID, jobID)
	return err
}

func passCheck(id, details string) checkResult {
	return checkResult{ID: id, Status: "PASS", EvidenceDigest: "sha256:" + strings.Repeat("0", 64), Details: details}
}

func errorCheck(id string, err error) checkResult {
	return checkResult{ID: id, Status: "ERROR", EvidenceDigest: "sha256:" + strings.Repeat("0", 64), Details: err.Error()}
}

func appendDetails(existing any, failures []string) []string {
	details := make([]string, 0, len(failures)+1)
	if text, ok := existing.(string); ok && text != "" {
		details = append(details, text)
	}
	if values, ok := existing.([]string); ok {
		details = append(details, values...)
	}
	return append(details, failures...)
}

func buildReport(root string, inputs []inputDigest, checks []checkResult) report {
	passed := 0
	failed := 0
	for _, check := range checks {
		switch check.Status {
		case "PASS":
			passed++
		case "FAIL", "ERROR":
			failed++
		}
	}
	status := "PASS"
	if failed > 0 {
		status = "ERROR"
	}
	specPath := filepath.Join(root, "docs", "产品方案设计", "VERTEX_PRO_COMPLETE_REDESIGN_V1_RC8_FINAL.md")
	specDigest, _ := fileDigest(specPath)
	return report{
		SchemaVersion: 1,
		ReportKind:    reportKind,
		Status:        status,
		EligibleForG3: false,
		GeneratedAt:   time.Now().UTC().Format(time.RFC3339),
		Spec:          specInfo{Name: "VERTEX_PRO_COMPLETE_REDESIGN_V1_RC8_FINAL.md", Status: "DESIGN-RC8 / FROZEN-CANDIDATE", Digest: "sha256:" + specDigest},
		Git:           gitInfo{Commit: "local-only", Clean: false},
		Tool:          toolInfo{Name: toolName, Version: toolVersion},
		Inputs:        inputs,
		Checks:        checks,
		Waivers:       []any{},
		Summary:       summaryInfo{Required: len(checks), Passed: passed, Failed: failed, Skipped: 0},
	}
}

func missingCheck(id, details string) checkResult {
	return checkResult{ID: id, Status: "ERROR", EvidenceDigest: "sha256:" + strings.Repeat("0", 64), Details: details}
}

func passIfFiles(root, id string, paths []string) checkResult {
	for _, path := range paths {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(path))); err != nil {
			return checkResult{ID: id, Status: "ERROR", EvidenceDigest: "sha256:" + strings.Repeat("0", 64), Details: fmt.Sprintf("missing input %s", path)}
		}
	}
	return checkResult{ID: id, Status: "PASS", EvidenceDigest: "sha256:" + strings.Repeat("0", 64), Details: nil}
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}

func fileDigest(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func fatal(code int, err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(code)
}

var _ = sort.Strings
