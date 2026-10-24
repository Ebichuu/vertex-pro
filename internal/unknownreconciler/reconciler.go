package unknownreconciler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
	"github.com/vertex-pro/vertex-pro/internal/firstconfirm"
)

const (
	OutcomeConfirmed           = "RECONCILED_CONFIRMED"
	OutcomeNoEffect            = "RECONCILED_NO_EFFECT"
	OutcomeExpired             = "EXPIRED_UNKNOWN"
	OutcomeHoldNoFreshEvidence = "HOLD_NO_FRESH_EVIDENCE"
)

// Snapshot 是 SyncWatcher 发布的不可变合并快照中与本次对账相关的事实。
// 只有 is_complete 的新鲜完整快照才能作为对账证据。
type Snapshot struct {
	InstanceID       string
	SyncGeneration   int64
	SnapshotRevision int64
	SnapshotDigest   string
	IdentityPresent  bool
	QBTorrentID      string
}

// SnapshotProvider 返回实例当前最新鲜的完整合并快照；qB/qui 离线或快照
// 未就绪时返回 false。提供方不得伪造证据，也不得返回部分快照。
type SnapshotProvider interface {
	FreshestComplete(ctx context.Context, instanceID string) (Snapshot, bool)
}

type SweepInput struct {
	Now time.Time
	// InstanceID limits an event-driven sweep to the instance whose evidence
	// was collected. Empty keeps the all-instance maintenance sweep.
	InstanceID string
}

type Outcome struct {
	AttemptID string
	JobID     string
	Outcome   string
}

type candidate struct {
	attemptID              string
	jobID                  string
	instanceID             string
	identityKey            string
	storageTargetID        string
	submitIntentID         string
	reservationID          string
	waiterID               string
	reconciliationDeadline string
	preGeneration          int64
	preRevision            int64
	preDigest              string
	preFact                string
}

var ErrSweepInvalid = errors.New("sweep input is invalid")

// Sweep 是 UNKNOWN reconciler 的单个对账周期：从数据库确定性枚举全部
// UNKNOWN 尝试（进程重启后自动恢复，不依赖内存状态），用最新鲜完整快照
// 判定事实。本组件从不发送网络请求，也绝不自动重放 Add：
//
//   - 快照证明 Identity 已存在 -> 复用 firstconfirm 原子事务进入 RECONCILED_CONFIRMED；
//   - 快照证明 Identity 不存在 -> RECONCILED_NO_EFFECT 并以 NO_EFFECT 释放 Reservation；
//   - 无决定性证据且已过 waiter 的 reconciliation_deadline -> EXPIRED_UNKNOWN，
//     保留 Reservation 与 Identity 活动链，等待人工闭环；
//   - 其余情况保持 UNKNOWN 等待下一个周期。
func Sweep(ctx context.Context, database *sql.DB, provider SnapshotProvider, input SweepInput) ([]Outcome, error) {
	if provider == nil {
		return nil, fmt.Errorf("%w: snapshot provider is required", ErrSweepInvalid)
	}
	if input.Now.IsZero() {
		return nil, fmt.Errorf("%w: sweep time is required", ErrSweepInvalid)
	}
	query := `SELECT a.attempt_id, a.job_id, a.instance_id, a.identity_key, a.storage_target_id, s.submit_intent_id, r.reservation_id, w.waiter_id, w.reconciliation_deadline, s.pre_add_sync_generation, s.pre_add_snapshot_revision, s.pre_add_snapshot_digest, s.pre_add_identity_fact FROM placement_attempts a JOIN race_jobs j ON j.job_id = a.job_id AND j.job_state = 'UNKNOWN' AND j.current_attempt_id = a.attempt_id JOIN submit_intents s ON s.attempt_id = a.attempt_id JOIN capacity_reservations r ON r.attempt_id = a.attempt_id AND r.reservation_state = 'ACTIVE' JOIN sync_waiters w ON w.attempt_id = a.attempt_id AND w.waiter_state = 'ACTIVE' WHERE a.attempt_state = 'UNKNOWN'`
	var args []any
	// An earlier durable positive observation must not be overwritten by a
	// later absence while its confirmation transaction is being retried.
	query += ` AND NOT EXISTS(SELECT 1 FROM pending_confirmations p WHERE p.attempt_id=a.attempt_id)`
	if input.InstanceID != "" {
		query += ` AND a.instance_id = ?`
		args = append(args, input.InstanceID)
	}
	query += ` ORDER BY a.attempt_id`
	rows, err := database.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list unknown attempts: %w", err)
	}
	defer rows.Close()
	candidates := make([]candidate, 0)
	for rows.Next() {
		var item candidate
		if err := rows.Scan(&item.attemptID, &item.jobID, &item.instanceID, &item.identityKey, &item.storageTargetID, &item.submitIntentID, &item.reservationID, &item.waiterID, &item.reconciliationDeadline, &item.preGeneration, &item.preRevision, &item.preDigest, &item.preFact); err != nil {
			return nil, fmt.Errorf("scan unknown attempt: %w", err)
		}
		candidates = append(candidates, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate unknown attempts: %w", err)
	}

	outcomes := make([]Outcome, 0, len(candidates))
	for _, item := range candidates {
		outcome, err := reconcileOne(ctx, database, provider, item, input.Now)
		if err != nil {
			return nil, err
		}
		outcomes = append(outcomes, outcome)
	}
	return outcomes, nil
}

