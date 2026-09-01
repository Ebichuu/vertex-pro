package placementaudit

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/vertex-pro/vertex-pro/internal/db"
	"github.com/vertex-pro/vertex-pro/internal/placement"
)

var ErrNotFound = errors.New("placement decision audit not found")

type RecordInput struct {
	AuditID        string
	JobID          string
	SourceID       string
	SiteID         string
	FeedHash       string
	CanonicalHash  string
	OfficialState  string
	OfficialReason string
	TargetMode     string
	Decision       placement.DecisionResult
	AddResultState string
	AddResultCode  string
	CreatedAt      string
	UpdatedAt      string
}

type Audit struct {
	AuditID                 string
	JobID                   string
	SourceID                string
	SiteID                  string
	FeedHash                string
	CanonicalHash           string
	OfficialState           string
	OfficialReason          string
	AlgorithmVersion        string
	TargetMode              string
	SelectedInstanceID      string
	SelectedStorageTargetID string
	SelectedScore           *int64
	DecisionState           string
	CandidateSnapshotJSON   string
	DecisionSummaryJSON     string
	AddResultState          string
	AddResultCode           string
	CreatedAt               string
	UpdatedAt               string
}

type Recorder struct {
	database *sql.DB
}

func New(database *sql.DB) (*Recorder, error) {
	if database == nil {
		return nil, errors.New("database is required")
	}
	return &Recorder{database: database}, nil
}

