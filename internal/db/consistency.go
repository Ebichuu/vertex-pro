package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

var requiredIndexes = []string{
	"uq_capacity_reservation_attempt",
	"uq_race_job_current_attempt",
	"uq_capacity_task_owner",
	"uq_reservation_reflection_step",
	"uq_job_active_attempt",
	"uq_identity_active_delivery_chain",
	"uq_attempt_active_waiter",
	"ix_profile_verification_runs_binding",
}

var requiredTriggers = []string{
	"submit_intent_attempt_match_guard",
	"submit_intent_relation_immutable_guard",
	"race_job_current_attempt_intent_insert_guard",
	"race_job_current_attempt_intent_update_guard",
	"capacity_reservation_attempt_guard",
	"placement_attempt_target_immutable_guard",
	"capacity_reservation_attempt_match_guard",
	"capacity_reservation_attempt_update_guard",
	"event_stream_revision_monotonic_guard",
	"event_stream_append_only_guard",
	"race_events_update_guard",
	"race_events_delete_guard",
	"system_events_append_only_guard",
	"api_command_identity_immutable_guard",
	"api_command_state_transition_guard",
	"profile_verification_runs_update_guard",
	"profile_verification_runs_delete_guard",
}

// StartupConsistencyReport describes every fail-closed startup violation.
type StartupConsistencyReport struct {
	Violations []string
}

// Error returns a stable diagnostic suitable for logs and evidence.
func (report StartupConsistencyReport) Error() string {
	if len(report.Violations) == 0 {
		return "startup consistency check passed"
	}
	return "startup consistency check failed: " + strings.Join(report.Violations, "; ")
}

// CheckStartupConsistency verifies the persisted schema before workers receive traffic.
func CheckStartupConsistency(ctx context.Context, database *sql.DB) error {
	if database == nil {
		return errors.New("database is required")
	}
	report := StartupConsistencyReport{}
	checkForeignKeysEnabled(ctx, database, &report)
	checkForeignKeys(ctx, database, &report)
	checkRequiredObjects(ctx, database, "index", requiredIndexes, &report)
	checkRequiredObjects(ctx, database, "trigger", requiredTriggers, &report)
	checkQuery(ctx, database, "attempt_submit_intent_cardinality", `
		SELECT a.attempt_id
		FROM placement_attempts a
		LEFT JOIN submit_intents s ON s.attempt_id = a.attempt_id
		GROUP BY a.attempt_id
		HAVING COUNT(s.submit_intent_id) != 1`, &report)
	checkQuery(ctx, database, "current_attempt_link", `
		SELECT r.job_id
		FROM race_jobs r
		JOIN placement_attempts a ON a.attempt_id = r.current_attempt_id
		LEFT JOIN submit_intents s ON s.attempt_id = a.attempt_id
		WHERE r.current_attempt_id IS NOT NULL
		  AND (a.job_id != r.job_id OR s.submit_intent_id IS NULL)`, &report)
	checkQuery(ctx, database, "reservation_attempt_fact_match", `
		SELECT r.reservation_id
		FROM capacity_reservations r
		LEFT JOIN placement_attempts a ON a.attempt_id = r.attempt_id
		WHERE a.attempt_id IS NULL
		   OR a.storage_target_id != r.storage_target_id
		   OR a.allocation_epoch != r.allocation_epoch
		   OR a.reserved_bytes != r.reserved_bytes`, &report)
	checkQuery(ctx, database, "reservation_owner_integrity", `
		SELECT b.binding_id
		FROM capacity_task_bindings b
		LEFT JOIN capacity_reservations r ON r.reservation_id = b.reservation_id
		WHERE b.owner_kind = 'RESERVATION'
		  AND (r.reservation_id IS NULL OR r.storage_target_id != b.storage_target_id OR r.reservation_state NOT IN ('ACTIVE', 'REFLECTING'))`, &report)
	checkQuery(ctx, database, "event_revision_sequence", `
		WITH ordered AS (
			SELECT aggregate_type, aggregate_id, aggregate_revision,
			       LAG(aggregate_revision) OVER (PARTITION BY aggregate_type, aggregate_id ORDER BY aggregate_revision) AS previous_revision
			FROM event_stream
		)
		SELECT aggregate_id
		FROM ordered
		WHERE (previous_revision IS NULL AND aggregate_revision != 0)
		   OR (previous_revision IS NOT NULL AND aggregate_revision != previous_revision + 1)`, &report)
	if len(report.Violations) > 0 {
		return report
	}
	return nil
}

func checkForeignKeysEnabled(ctx context.Context, database *sql.DB, report *StartupConsistencyReport) {
	var enabled int
	if err := database.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&enabled); err != nil {
		report.Violations = append(report.Violations, "foreign_keys pragma query failed")
		return
	}
	if enabled != 1 {
		report.Violations = append(report.Violations, "foreign_keys pragma is disabled")
	}
}

func checkForeignKeys(ctx context.Context, database *sql.DB, report *StartupConsistencyReport) {
	rows, err := database.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		report.Violations = append(report.Violations, "foreign_key_check query failed")
		return
	}
	defer rows.Close()
	var found bool
	for rows.Next() {
		found = true
		var table string
		var rowID any
		var parent string
		var foreignKeyID int
		if err := rows.Scan(&table, &rowID, &parent, &foreignKeyID); err != nil {
			report.Violations = append(report.Violations, "foreign_key_check scan failed")
			return
		}
	}
	if err := rows.Err(); err != nil {
		report.Violations = append(report.Violations, "foreign_key_check iteration failed")
		return
	}
	if found {
		report.Violations = append(report.Violations, "foreign_key_check returned violations")
	}
}

func checkRequiredObjects(ctx context.Context, database *sql.DB, objectType string, names []string, report *StartupConsistencyReport) {
	for _, name := range names {
		var count int
		err := database.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type = ? AND name = ?", objectType, name).Scan(&count)
		if err != nil {
			report.Violations = append(report.Violations, fmt.Sprintf("%s %s lookup failed", objectType, name))
			continue
		}
		if count != 1 {
			report.Violations = append(report.Violations, fmt.Sprintf("missing %s %s", objectType, name))
		}
	}
}

func checkQuery(ctx context.Context, database *sql.DB, name string, query string, report *StartupConsistencyReport) {
	rows, err := database.QueryContext(ctx, query)
	if err != nil {
		report.Violations = append(report.Violations, fmt.Sprintf("%s query failed", name))
		return
	}
	defer rows.Close()
	if rows.Next() {
		report.Violations = append(report.Violations, name+" returned violations")
	}
	if err := rows.Err(); err != nil {
		report.Violations = append(report.Violations, fmt.Sprintf("%s iteration failed", name))
	}
}