func reconcileOne(ctx context.Context, database *sql.DB, provider SnapshotProvider, item candidate, now time.Time) (Outcome, error) {
	outcome := Outcome{AttemptID: item.attemptID, JobID: item.jobID, Outcome: OutcomeHoldNoFreshEvidence}
	snapshot, available := provider.FreshestComplete(ctx, item.instanceID)
	if available && (snapshot.InstanceID != item.instanceID || strings.TrimSpace(snapshot.SnapshotDigest) == "") {
		return Outcome{}, fmt.Errorf("provider returned invalid snapshot for instance %s", item.instanceID)
	}
	decisive := available && isFreshRelativeToAddBaseline(snapshot, item)
	if decisive && snapshot.IdentityPresent {
		if strings.TrimSpace(snapshot.QBTorrentID) == "" {
			return Outcome{}, fmt.Errorf("present snapshot for attempt %s lacks qb torrent id", item.attemptID)
		}
		if item.preFact != "ABSENT" {
			return Outcome{}, fmt.Errorf("attempt %s pre-add fact %q cannot be confirmed as new effect", item.attemptID, item.preFact)
		}
		if _, err := firstconfirm.Confirm(ctx, database, firstconfirm.Request{
			BindingID:              "binding-" + item.attemptID,
			AttemptID:              item.attemptID,
			SubmitIntentID:         item.submitIntentID,
			ReservationID:          item.reservationID,
			JobID:                  item.jobID,
			StorageTargetID:        item.storageTargetID,
			IdentityKey:            item.identityKey,
			QBTorrentID:            snapshot.QBTorrentID,
			SyncGeneration:         snapshot.SyncGeneration,
			SnapshotRevision:       snapshot.SnapshotRevision,
			SnapshotDigest:         snapshot.SnapshotDigest,
			PreAddSyncGeneration:   item.preGeneration,
			PreAddSnapshotRevision: item.preRevision,
			PreAddSnapshotDigest:   item.preDigest,
			EvidenceDigest:         evidenceDigest(snapshot),
			Now:                    now,
		}); err != nil {
			return Outcome{}, fmt.Errorf("confirm unknown attempt %s: %w", item.attemptID, err)
		}
		outcome.Outcome = OutcomeConfirmed
		return outcome, nil
	}
	if decisive {
		if item.preFact != "ABSENT" {
			return Outcome{}, fmt.Errorf("attempt %s pre-add fact %q cannot be reconciled as no effect", item.attemptID, item.preFact)
		}
		if err := recordNoEffect(ctx, database, item, snapshot, now); err != nil {
			return Outcome{}, err
		}
		outcome.Outcome = OutcomeNoEffect
		return outcome, nil
	}
	deadline, err := time.Parse(time.RFC3339, item.reconciliationDeadline)
	if err != nil {
		return Outcome{}, fmt.Errorf("parse reconciliation deadline for attempt %s: %w", item.attemptID, err)
	}
	if now.UTC().Before(deadline) {
		return outcome, nil
	}
	if err := recordExpired(ctx, database, item, now); err != nil {
		return Outcome{}, err
	}
	outcome.Outcome = OutcomeExpired
	return outcome, nil
}

