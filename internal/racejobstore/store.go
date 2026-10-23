// Package racejobstore persists racejob jobs in the race_jobs table. It backs
// the race job creator with the production SQLite database so discovery is
// deduplicated across restarts, mirroring the in-memory store semantics.
package racejobstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
	"github.com/vertex-pro/vertex-pro/internal/racejob"
)

// Store implements racejob.Store on top of SQLite.
type Store struct {
	database *sql.DB
}

// New wraps a database. The schema is owned by the migration runner.
func New(database *sql.DB) (*Store, error) {
	if database == nil {
		return nil, errors.New("database is required")
	}
	return &Store{database: database}, nil
}

// LoadBySource returns the job created for a feed source, if any.
func (store *Store) LoadBySource(ctx context.Context, sourceID string) (racejob.Job, bool, error) {
	var job racejob.Job
	var identityKey, currentAttempt sql.NullString
	var participation, observation, createdAt, updatedAt string
	err := store.database.QueryRowContext(ctx, `SELECT job_id, source_id, identity_key, job_state, rule_state, torrent_state, placement_state, observation_state, preparation_revision, current_attempt_id, participation_deadline, observation_deadline, created_at, updated_at FROM race_jobs WHERE source_id=?`, sourceID).Scan(
		&job.ID, &job.SourceID, &identityKey, &job.JobState, &job.RuleState, &job.TorrentState, &job.PlacementState, &job.ObservationState, &job.PreparationRevision, &currentAttempt, &participation, &observation, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return racejob.Job{}, false, nil
	}
	if err != nil {
		return racejob.Job{}, false, fmt.Errorf("load race job by source: %w", err)
	}
	job.IdentityKey = identityKey.String
	if currentAttempt.Valid {
		value := currentAttempt.String
		job.CurrentAttemptID = &value
	}
	if err := parseTime(participation, &job.ParticipationDeadline); err != nil {
		return racejob.Job{}, false, err
	}
	if err := parseTime(observation, &job.ObservationDeadline); err != nil {
		return racejob.Job{}, false, err
	}
	if err := parseTime(createdAt, &job.CreatedAt); err != nil {
		return racejob.Job{}, false, err
	}
	if err := parseTime(updatedAt, &job.UpdatedAt); err != nil {
		return racejob.Job{}, false, err
	}
	return job, true, nil
}

// Save inserts a new job or updates the mutable state of an existing one.
// Immutable identity columns (job_id, source_id, identity_key) are only set on
// insert; updates never rewrite them.
func (store *Store) Save(ctx context.Context, job racejob.Job) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	currentAttempt := nullablePointer(job.CurrentAttemptID)
	return db.RunImmediate(ctx, store.database, func(ctx context.Context, tx *db.ImmediateTx) error {
		result, err := tx.ExecContext(ctx, `INSERT INTO race_jobs(job_id, source_id, identity_key, job_state, rule_state, torrent_state, placement_state, observation_state, preparation_revision, current_attempt_id, participation_deadline, observation_deadline, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(job_id) DO UPDATE SET job_state=excluded.job_state, rule_state=excluded.rule_state, torrent_state=excluded.torrent_state, placement_state=excluded.placement_state, observation_state=excluded.observation_state, preparation_revision=excluded.preparation_revision, current_attempt_id=excluded.current_attempt_id, updated_at=excluded.updated_at`,
			job.ID, job.SourceID, job.IdentityKey, job.JobState, job.RuleState, job.TorrentState, job.PlacementState, job.ObservationState, job.PreparationRevision, currentAttempt, formatTime(job.ParticipationDeadline), formatTime(job.ObservationDeadline), now, now)
		if err != nil {
			return fmt.Errorf("save race job: %w", err)
		}
		if rows, err := result.RowsAffected(); err == nil && rows == 0 {
			return fmt.Errorf("save race job: no rows affected")
		}
		if len(job.RuleSnapshot) > 0 {
			if _, err := tx.ExecContext(ctx, `INSERT INTO job_rule_snapshots(job_id, aggregation_json, content_size, created_at) VALUES(?,?,?,?) ON CONFLICT(job_id) DO NOTHING`, job.ID, string(job.RuleSnapshot), job.ContentSize, now); err != nil {
				return fmt.Errorf("save rule snapshot: %w", err)
			}
		}
		return nil
	})
}

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339)
}

func parseTime(raw string, target *time.Time) error {
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return fmt.Errorf("parse race job timestamp %q: %w", raw, err)
	}
	*target = parsed.UTC()
	return nil
}

func nullablePointer(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}
