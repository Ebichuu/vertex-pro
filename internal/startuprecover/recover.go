package startuprecover

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
	"github.com/vertex-pro/vertex-pro/internal/syncwaiter"
)

var ErrRecoveryFailed = errors.New("startup recovery failed closed")

// 进程崩溃后处于投递链上、仍需继续确认或对账的状态。
var pointerStates = map[string]bool{
	"RESERVED": true, "ADD_SENT": true, "AWAITING_CONFIRMATION": true,
	"UNKNOWN": true, "HASH_SEEN": true, "RECONCILED_CONFIRMED": true, "EXPIRED_UNKNOWN": true,
}

// 终局状态：waiter 若仍残留 ACTIVE 属于崩溃遗留，恢复时统一关闭。
var terminalStates = map[string]bool{
	"HASH_SEEN": true, "ALREADY_PRESENT": true, "ADD_FAILED": true,
	"RECONCILED_CONFIRMED": true, "RECONCILED_NO_EFFECT": true, "EXPIRED_UNKNOWN": true,
}

type Input struct {
	Now                  time.Time
	ConfirmationWindow   time.Duration
	ReconciliationWindow time.Duration
}

type Result struct {
	RestoredWaiters []string
	KeptWaiters     []string
	ClosedWaiters   []string
	ResumedChains   []string
	HeldChains      []string
	PromotedUnknown []string
	Violations      []string
}

type chain struct {
	attemptID     string
	jobID         string
	instanceID    string
	identityKey   string
	attemptState  string
	attemptDispa  string
	sideEffect    string
	jobState      string
	currentPtr    sql.NullString
	intentID      sql.NullString
	intentDispa   sql.NullString
	waiterID      sql.NullString
	waiterState   sql.NullString
	preGeneration sql.NullInt64
	preRevision   sql.NullInt64
	startedAt     string
}

// Recover 在进程启动时对持久化投递链做确定性重排：校验 Reservation 与
// Job/Attempt 镜像不变量（违反即 fail-closed，不做任何写入），关闭终局
// Attempt 的遗留 waiter，为可继续的链重新注册缺失的 waiter（恢复 Worker
// 可重新注册），并把链分类为可恢复 dispatch（RESERVED 且未开始）或继续
// 等待确认/对账（dispatch 已开始）。崩溃遗留的 ADD_SENT 会原子提升为
// UNKNOWN；恢复不按进程退出时间释放任何 Reservation，也不自动重放网络
// 请求。相同数据库状态与输入必然得到相同结果。
func Recover(ctx context.Context, database *sql.DB, input Input) (Result, error) {
	result := Result{}
	if err := validateInput(input); err != nil {
		return result, err
	}
	chains, err := loadChains(ctx, database)
	if err != nil {
		return result, err
	}
	if err := checkInvariants(ctx, database, chains, &result); err != nil {
		return result, err
	}
	if len(result.Violations) > 0 {
		return result, fmt.Errorf("%w: %d invariant violations", ErrRecoveryFailed, len(result.Violations))
	}
	for _, item := range chains {
		if err := recoverChain(ctx, database, item, input, &result); err != nil {
			return result, err
		}
	}
	sort.Strings(result.RestoredWaiters)
	sort.Strings(result.KeptWaiters)
	sort.Strings(result.ClosedWaiters)
	sort.Strings(result.ResumedChains)
	sort.Strings(result.HeldChains)
	sort.Strings(result.PromotedUnknown)
	return result, nil
}

func validateInput(input Input) error {
	if input.Now.IsZero() {
		return errors.New("recovery time is required")
	}
	if input.ConfirmationWindow <= 0 || input.ReconciliationWindow < input.ConfirmationWindow {
		return errors.New("recovery windows are invalid")
	}
	return nil
}

func loadChains(ctx context.Context, database *sql.DB) ([]chain, error) {
	rows, err := database.QueryContext(ctx, `SELECT a.attempt_id, a.job_id, a.instance_id, a.identity_key, a.attempt_state, a.dispatch_state, COALESCE(a.side_effect_fact,''), j.job_state, j.current_attempt_id, s.submit_intent_id, s.dispatch_state, w.waiter_id, w.waiter_state, s.pre_add_sync_generation, s.pre_add_snapshot_revision, a.started_at FROM placement_attempts a JOIN race_jobs j ON j.job_id = a.job_id LEFT JOIN submit_intents s ON s.attempt_id = a.attempt_id LEFT JOIN sync_waiters w ON w.attempt_id = a.attempt_id ORDER BY a.attempt_id`)
	if err != nil {
		return nil, fmt.Errorf("list chains: %w", err)
	}
	defer rows.Close()
	chains := make([]chain, 0)
	for rows.Next() {
		var item chain
		if err := rows.Scan(&item.attemptID, &item.jobID, &item.instanceID, &item.identityKey, &item.attemptState, &item.attemptDispa, &item.sideEffect, &item.jobState, &item.currentPtr, &item.intentID, &item.intentDispa, &item.waiterID, &item.waiterState, &item.preGeneration, &item.preRevision, &item.startedAt); err != nil {
			return nil, fmt.Errorf("scan chain: %w", err)
		}
		chains = append(chains, item)
	}
	return chains, rows.Err()
}