func isFreshRelativeToAddBaseline(snapshot Snapshot, item candidate) bool {
	if snapshot.SyncGeneration < item.preGeneration {
		return false
	}
	if snapshot.SyncGeneration == item.preGeneration && snapshot.SnapshotRevision <= item.preRevision {
		return false
	}
	return snapshot.SnapshotDigest != item.preDigest
}

// recordNoEffect 在单个事务中把已被新鲜完整快照证明无副作用的尝试推进到
// RECONCILED_NO_EFFECT，关闭 waiter，并以 NO_EFFECT 释放 Reservation。
func recordNoEffect(ctx context.Context, database *sql.DB, item candidate, snapshot Snapshot, now time.Time) error {
	timestamp := now.UTC().Format(time.RFC3339)
	return db.RunImmediate(ctx, database, func(ctx context.Context, transaction *db.ImmediateTx) error {
		update, err := transaction.ExecContext(ctx, `UPDATE placement_attempts SET attempt_state='RECONCILED_NO_EFFECT', finished_at=? WHERE attempt_id=? AND attempt_state='UNKNOWN' AND dispatch_state='STARTED'`, timestamp, item.attemptID)
		if err != nil {
			return fmt.Errorf("reconcile attempt: %w", err)
		}
		if rows, err := update.RowsAffected(); err != nil || rows != 1 {
			return fmt.Errorf("reconcile attempt rows=%d err=%v", rows, err)
		}
		update, err = transaction.ExecContext(ctx, `UPDATE race_jobs SET job_state='RECONCILED_NO_EFFECT', current_attempt_id=NULL, updated_at=? WHERE job_id=? AND job_state='UNKNOWN' AND current_attempt_id=?`, timestamp, item.jobID, item.attemptID)
		if err != nil {
			return fmt.Errorf("reconcile job: %w", err)
		}
		if rows, err := update.RowsAffected(); err != nil || rows != 1 {
			return fmt.Errorf("reconcile job rows=%d err=%v", rows, err)
		}
		update, err = transaction.ExecContext(ctx, `UPDATE capacity_reservations SET reservation_state='RELEASED', release_reason='NO_EFFECT', released_unconsumed_bytes=reserved_bytes-reflected_physical_bytes, released_at=?, revision=revision+1 WHERE reservation_id=? AND reservation_state='ACTIVE'`, timestamp, item.reservationID)
		if err != nil {
			return fmt.Errorf("release reservation: %w", err)
		}
		if rows, err := update.RowsAffected(); err != nil || rows != 1 {
			return fmt.Errorf("release reservation rows=%d err=%v", rows, err)
		}
		if _, err := transaction.ExecContext(ctx, `UPDATE sync_waiters SET waiter_state='CLOSED', closed_at=? WHERE waiter_id=? AND waiter_state='ACTIVE'`, timestamp, item.waiterID); err != nil {
			return fmt.Errorf("close waiter: %w", err)
		}
		if _, err := transaction.ExecContext(ctx, `INSERT INTO race_events(job_id, attempt_id, event_type, from_state, to_state, reason_code, safe_metadata_json, occurred_at) VALUES (?, ?, 'attempt.state.changed', 'UNKNOWN', 'RECONCILED_NO_EFFECT', 'FRESH_SNAPSHOT_IDENTITY_ABSENT', '{}', ?)`, item.jobID, item.attemptID, timestamp); err != nil {
			return fmt.Errorf("insert attempt event: %w", err)
		}
		if _, err := transaction.ExecContext(ctx, `INSERT INTO race_events(job_id, event_type, from_state, to_state, reason_code, safe_metadata_json, occurred_at) VALUES (?, 'job.state.changed', 'UNKNOWN', 'RECONCILED_NO_EFFECT', 'FRESH_SNAPSHOT_IDENTITY_ABSENT', '{}', ?)`, item.jobID, timestamp); err != nil {
			return fmt.Errorf("insert job event: %w", err)
		}
		return nil
	})
}

