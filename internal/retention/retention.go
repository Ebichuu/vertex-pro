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

	if snapshotRoot != "" {
		snapshots, failures, err := removeExpiredSnapshots(ctx, database, snapshotRoot, now.UTC().Add(-policy.SnapshotRetention).Format(time.RFC3339Nano), policy.batchSize())
		if err != nil {
			return result, fmt.Errorf("prune expired snapshots: %w", err)
		}
		result.Snapshots = snapshots
		result.SnapshotFileFailures = failures
	}

	stats, err := statsstore.Prune(ctx, database, now, statsstore.Retention{RawSamples: policy.StatsRawRetention, Hourly: policy.StatsHourly})
	if err != nil {
		return result, fmt.Errorf("prune stats: %w", err)
	}
	result.StatsRaw = stats.RawSamples
	result.StatsHourly = stats.Hourly
	return result, nil
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
	for {
		deleted := int64(0)
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
				removedPaths = append(removedPaths, item.path)
			}
			deleted = int64(len(candidates))
			return nil
		})
		if err != nil {
			return total, failures, err
		}
		total += deleted
		if deleted < int64(batchSize) {
			break
		}
	}
	for _, relative := range removedPaths {
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
	return total, failures, nil
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
