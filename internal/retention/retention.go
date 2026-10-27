// Package retention implements the OBS-03 cleanup policy: batched deletes in
// per-batch immediate transactions so no sweep holds a long write lock. The
// sweep enforces the expires_at facts the writers persist (RC8: durable events
// 7d, terminal commands 7d, idempotency 24h, stats raw 30d / hourly 180d) and
// removes completed sync snapshots older than the snapshot retention while
// always keeping the latest revision of every instance.
package retention

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
	"github.com/vertex-pro/vertex-pro/internal/statsstore"
)

// Policy carries the tunable knobs; the defaults mirror the RC8 retention
// contract block. Snapshot retention is the OBS-03 policy decision: completed
// snapshots older than the window are removed unless they are the newest
// revision of their instance.
type Policy struct {
	BatchSize         int
	SnapshotRetention time.Duration
	StatsRawRetention time.Duration
	StatsHourly       time.Duration
}

func DefaultPolicy() Policy {
	return Policy{
		BatchSize:         500,
		SnapshotRetention: 7 * 24 * time.Hour,
		StatsRawRetention: 30 * 24 * time.Hour,
		StatsHourly:       180 * 24 * time.Hour,
	}
}

func (policy Policy) batchSize() int {
	if policy.BatchSize <= 0 {
		return 500
	}
	return policy.BatchSize
}

// Result reports what one sweep removed. SnapshotFileFailures lists relative
// paths whose file removal failed after their rows were committed; rows are
// already gone and the next sweep retries the file only if still unreferenced.
type Result struct {
	Events               int64
	SystemEvents         int64
	Commands             int64
	IdempotencyRecords   int64
	Snapshots            int64
	ClosedWaiters        int64
	TorrentArtifacts     int64
	SnapshotFileFailures []string
	StatsRaw             int64
	StatsHourly          int64
}

// Sweep runs one full cleanup pass. now is injected so tests and callers use
// a deterministic clock.
func Sweep(ctx context.Context, database *sql.DB, snapshotRoot string, policy Policy, now time.Time) (Result, error) {
	if database == nil {
		return Result{}, errors.New("database is required")
	}
	if now.IsZero() {
		return Result{}, errors.New("now is required")
	}
	result := Result{}
	cutoff := now.UTC().Format(time.RFC3339)

	events, err := deleteExpiredBatched(ctx, database, "event_stream", "event_id", cutoff, policy.batchSize())
	if err != nil {
		return result, fmt.Errorf("prune expired events: %w", err)
	}
	result.Events = events

	systemEvents, err := deleteExpiredBatched(ctx, database, "system_events", "system_event_id", cutoff, policy.batchSize())
	if err != nil {
		return result, fmt.Errorf("prune expired system events: %w", err)
	}
	result.SystemEvents = systemEvents

	idempotency, err := deleteExpiredBatched(ctx, database, "api_idempotency_records", "idempotency_key", cutoff, policy.batchSize())
	if err != nil {
		return result, fmt.Errorf("prune expired idempotency records: %w", err)
	}
	result.IdempotencyRecords = idempotency

	commands, err := deleteExpiredCommands(ctx, database, cutoff, policy.batchSize())
	if err != nil {
		return result, fmt.Errorf("prune expired commands: %w", err)
	}
	result.Commands = commands

	var snapshotErr error
	if snapshotRoot != "" {
		closed, err := deleteTerminalWaiters(ctx, database, now.UTC().Add(-policy.SnapshotRetention).Format(time.RFC3339Nano), policy.batchSize())
		result.ClosedWaiters = closed
		if err != nil {
			snapshotErr = fmt.Errorf("prune terminal waiters: %w", err)
		}
		if snapshotErr == nil {
			snapshots, failures, err := removeExpiredSnapshots(ctx, database, snapshotRoot, now.UTC().Add(-policy.SnapshotRetention).Format(time.RFC3339Nano), policy.batchSize())
			if err != nil {
				snapshotErr = fmt.Errorf("prune expired snapshots: %w", err)
			}
			result.Snapshots = snapshots
			result.SnapshotFileFailures = failures
		}
	}

	stats, err := statsstore.Prune(ctx, database, now, statsstore.Retention{RawSamples: policy.StatsRawRetention, Hourly: policy.StatsHourly})
	if err != nil {
		return result, errors.Join(snapshotErr, fmt.Errorf("prune stats: %w", err))
	}
	result.StatsRaw = stats.RawSamples
	result.StatsHourly = stats.Hourly
	artifacts, err := pruneTorrentArtifacts(ctx, database, now.Add(-7*24*time.Hour), policy.batchSize())
	result.TorrentArtifacts = artifacts
	if err != nil {
		return result, errors.Join(snapshotErr, fmt.Errorf("prune torrent artifacts: %w", err))
	}
	return result, snapshotErr
}