// recordExpired 在对账 deadline 到期且仍无决定性证据时把尝试推进到
// EXPIRED_UNKNOWN：关闭 waiter 停止自动对账，但保留 Reservation 与
// current_attempt_id，等待两条受审计的人工闭环。
func recordExpired(ctx context.Context, database *sql.DB, item candidate, now time.Time) error {
	timestamp := now.UTC().Format(time.RFC3339)
	return db.RunImmediate(ctx, database, func(ctx context.Context, transaction *db.ImmediateTx) error {
		update, err := transaction.ExecContext(ctx, `UPDATE placement_attempts SET attempt_state='EXPIRED_UNKNOWN', finished_at=? WHERE attempt_id=? AND attempt_state='UNKNOWN' AND dispatch_state='STARTED'`, timestamp, item.attemptID)
		if err != nil {
			return fmt.Errorf("expire attempt: %w", err)
		}
		if rows, err := update.RowsAffected(); err != nil || rows != 1 {
			return fmt.Errorf("expire attempt rows=%d err=%v", rows, err)
		}
		update, err = transaction.ExecContext(ctx, `UPDATE race_jobs SET job_state='EXPIRED_UNKNOWN', updated_at=? WHERE job_id=? AND job_state='UNKNOWN' AND current_attempt_id=?`, timestamp, item.jobID, item.attemptID)
		if err != nil {
			return fmt.Errorf("expire job: %w", err)
		}
		if rows, err := update.RowsAffected(); err != nil || rows != 1 {
			return fmt.Errorf("expire job rows=%d err=%v", rows, err)
		}
		if _, err := transaction.ExecContext(ctx, `UPDATE sync_waiters SET waiter_state='CLOSED', closed_at=? WHERE waiter_id=? AND waiter_state='ACTIVE'`, timestamp, item.waiterID); err != nil {
			return fmt.Errorf("close waiter: %w", err)
		}
		if _, err := transaction.ExecContext(ctx, `INSERT INTO race_events(job_id, attempt_id, event_type, from_state, to_state, reason_code, safe_metadata_json, occurred_at) VALUES (?, ?, 'attempt.state.changed', 'UNKNOWN', 'EXPIRED_UNKNOWN', 'RECONCILIATION_DEADLINE_EXPIRED', '{}', ?)`, item.jobID, item.attemptID, timestamp); err != nil {
			return fmt.Errorf("insert attempt event: %w", err)
		}
		if _, err := transaction.ExecContext(ctx, `INSERT INTO race_events(job_id, event_type, from_state, to_state, reason_code, safe_metadata_json, occurred_at) VALUES (?, 'job.state.changed', 'UNKNOWN', 'EXPIRED_UNKNOWN', 'RECONCILIATION_DEADLINE_EXPIRED', '{}', ?)`, item.jobID, timestamp); err != nil {
			return fmt.Errorf("insert job event: %w", err)
		}
		return nil
	})
}

func evidenceDigest(snapshot Snapshot) string {
	return fmt.Sprintf("snapshot:%s:%d:%d:%s", snapshot.InstanceID, snapshot.SyncGeneration, snapshot.SnapshotRevision, snapshot.SnapshotDigest)
}