func checkInvariants(ctx context.Context, database *sql.DB, chains []chain, result *Result) error {
	var reflected int
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM capacity_reservations WHERE reservation_state IN ('ACTIVE','REFLECTING') AND reflected_physical_bytes != 0`).Scan(&reflected); err != nil {
		return fmt.Errorf("check reservation invariant: %w", err)
	}
	if reflected > 0 {
		result.Violations = append(result.Violations, fmt.Sprintf("RESERVATION_REFLECTED_BYTES_NONZERO:%d", reflected))
	}
	for _, item := range chains {
		if pointerStates[item.attemptState] {
			if !item.currentPtr.Valid || item.currentPtr.String != item.attemptID || item.jobState != item.attemptState {
				result.Violations = append(result.Violations, "JOB_ATTEMPT_MISMATCH:"+item.attemptID+":"+item.jobState)
			}
		} else if item.currentPtr.Valid && item.currentPtr.String == item.attemptID {
			// A historical terminal attempt may coexist with a later current
			// attempt. Only a pointer still targeting this terminal row is stale.
			result.Violations = append(result.Violations, "JOB_POINTER_NOT_CLEARED:"+item.jobID)
		}
		if item.attemptState == "ADD_FAILED" && item.sideEffect != "FAILED_NO_EFFECT" {
			result.Violations = append(result.Violations, "FAILED_CHAIN_FACT_MISMATCH:"+item.attemptID)
		}
		if item.attemptState == "RESERVED" {
			if !item.intentID.Valid {
				result.Violations = append(result.Violations, "RESERVED_CHAIN_WITHOUT_INTENT:"+item.attemptID)
			} else if item.intentDispa.Valid && item.intentDispa.String == "STARTED" {
				result.Violations = append(result.Violations, "RESERVED_CHAIN_WITH_STARTED_INTENT:"+item.attemptID)
			}
			if item.attemptDispa != "NOT_STARTED" || item.sideEffect != "" {
				result.Violations = append(result.Violations, "RESERVED_CHAIN_WITH_DISPATCH_FACT:"+item.attemptID)
			}
		}
		if item.attemptState == "ADD_SENT" || item.attemptState == "AWAITING_CONFIRMATION" || item.attemptState == "UNKNOWN" {
			if item.attemptDispa != "STARTED" || item.sideEffect != "POSSIBLE_EFFECT" || !item.intentDispa.Valid || item.intentDispa.String != "STARTED" {
				result.Violations = append(result.Violations, "STARTED_CHAIN_FACT_MISMATCH:"+item.attemptID)
			}
		}
	}
	// Validate from the job side as well: loadChains cannot reveal a dangling
	// pointer (or a job with no own attempt). Never accept a foreign job's chain,
	// a terminal target or inconsistent mirrored state merely because the old
	// attempt is allowed to have a successor.
	rows, err := database.QueryContext(ctx, `SELECT j.job_id,j.job_state,COALESCE(j.identity_key,''),a.attempt_id,a.job_id,a.attempt_state,a.identity_key
	 FROM race_jobs j LEFT JOIN placement_attempts a ON a.attempt_id=j.current_attempt_id
	 WHERE j.current_attempt_id IS NOT NULL ORDER BY j.job_id`)
	if err != nil {
		return fmt.Errorf("check current attempt pointers: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var jobID, state, identity string
		var attempt, attemptJob, attemptState, attemptIdentity sql.NullString
		if err := rows.Scan(&jobID, &state, &identity, &attempt, &attemptJob, &attemptState, &attemptIdentity); err != nil {
			return err
		}
		if !attempt.Valid || attemptJob.String != jobID || !pointerStates[attemptState.String] || state != attemptState.String || identity == "" || identity != attemptIdentity.String {
			result.Violations = append(result.Violations, "JOB_CURRENT_ATTEMPT_INVALID:"+jobID)
		}
	}
	return rows.Err()
}

func recoverChain(ctx context.Context, database *sql.DB, item chain, input Input, result *Result) error {
	if item.waiterID.Valid && item.waiterState.Valid && item.waiterState.String == "ACTIVE" && terminalStates[item.attemptState] {
		timestamp := input.Now.UTC().Format(time.RFC3339)
		if _, err := database.ExecContext(ctx, `UPDATE sync_waiters SET waiter_state='CLOSED', closed_at=? WHERE waiter_id=? AND waiter_state='ACTIVE'`, timestamp, item.waiterID.String); err != nil {
			return fmt.Errorf("close orphan waiter %s: %w", item.waiterID.String, err)
		}
		result.ClosedWaiters = append(result.ClosedWaiters, item.waiterID.String)
		return nil
	}
	switch item.attemptState {
	case "RESERVED":
		if err := ensureWaiter(ctx, database, item, input, result); err != nil {
			return err
		}
		result.ResumedChains = append(result.ResumedChains, item.attemptID)
	case "ADD_SENT":
		if err := promoteInterruptedDispatch(ctx, database, item, input.Now); err != nil {
			return err
		}
		item.attemptState = "UNKNOWN"
		item.jobState = "UNKNOWN"
		result.PromotedUnknown = append(result.PromotedUnknown, item.attemptID)
		if err := ensureWaiter(ctx, database, item, input, result); err != nil {
			return err
		}
		result.HeldChains = append(result.HeldChains, item.attemptID)
	case "AWAITING_CONFIRMATION", "UNKNOWN":
		if err := ensureWaiter(ctx, database, item, input, result); err != nil {
			return err
		}
		result.HeldChains = append(result.HeldChains, item.attemptID)
	}
	return nil
}

// promoteInterruptedDispatch converts the durable pre-send ADD_SENT marker to
// UNKNOWN during startup. The process may have crashed before or after qB read
// the request body, so recovery must not replay the Add and must converge only
// from a fresh Sync snapshot.
func promoteInterruptedDispatch(ctx context.Context, database *sql.DB, item chain, now time.Time) error {
	timestamp := now.UTC().Format(time.RFC3339Nano)
	return db.RunImmediate(ctx, database, func(ctx context.Context, transaction *db.ImmediateTx) error {
		update, err := transaction.ExecContext(ctx, `UPDATE placement_attempts SET attempt_state='UNKNOWN' WHERE attempt_id=? AND attempt_state='ADD_SENT' AND dispatch_state='STARTED' AND side_effect_fact='POSSIBLE_EFFECT'`, item.attemptID)
		if err != nil {
			return fmt.Errorf("promote interrupted attempt %s: %w", item.attemptID, err)
		}
		if rows, err := update.RowsAffected(); err != nil || rows != 1 {
			return fmt.Errorf("promote interrupted attempt %s rows=%d err=%v", item.attemptID, rows, err)
		}
		update, err = transaction.ExecContext(ctx, `UPDATE race_jobs SET job_state='UNKNOWN', updated_at=? WHERE job_id=? AND job_state='ADD_SENT' AND current_attempt_id=?`, timestamp, item.jobID, item.attemptID)
		if err != nil {
			return fmt.Errorf("promote interrupted job %s: %w", item.jobID, err)
		}
		if rows, err := update.RowsAffected(); err != nil || rows != 1 {
			return fmt.Errorf("promote interrupted job %s rows=%d err=%v", item.jobID, rows, err)
		}
		if _, err := transaction.ExecContext(ctx, `INSERT INTO race_events(job_id, attempt_id, event_type, from_state, to_state, reason_code, safe_metadata_json, occurred_at) VALUES (?, ?, 'attempt.state.changed', 'ADD_SENT', 'UNKNOWN', 'STARTUP_INTERRUPTED_DISPATCH', '{}', ?)`, item.jobID, item.attemptID, timestamp); err != nil {
			return fmt.Errorf("record interrupted dispatch %s: %w", item.attemptID, err)
		}
		return nil
	})
}

// ensureWaiter 保证链上存在 ACTIVE waiter；崩溃可能发生在注册与重启之间，
// 恢复 Worker 依据 SubmitIntent 冻结的 Add 前基线快照重新注册。
func ensureWaiter(ctx context.Context, database *sql.DB, item chain, input Input, result *Result) error {
	if item.waiterID.Valid && item.waiterState.Valid && item.waiterState.String == "ACTIVE" {
		result.KeptWaiters = append(result.KeptWaiters, item.waiterID.String)
		return nil
	}
	if !item.preGeneration.Valid || !item.preRevision.Valid {
		return fmt.Errorf("chain %s lacks frozen baseline snapshot facts", item.attemptID)
	}
	// A missing waiter uses the durable attempt start, never the restart time.
	// Existing waiters retain their exact original deadlines in Restore.
	timestamp, err := time.Parse(time.RFC3339Nano, item.startedAt)
	if err != nil {
		return fmt.Errorf("parse attempt start for %s: %w", item.attemptID, err)
	}
	waiter, err := syncwaiter.Restore(ctx, database, syncwaiter.Request{
		WaiterID:               "waiter-recovered-" + item.attemptID,
		AttemptID:              item.attemptID,
		InstanceID:             item.instanceID,
		RegisteredGeneration:   item.preGeneration.Int64,
		SourceSnapshotRevision: item.preRevision.Int64,
		IdentityKey:            item.identityKey,
		ConfirmationDeadline:   timestamp.Add(input.ConfirmationWindow).Format(time.RFC3339),
		ReconciliationDeadline: timestamp.Add(input.ReconciliationWindow).Format(time.RFC3339),
		CreatedAt:              timestamp.Format(time.RFC3339),
	})
	if err != nil {
		return fmt.Errorf("re-register waiter for %s: %w", item.attemptID, err)
	}
	result.RestoredWaiters = append(result.RestoredWaiters, waiter.ID)
	return nil
}