// Keep reusable artifacts for all unresolved jobs. Only settled jobs older than
// the diagnostic window are pruned; UNKNOWN and failed/retryable chains remain.
func pruneTorrentArtifacts(ctx context.Context, database *sql.DB, cutoff time.Time, batchSize int) (int64, error) {
	var total int64
	for {
		var count int64
		err := db.RunImmediate(ctx, database, func(ctx context.Context, tx *db.ImmediateTx) error {
			result, err := tx.ExecContext(ctx, `DELETE FROM job_torrent_artifacts WHERE job_id IN (
			 SELECT a.job_id FROM job_torrent_artifacts a JOIN race_jobs j ON j.job_id=a.job_id
			 WHERE (j.job_state IN ('EXPIRED','HASH_SEEN','RECONCILED_CONFIRMED','ALREADY_PRESENT')
			 OR (j.job_state='RECONCILED_NO_EFFECT' AND unixepoch(j.participation_deadline)<unixepoch(?)))
			 AND unixepoch(j.updated_at)<unixepoch(?)
			 AND NOT EXISTS(SELECT 1 FROM placement_attempts p JOIN capacity_reservations r ON r.attempt_id=p.attempt_id WHERE p.job_id=j.job_id AND r.reservation_state!='RELEASED')
			 ORDER BY a.job_id LIMIT ?)`, cutoff.UTC().Format(time.RFC3339Nano), cutoff.UTC().Format(time.RFC3339Nano), batchSize)
			if err != nil {
				return err
			}
			count, err = result.RowsAffected()
			return err
		})
		if err != nil {
			return total, err
		}
		total += count
		if count < int64(batchSize) {
			return total, nil
		}
	}
}

// A closed notification is not necessarily a confirmed delivery. Keep its
// baseline until the attempt is terminal, all debt has been released, and the
// diagnostic retention period has elapsed. Missing/corrupt chains fail closed.
func deleteTerminalWaiters(ctx context.Context, database *sql.DB, cutoff string, batchSize int) (int64, error) {
	var total int64
	for {
		var count int64
		err := db.RunImmediate(ctx, database, func(ctx context.Context, tx *db.ImmediateTx) error {
			result, err := tx.ExecContext(ctx, `DELETE FROM sync_waiters WHERE waiter_id IN (
				SELECT w.waiter_id FROM sync_waiters w JOIN placement_attempts a ON a.attempt_id=w.attempt_id
				WHERE w.waiter_state='CLOSED' AND unixepoch(w.closed_at)<unixepoch(?)
				AND a.attempt_state IN ('ADD_FAILED','ALREADY_PRESENT','RECONCILED_NO_EFFECT','HASH_SEEN','RECONCILED_CONFIRMED')
				AND EXISTS (SELECT 1 FROM capacity_reservations r WHERE r.attempt_id=a.attempt_id AND r.reservation_state='RELEASED')
				AND NOT EXISTS (SELECT 1 FROM capacity_reservations r WHERE r.attempt_id=a.attempt_id AND r.reservation_state!='RELEASED')
				AND NOT EXISTS (SELECT 1 FROM capacity_task_bindings b WHERE
					(b.storage_target_id=a.storage_target_id AND (b.identity_key=a.identity_key OR b.qb_torrent_id=a.qb_torrent_id))
					OR b.reservation_id IN (SELECT r.reservation_id FROM capacity_reservations r WHERE r.attempt_id=a.attempt_id))
				ORDER BY w.waiter_id LIMIT ?)`, cutoff, batchSize)
			if err != nil {
				return err
			}
			count, err = result.RowsAffected()
			return err
		})
		if err != nil {
			return total, err
		}
		total += count
		if count < int64(batchSize) {
			return total, nil
		}
	}
}