func (recorder *Recorder) Record(ctx context.Context, input RecordInput) error {
	if recorder == nil || recorder.database == nil {
		return errors.New("placement audit recorder is not initialized")
	}
	if err := validateInput(input); err != nil {
		return err
	}
	candidateSnapshot, decisionSummary, err := marshalDecision(input.Decision)
	if err != nil {
		return err
	}
	selectedInstanceID, selectedStorageTargetID, selectedScore, decisionState := selectedValues(input.Decision)
	var canonicalHash any
	if strings.TrimSpace(input.CanonicalHash) != "" {
		canonicalHash = strings.TrimSpace(input.CanonicalHash)
	}
	var jobID any
	if strings.TrimSpace(input.JobID) != "" {
		jobID = strings.TrimSpace(input.JobID)
	}
	return db.RunImmediate(ctx, recorder.database, func(ctx context.Context, transaction *db.ImmediateTx) error {
		_, err := transaction.ExecContext(ctx, `INSERT INTO placement_decision_audits(
			audit_id, job_id, source_id, site_id, feed_hash, canonical_hash,
			official_state, official_reason, algorithm_version, target_mode,
			selected_instance_id, selected_storage_target_id, selected_score,
			decision_state, candidate_snapshot_json, decision_summary_json,
			add_result_state, add_result_code, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			strings.TrimSpace(input.AuditID), jobID, strings.TrimSpace(input.SourceID), strings.TrimSpace(input.SiteID), strings.TrimSpace(input.FeedHash), canonicalHash,
			strings.TrimSpace(input.OfficialState), strings.TrimSpace(input.OfficialReason), input.Decision.AlgorithmVersion, strings.TrimSpace(input.TargetMode),
			selectedInstanceID, selectedStorageTargetID, selectedScore,
			decisionState, candidateSnapshot, decisionSummary,
			strings.TrimSpace(input.AddResultState), nullableString(input.AddResultCode), strings.TrimSpace(input.CreatedAt), strings.TrimSpace(input.UpdatedAt),
		)
		if err != nil {
			return fmt.Errorf("insert placement decision audit: %w", err)
		}
		return nil
	})
}

func (recorder *Recorder) Get(ctx context.Context, auditID string) (Audit, error) {
	if recorder == nil || recorder.database == nil {
		return Audit{}, errors.New("placement audit recorder is not initialized")
	}
	auditID = strings.TrimSpace(auditID)
	if auditID == "" {
		return Audit{}, errors.New("audit ID is required")
	}
	var audit Audit
	var jobID, canonicalHash, selectedInstanceID, selectedStorageTargetID, addResultCode sql.NullString
	var selectedScore sql.NullInt64
	err := recorder.database.QueryRowContext(ctx, `SELECT
		audit_id, job_id, source_id, site_id, feed_hash, canonical_hash,
		official_state, official_reason, algorithm_version, target_mode,
		selected_instance_id, selected_storage_target_id, selected_score,
		decision_state, candidate_snapshot_json, decision_summary_json,
		add_result_state, add_result_code, created_at, updated_at
		FROM placement_decision_audits WHERE audit_id = ?`, auditID).Scan(
		&audit.AuditID, &jobID, &audit.SourceID, &audit.SiteID, &audit.FeedHash, &canonicalHash,
		&audit.OfficialState, &audit.OfficialReason, &audit.AlgorithmVersion, &audit.TargetMode,
		&selectedInstanceID, &selectedStorageTargetID, &selectedScore,
		&audit.DecisionState, &audit.CandidateSnapshotJSON, &audit.DecisionSummaryJSON,
		&audit.AddResultState, &addResultCode, &audit.CreatedAt, &audit.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Audit{}, ErrNotFound
	}
	if err != nil {
		return Audit{}, fmt.Errorf("read placement decision audit: %w", err)
	}
	audit.JobID = jobID.String
	audit.CanonicalHash = canonicalHash.String
	audit.SelectedInstanceID = selectedInstanceID.String
	audit.SelectedStorageTargetID = selectedStorageTargetID.String
	audit.AddResultCode = addResultCode.String
	if selectedScore.Valid {
		value := selectedScore.Int64
		audit.SelectedScore = &value
	}
	return audit, nil
}

func validateInput(input RecordInput) error {
	for name, value := range map[string]string{
		"audit ID":        input.AuditID,
		"source ID":       input.SourceID,
		"site ID":         input.SiteID,
		"feed hash":       input.FeedHash,
		"official reason": input.OfficialReason,
		"created at":      input.CreatedAt,
		"updated at":      input.UpdatedAt,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	if input.OfficialState != "OFFICIAL" && input.OfficialState != "NON_OFFICIAL" && input.OfficialState != "UNKNOWN" {
		return errors.New("official state is invalid")
	}
	if input.TargetMode != placement.TargetFixedInstance && input.TargetMode != placement.TargetInstanceGroup {
		return errors.New("target mode is invalid")
	}
	if input.Decision.AlgorithmVersion == "" {
		return errors.New("decision algorithm version is required")
	}
	if input.AddResultState == "" {
		return errors.New("add result state is required")
	}
	if input.AddResultState != "NOT_ATTEMPTED" && input.AddResultState != "SUCCEEDED" && input.AddResultState != "FAILED" && input.AddResultState != "UNKNOWN" {
		return errors.New("add result state is invalid")
	}
	if input.AddResultState != "NOT_ATTEMPTED" && strings.TrimSpace(input.AddResultCode) == "" {
		return errors.New("add result code is required")
	}
	if input.AddResultState == "NOT_ATTEMPTED" && strings.TrimSpace(input.AddResultCode) != "" {
		return errors.New("add result code is not allowed before add")
	}
	return nil
}

func selectedValues(decision placement.DecisionResult) (any, any, any, string) {
	if decision.Selected == nil {
		return nil, nil, nil, "NO_ELIGIBLE"
	}
	return decision.Selected.Candidate.InstanceID, decision.Selected.Candidate.StorageTargetID, decision.Selected.Score, "SELECTED"
}

type auditCandidate struct {
	Candidate placement.DecisionCandidate `json:"candidate"`
	Score     *int64                      `json:"score,omitempty"`
	Breakdown *placement.ScoreBreakdown   `json:"breakdown,omitempty"`
	TieBreak  *uint64                     `json:"tie_break,omitempty"`
	Reason    string                      `json:"reason,omitempty"`
}

type auditDecision struct {
	AlgorithmVersion string   `json:"algorithm_version"`
	RankedInstances  []string `json:"ranked_instances"`
	SelectedInstance string   `json:"selected_instance,omitempty"`
	RejectedCount    int      `json:"rejected_count"`
}

func marshalDecision(decision placement.DecisionResult) (string, string, error) {
	candidates := make([]auditCandidate, 0, len(decision.Ranked)+len(decision.Rejected))
	for _, scored := range decision.Ranked {
		score, breakdown, tieBreak := scored.Score, scored.Breakdown, scored.TieBreak
		candidates = append(candidates, auditCandidate{Candidate: scored.Candidate, Score: &score, Breakdown: &breakdown, TieBreak: &tieBreak})
	}
	for _, rejected := range decision.Rejected {
		candidates = append(candidates, auditCandidate{Candidate: rejected.Candidate, Reason: rejected.Reason})
	}
	candidateJSON, err := json.Marshal(struct {
		Candidates []auditCandidate `json:"candidates"`
	}{Candidates: candidates})
	if err != nil {
		return "", "", fmt.Errorf("marshal candidate snapshot: %w", err)
	}
	rankedInstances := make([]string, 0, len(decision.Ranked))
	for _, scored := range decision.Ranked {
		rankedInstances = append(rankedInstances, scored.Candidate.InstanceID)
	}
	summary := auditDecision{AlgorithmVersion: decision.AlgorithmVersion, RankedInstances: rankedInstances, RejectedCount: len(decision.Rejected)}
	if decision.Selected != nil {
		summary.SelectedInstance = decision.Selected.Candidate.InstanceID
	}
	decisionJSON, err := json.Marshal(summary)
	if err != nil {
		return "", "", fmt.Errorf("marshal decision summary: %w", err)
	}
	return string(candidateJSON), string(decisionJSON), nil
}

func nullableString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return strings.TrimSpace(value)
}
