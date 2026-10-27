package deletionsync

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
	"github.com/vertex-pro/vertex-pro/internal/qui/control"
)

// ActivityReader lists qui's automation activity for an instance.
type ActivityReader interface {
	Activity(context.Context, string, int) ([]control.Activity, error)
}

// Audit sources.
const (
	SourcePolicy     = "policy"
	SourceLegacyRule = "legacy_rule"
	SourceExternal   = "external"
)

// AuditEntry is one imported deletion.
type AuditEntry struct {
	Instance       string
	ActivityID     int
	Hash           string
	TorrentName    string
	TrackerDomain  string
	Action         string
	Outcome        string
	Reason         string
	QuiRuleID      *int
	QuiRuleName    string
	Source         string
	PolicyID       string
	PolicyRole     string
	PolicyRevision int
	VertexRuleID   string
	JobID          string
	Details        json.RawMessage
	OccurredAt     time.Time
}

// ImportResult summarizes one import pass.
type ImportResult struct {
	Fetched  int
	Imported int
	// Gap reports that a full page contained nothing at or below the highest
	// ID previously seen: older entries may have scrolled out of qui's list.
	Gap bool
}

// AuditImporter copies qui deletion activity into deletion_audit.
type AuditImporter struct {
	Store    Store
	Client   ActivityReader
	Instance string
	PageSize int
}