// deleteExpiredBatched removes rows whose expires_at has passed in LIMIT-ed
// chunks, one immediate transaction per chunk.
func deleteExpiredBatched(ctx context.Context, database *sql.DB, table, idColumn, cutoff string, batchSize int) (int64, error) {
	total := int64(0)
	for {
		deleted := int64(0)
		err := db.RunImmediate(ctx, database, func(ctx context.Context, transaction *db.ImmediateTx) error {
			result, err := transaction.ExecContext(ctx, `DELETE FROM `+table+` WHERE `+idColumn+` IN (SELECT `+idColumn+` FROM `+table+` WHERE expires_at <= ? ORDER BY `+idColumn+` LIMIT ?)`, cutoff, batchSize)
			if err != nil {
				return err
			}
			deleted, err = result.RowsAffected()
			return err
		})
		if err != nil {
			return total, err
		}
		total += deleted
		if deleted < int64(batchSize) {
			return total, nil
		}
	}
}

// deleteExpiredCommands removes terminal commands whose retention has passed.
// Non-terminal commands are never swept: an expired RUNNING command is either
// in flight or a writer bug, and startup consistency owns that judgment. Each
// batch first drops the idempotency records referencing the batch (RESTRICT
// foreign key), then the commands, in one transaction.
func deleteExpiredCommands(ctx context.Context, database *sql.DB, cutoff string, batchSize int) (int64, error) {
	total := int64(0)
	for {
		stop := false
		deleted := int64(0)
		err := db.RunImmediate(ctx, database, func(ctx context.Context, transaction *db.ImmediateTx) error {
			rows, err := transaction.QueryContext(ctx, `SELECT command_id FROM api_commands WHERE expires_at <= ? AND command_status IN ('SUCCEEDED', 'FAILED') ORDER BY created_at, command_id LIMIT ?`, cutoff, batchSize)
			if err != nil {
				return err
			}
			ids := make([]string, 0, batchSize)
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err != nil {
					rows.Close()
					return err
				}
				ids = append(ids, id)
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				return err
			}
			rows.Close()
			if len(ids) == 0 {
				stop = true
				return nil
			}
			placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
			args := make([]any, len(ids))
			for index, id := range ids {
				args[index] = id
			}
			if _, err := transaction.ExecContext(ctx, `DELETE FROM api_idempotency_records WHERE command_id IN (`+placeholders+`)`, args...); err != nil {
				return fmt.Errorf("drop idempotency records of expired commands: %w", err)
			}
			result, err := transaction.ExecContext(ctx, `DELETE FROM api_commands WHERE command_id IN (`+placeholders+`)`, args...)
			if err != nil {
				return err
			}
			deleted, err = result.RowsAffected()
			return err
		})
		if err != nil {
			return total, err
		}
		total += deleted
		if stop || deleted < int64(batchSize) {
			return total, nil
		}
	}
}