// Import fetches the newest page and stores every real deletion attempt
// (successful or failed). Dry-runs and non-deletion actions are skipped.
// Re-importing is idempotent: rows are keyed by qui's activity ID.
func (importer AuditImporter) Import(ctx context.Context) (ImportResult, error) {
	limit := importer.PageSize
	if limit <= 0 || limit > control.MaxActivityLimit {
		limit = control.MaxActivityLimit
	}
	activities, err := importer.Client.Activity(ctx, importer.Instance, limit)
	if err != nil {
		return ImportResult{}, err
	}
	result := ImportResult{Fetched: len(activities)}
	previousMax, err := importer.Store.auditCursor(ctx, importer.Instance)
	if err != nil {
		return result, err
	}
	pageMin, pageMax := 0, previousMax
	for _, activity := range activities {
		if pageMin == 0 || activity.ID < pageMin {
			pageMin = activity.ID
		}
		pageMax = max(pageMax, activity.ID)
	}
	// IDs are global across instances, so one instance's IDs are not
	// consecutive: only "a full page that never reached the last seen ID"
	// proves older entries may have been missed.
	result.Gap = previousMax > 0 && len(activities) == limit && pageMin > previousMax

	err = db.RunImmediate(ctx, importer.Store.DB, func(ctx context.Context, tx *db.ImmediateTx) error {
		for _, activity := range activities {
			if !activity.IsDeletion() || activity.IsDryRun() || (activity.Outcome != control.OutcomeSuccess && activity.Outcome != control.OutcomeFailed) {
				continue
			}
			entry, err := importer.entry(ctx, tx, activity)
			if err != nil {
				return err
			}
			inserted, err := insertAudit(ctx, tx, entry, importer.Store.stamp())
			if err != nil {
				return err
			}
			if inserted {
				result.Imported++
			}
		}
		var gapAt any
		if result.Gap {
			gapAt = importer.Store.stamp()
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO deletion_audit_cursor(instance_id,max_activity_id,last_import_at,gap_detected_at) VALUES(?,?,?,?)
			ON CONFLICT(instance_id) DO UPDATE SET max_activity_id=max(max_activity_id,excluded.max_activity_id),last_import_at=excluded.last_import_at,gap_detected_at=COALESCE(excluded.gap_detected_at,gap_detected_at)`,
			importer.Instance, pageMax, importer.Store.stamp(), gapAt)
		return err
	})
	return result, err
}

// entry classifies the deleting rule and links the job that delivered the
// torrent on this instance, if Vertex delivered it.
func (importer AuditImporter) entry(ctx context.Context, tx *db.ImmediateTx, activity control.Activity) (AuditEntry, error) {
	entry := AuditEntry{
		Instance: importer.Instance, ActivityID: activity.ID, Hash: strings.ToLower(strings.TrimSpace(activity.Hash)),
		TorrentName: activity.TorrentName, TrackerDomain: activity.TrackerDomain, Action: activity.Action, Outcome: activity.Outcome,
		Reason: activity.Reason, QuiRuleID: activity.RuleID, QuiRuleName: activity.RuleName, Source: SourceExternal,
		Details: activity.Details, OccurredAt: activity.CreatedAt,
	}
	if activity.RuleID != nil {
		var policyID, role string
		var revision int
		err := tx.QueryRowContext(ctx, `SELECT policy_id,role,policy_revision FROM deletion_policy_bindings WHERE instance_id=? AND qui_id=?`, importer.Instance, *activity.RuleID).Scan(&policyID, &role, &revision)
		switch {
		case err == nil:
			entry.Source, entry.PolicyID, entry.PolicyRole, entry.PolicyRevision = SourcePolicy, policyID, role, revision
		case !errors.Is(err, sql.ErrNoRows):
			return entry, err
		default:
			var ruleID string
			err := tx.QueryRowContext(ctx, `SELECT rule_id FROM qui_policy_bindings WHERE instance_id=? AND qui_id=? ORDER BY rule_revision DESC LIMIT 1`, importer.Instance, *activity.RuleID).Scan(&ruleID)
			if err == nil {
				entry.Source, entry.VertexRuleID = SourceLegacyRule, ruleID
			} else if !errors.Is(err, sql.ErrNoRows) {
				return entry, err
			}
		}
	}
	if entry.Hash != "" {
		err := tx.QueryRowContext(ctx, `SELECT job_id FROM placement_attempts WHERE instance_id=? AND qb_torrent_id=? ORDER BY started_at DESC, attempt_id DESC LIMIT 1`, importer.Instance, entry.Hash).Scan(&entry.JobID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return entry, err
		}
	}
	return entry, nil
}

func insertAudit(ctx context.Context, tx *db.ImmediateTx, entry AuditEntry, importedAt string) (bool, error) {
	details := string(entry.Details)
	if len(entry.Details) == 0 || !json.Valid(entry.Details) {
		details = "{}"
	}
	result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO deletion_audit(instance_id,qui_activity_id,hash,torrent_name,tracker_domain,action,outcome,reason,qui_rule_id,qui_rule_name,source,policy_id,policy_role,policy_revision,vertex_rule_id,job_id,details_json,occurred_at,imported_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		entry.Instance, entry.ActivityID, entry.Hash, entry.TorrentName, entry.TrackerDomain, entry.Action, entry.Outcome, entry.Reason,
		entry.QuiRuleID, entry.QuiRuleName, entry.Source, nullable(entry.PolicyID), nullable(entry.PolicyRole), nullableInt(entry.PolicyRevision),
		nullable(entry.VertexRuleID), nullable(entry.JobID), details, entry.OccurredAt.UTC().Format(time.RFC3339Nano), importedAt)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count == 1, err
}

func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableInt(value int) any {
	if value == 0 {
		return nil
	}
	return value
}

func (store Store) auditCursor(ctx context.Context, instance string) (int, error) {
	var value int
	err := store.DB.QueryRowContext(ctx, `SELECT max_activity_id FROM deletion_audit_cursor WHERE instance_id=?`, instance).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return value, err
}

// HasAuditWork reports whether qui may hold Vertex-relevant deletions: any
// deletion policy, deletion binding or legacy per-rule policy binding.
func (store Store) HasAuditWork(ctx context.Context) (bool, error) {
	var count int
	err := store.DB.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM deletion_policies) + (SELECT count(*) FROM deletion_policy_bindings) + (SELECT count(*) FROM qui_policy_bindings)`).Scan(&count)
	return count > 0, err
}

const auditColumns = `instance_id,qui_activity_id,hash,torrent_name,tracker_domain,action,outcome,reason,qui_rule_id,qui_rule_name,source,COALESCE(policy_id,''),COALESCE(policy_role,''),COALESCE(policy_revision,0),COALESCE(vertex_rule_id,''),COALESCE(job_id,''),details_json,occurred_at`

func scanAudit(rows *sql.Rows) ([]AuditEntry, error) {
	defer rows.Close()
	var entries []AuditEntry
	for rows.Next() {
		var entry AuditEntry
		var ruleID sql.NullInt64
		var details, occurred string
		if err := rows.Scan(&entry.Instance, &entry.ActivityID, &entry.Hash, &entry.TorrentName, &entry.TrackerDomain, &entry.Action, &entry.Outcome, &entry.Reason, &ruleID, &entry.QuiRuleName, &entry.Source, &entry.PolicyID, &entry.PolicyRole, &entry.PolicyRevision, &entry.VertexRuleID, &entry.JobID, &details, &occurred); err != nil {
			return nil, err
		}
		if ruleID.Valid {
			value := int(ruleID.Int64)
			entry.QuiRuleID = &value
		}
		entry.Details = json.RawMessage(details)
		entry.OccurredAt, _ = time.Parse(time.RFC3339Nano, occurred)
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

// AuditForJob lists deletions linked to a delivery job, newest first.
func (store Store) AuditForJob(ctx context.Context, jobID string) ([]AuditEntry, error) {
	rows, err := store.DB.QueryContext(ctx, `SELECT `+auditColumns+` FROM deletion_audit WHERE job_id=? ORDER BY occurred_at DESC, qui_activity_id DESC`, jobID)
	if err != nil {
		return nil, err
	}
	return scanAudit(rows)
}

// Audit lists one instance's deletions, newest first by qui activity ID;
// beforeID > 0 pages to older entries.
func (store Store) Audit(ctx context.Context, instance string, beforeID, limit int) ([]AuditEntry, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	query := `SELECT ` + auditColumns + ` FROM deletion_audit WHERE instance_id=?`
	args := []any{instance}
	if beforeID > 0 {
		query += ` AND qui_activity_id<?`
		args = append(args, beforeID)
	}
	rows, err := store.DB.QueryContext(ctx, query+` ORDER BY qui_activity_id DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	return scanAudit(rows)
}

// AuditGap returns when a possible gap in imported activity was detected.
func (store Store) AuditGap(ctx context.Context, instance string) (time.Time, bool, error) {
	var raw sql.NullString
	err := store.DB.QueryRowContext(ctx, `SELECT gap_detected_at FROM deletion_audit_cursor WHERE instance_id=?`, instance).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) || err == nil && !raw.Valid {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	at, err := time.Parse(time.RFC3339Nano, raw.String)
	return at, err == nil, err
}