// removeExpiredSnapshots deletes completed snapshot rows older than the cutoff
// while keeping each instance's newest revision (by generation, then
// revision). Referenced files are never touched; files whose rows are gone
// and that no other row references are removed from disk.
func removeExpiredSnapshots(ctx context.Context, database *sql.DB, root, cutoff string, batchSize int) (int64, []string, error) {
	total := int64(0)
	failures := []string{}
	removedPaths := make([]string, 0, batchSize)
	var sweepErr error
	for {
		deleted := int64(0)
		batchPaths := make([]string, 0, batchSize)
		err := db.RunImmediate(ctx, database, func(ctx context.Context, transaction *db.ImmediateTx) error {
			rows, err := transaction.QueryContext(ctx, `SELECT s.rowid, s.snapshot_path FROM sync_snapshots s
				WHERE s.is_complete = 1 AND s.created_at < ? AND s.rowid NOT IN (
					SELECT newest.rowid FROM sync_snapshots newest
					WHERE newest.rowid = (
						SELECT k.rowid FROM sync_snapshots k
						WHERE k.instance_id = s.instance_id
						ORDER BY k.sync_generation DESC, k.snapshot_revision DESC, k.rowid DESC
						LIMIT 1
					)
				)
				AND NOT EXISTS (SELECT 1 FROM sync_waiters w WHERE w.instance_id=s.instance_id AND w.registered_generation=s.sync_generation AND w.source_snapshot_revision=s.snapshot_revision)
				AND NOT EXISTS (
					SELECT 1 FROM pending_confirmations p JOIN placement_attempts a ON a.attempt_id=p.attempt_id
					WHERE a.instance_id=s.instance_id
					AND json_extract(p.evidence_json,'$.SyncGeneration')=s.sync_generation
					AND json_extract(p.evidence_json,'$.SnapshotRevision')=s.snapshot_revision
				)
				AND NOT EXISTS (
					SELECT 1 FROM submit_intents i JOIN placement_attempts a ON a.attempt_id=i.attempt_id
					WHERE i.instance_id=s.instance_id AND i.pre_add_sync_generation=s.sync_generation AND i.pre_add_snapshot_revision=s.snapshot_revision
					AND (a.attempt_state IN ('RESERVED','ADD_SENT','AWAITING_CONFIRMATION','UNKNOWN','EXPIRED_UNKNOWN')
					OR EXISTS (SELECT 1 FROM capacity_reservations r WHERE r.attempt_id=a.attempt_id AND r.reservation_state!='RELEASED')
					OR EXISTS (SELECT 1 FROM capacity_task_bindings b WHERE b.storage_target_id=a.storage_target_id AND (b.identity_key=a.identity_key OR b.qb_torrent_id=a.qb_torrent_id)))
				)
				ORDER BY s.rowid LIMIT ?`, cutoff, batchSize)
			if err != nil {
				return err
			}
			type candidate struct {
				rowid int64
				path  string
			}
			candidates := make([]candidate, 0, batchSize)
			for rows.Next() {
				var item candidate
				if err := rows.Scan(&item.rowid, &item.path); err != nil {
					rows.Close()
					return err
				}
				candidates = append(candidates, item)
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				return err
			}
			rows.Close()
			if len(candidates) == 0 {
				return nil
			}
			for _, item := range candidates {
				if _, err := transaction.ExecContext(ctx, `DELETE FROM sync_snapshots WHERE rowid = ?`, item.rowid); err != nil {
					return err
				}
				batchPaths = append(batchPaths, item.path)
			}
			deleted = int64(len(candidates))
			return nil
		})
		if err != nil {
			sweepErr = err
			break
		}
		total += deleted
		removedPaths = append(removedPaths, batchPaths...)
		if deleted < int64(batchSize) {
			break
		}
	}
	for _, relative := range removedPaths {
		// Memory-only revisions (snapshot.Store.Commit) have no file.
		if relative == "" {
			continue
		}
		absolute, ok := safeJoin(root, relative)
		if !ok {
			failures = append(failures, relative)
			continue
		}
		var references int
		if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM sync_snapshots WHERE snapshot_path = ?`, relative).Scan(&references); err != nil {
			failures = append(failures, relative)
			continue
		}
		if references > 0 {
			continue
		}
		if err := os.Remove(absolute); err != nil && !errors.Is(err, os.ErrNotExist) {
			failures = append(failures, relative)
		}
	}
	return total, failures, sweepErr
}

// safeJoin mirrors the snapshot store's path containment rules: the cleaned
// path must stay inside the snapshot root.
func safeJoin(root, relative string) (string, bool) {
	clean := path.Clean(strings.ReplaceAll(relative, "\\", "/"))
	if clean == "." || path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", false
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return "", false
	}
	absolute := filepath.Join(absoluteRoot, filepath.FromSlash(clean))
	relativeToRoot, err := filepath.Rel(absoluteRoot, absolute)
	if err != nil || relativeToRoot == ".." || strings.HasPrefix(relativeToRoot, ".."+string(filepath.Separator)) {
		return "", false
	}
	return absolute, true
}
