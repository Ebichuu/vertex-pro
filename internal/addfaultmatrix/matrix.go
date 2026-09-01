package addfaultmatrix

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/alreadypresent"
	"github.com/vertex-pro/vertex-pro/internal/firstconfirm"
	"github.com/vertex-pro/vertex-pro/internal/saferetry"
	"github.com/vertex-pro/vertex-pro/internal/startuprecover"
	"github.com/vertex-pro/vertex-pro/internal/unknownreconciler"
)

// Manifest 是阶段 H P0 故障用例清单，映射 RC8 第 19 节故障矩阵与第 16 节
// Add/恢复纪律。每个用例先以 Seed 固定崩溃点留下的持久化状态，再由 Drive
// 从该状态推进到收敛；Oracle 以数据库与事件流的规范化摘要作为终态断言，
// 同一用例重复运行必须得到相同摘要。
type Case struct {
	ID     string
	RC8Ref string
	Seed   func(t TestingT, database *sql.DB)
	Drive  func(ctx context.Context, database *sql.DB, hook Hook) error
}

// Hook 是故障注入点：在驱动推进前调用，返回错误即模拟 kill -9 中断。
type Hook func(stage string) error

type TestingT interface {
	Helper()
	Fatalf(format string, args ...any)
}

var now = time.Date(2026, 8, 29, 4, 0, 0, 0, time.UTC)

// Manifest 返回全部 P0 用例，顺序固定。
func Manifest() []Case {
	return []Case{
		{
			ID:     "F01_CRASH_BEFORE_WAITER_REGISTRATION",
			RC8Ref: "RC8 16.1 rule2 / 19 Vertex 重启",
			Seed:   seedReservedChain(false),
			Drive:  driveRecoverThenAssertNoDispatch,
		},
		{
			ID:     "F02_CRASH_BEFORE_DISPATCH_PERSIST",
			RC8Ref: "RC8 16.1 / 19 Vertex 重启",
			Seed:   seedReservedChain(true),
			Drive:  driveRecoverThenAssertNoDispatch,
		},
		{
			ID:     "F03_UNKNOWN_WITH_ABSENT_FRESH_SNAPSHOT",
			RC8Ref: "RC8 19 Add 超时/断流 -> UNKNOWN 对账",
			Seed:   seedUnknownChain(false),
			Drive:  driveRecoverReconcile(&unknownreconciler.Snapshot{InstanceID: "qb-1", SyncGeneration: 5, SnapshotRevision: 2, SnapshotDigest: "after", IdentityPresent: false}),
		},
		{
			ID:     "F04_UNKNOWN_CONFIRMED_BY_FRESH_SNAPSHOT",
			RC8Ref: "RC8 19 JSON 200 但 Hash 不出现 -> 继续对账",
			Seed:   seedUnknownChain(true),
			Drive:  driveRecoverReconcile(&unknownreconciler.Snapshot{InstanceID: "qb-1", SyncGeneration: 5, SnapshotRevision: 2, SnapshotDigest: "after", IdentityPresent: true, QBTorrentID: "torrent-9"}),
		},
		{
			ID:     "F05_QB_OFFLINE_PAST_RECONCILIATION_DEADLINE",
			RC8Ref: "RC8 19 Add 超时 + Vertex 重启 -> EXPIRED_UNKNOWN",
			Seed:   seedUnknownChain(true),
			Drive:  driveReconcileOfflinePastDeadline,
		},
		{
			ID:     "F06_CONFIRM_TRANSACTION_IDEMPOTENT_REPLAY",
			RC8Ref: "RC8 8.7.3 确认事务崩溃与重复快照",
			Seed:   seedAwaitingChain(),
			Drive:  driveConfirmTwice,
		},
		{
			ID:     "F07_ALREADY_PRESENT_BRANCH_AFTER_CRASH",
			RC8Ref: "RC8 19 / 8.7.3 ALREADY_PRESENT 原子分支",
			Seed:   seedReservedPresentChain(),
			Drive:  driveAlreadyPresent,
		},
		{
			ID:     "F08_REQUEUED_JOB_WITHOUT_NEW_PLACEMENT",
			RC8Ref: "RC8 19 旧 Attempt 已关闭但新 Placement 未执行",
			Seed:   seedClosedFactChain(),
			Drive:  driveRetryRequeueLeavesReady,
		},
	}
}

func driveRecoverThenAssertNoDispatch(ctx context.Context, database *sql.DB, hook Hook) error {
	if err := hook("recover"); err != nil {
		return err
	}
	if _, err := startuprecover.Recover(ctx, database, startuprecover.Input{Now: now, ConfirmationWindow: 10 * time.Minute, ReconciliationWindow: time.Hour}); err != nil {
		return err
	}
	return assertIntentNotDispatched(ctx, database, "intent-1")
}

func driveRecoverReconcile(snapshot *unknownreconciler.Snapshot) func(ctx context.Context, database *sql.DB, hook Hook) error {
	return func(ctx context.Context, database *sql.DB, hook Hook) error {
		if err := hook("recover"); err != nil {
			return err
		}
		if _, err := startuprecover.Recover(ctx, database, startuprecover.Input{Now: now, ConfirmationWindow: 10 * time.Minute, ReconciliationWindow: time.Hour}); err != nil {
			return err
		}
		if err := hook("reconcile"); err != nil {
			return err
		}
		provider := &fixedProvider{snapshot: snapshot}
		_, err := unknownreconciler.Sweep(ctx, database, provider, unknownreconciler.SweepInput{Now: now})
		return err
	}
}

func driveReconcileOfflinePastDeadline(ctx context.Context, database *sql.DB, hook Hook) error {
	if err := hook("reconcile"); err != nil {
		return err
	}
	_, err := unknownreconciler.Sweep(ctx, database, &fixedProvider{}, unknownreconciler.SweepInput{Now: time.Date(2026, 8, 29, 5, 0, 0, 0, time.UTC)})
	return err
}

func driveConfirmTwice(ctx context.Context, database *sql.DB, hook Hook) error {
	request := firstconfirm.Request{
		BindingID: "binding-1", AttemptID: "attempt-1", SubmitIntentID: "intent-1",
		ReservationID: "reservation-1", JobID: "job-1", StorageTargetID: "target-1",
		IdentityKey: "identity-1", QBTorrentID: "torrent-9",
		SyncGeneration: 5, SnapshotRevision: 2, SnapshotDigest: "after",
		PreAddSyncGeneration: 3, PreAddSnapshotRevision: 7, PreAddSnapshotDigest: "baseline",
		EvidenceDigest: "evidence-1", Now: now,
	}
	if err := hook("confirm"); err != nil {
		return err
	}
	if _, err := firstconfirm.Confirm(ctx, database, request); err != nil {
		return err
	}
	// 崩溃后同一确认事实重复到达：幂等返回同一提交结果。
	if _, err := firstconfirm.Confirm(ctx, database, request); err != nil {
		return err
	}
	return nil
}

func driveAlreadyPresent(ctx context.Context, database *sql.DB, hook Hook) error {
	if err := hook("already-present"); err != nil {
		return err
	}
	_, err := alreadypresent.Record(ctx, database, alreadypresent.Request{
		BindingID: "binding-1", AttemptID: "attempt-1", SubmitIntentID: "intent-1",
		ReservationID: "reservation-1", JobID: "job-1", StorageTargetID: "target-1",
		InstanceID: "qb-1", IdentityKey: "identity-1", QBTorrentID: "torrent-9",
		AmountLeftKnown: true, AmountLeft: 2048, Now: now,
	})
	return err
}

func driveRetryRequeueLeavesReady(ctx context.Context, database *sql.DB, hook Hook) error {
	if err := hook("requeue"); err != nil {
		return err
	}
	// 旧 Attempt 的 ADD_FAILED 事实已记录但旧链未关闭；关闭命令把 Job 放回
	// READY 并释放旧链。新 Placement 未执行：断言无半创建 Attempt。
	if _, err := saferetry.CloseChainAndRequeue(ctx, database, saferetry.Request{
		CommandID: "cmd-requeue", JobID: "job-1", AttemptID: "attempt-1",
		ReservationID: "reservation-1", StorageTargetID: "target-1",
		InstanceID: "qb-1", IdentityKey: "identity-1",
		MaxAttempts: 2, ParticipationDeadline: "2026-08-29T08:00:00Z", Now: now,
	}); err != nil {
		return err
	}
	return assertReadyWithoutNewAttempt(ctx, database, "job-1")
}

type fixedProvider struct {
	snapshot *unknownreconciler.Snapshot
}

func (p *fixedProvider) FreshestComplete(_ context.Context, _ string) (unknownreconciler.Snapshot, bool) {
	if p.snapshot == nil {
		return unknownreconciler.Snapshot{}, false
	}
	return *p.snapshot, true
}

// Oracle 将终态相关的全部行按主键规范化序列化后取 SHA-256；事件与命令
// 一并纳入，保证终态断言覆盖 DB 与事件流。
func Oracle(database *sql.DB) (string, error) {
	queries := []string{
		`SELECT attempt_id, job_id, attempt_state, dispatch_state, COALESCE(side_effect_fact,''), COALESCE(qb_torrent_id,''), COALESCE(confirmation_evidence_digest,'') FROM placement_attempts ORDER BY attempt_id`,
		`SELECT job_id, job_state, COALESCE(current_attempt_id,''), observation_state, preparation_revision FROM race_jobs ORDER BY job_id`,
		`SELECT reservation_id, reservation_state, COALESCE(release_reason,''), COALESCE(released_unconsumed_bytes,-1), revision FROM capacity_reservations ORDER BY reservation_id`,
		`SELECT waiter_id, waiter_state, COALESCE(closed_at,'') FROM sync_waiters ORDER BY waiter_id`,
		`SELECT binding_id, owner_kind, COALESCE(reservation_id,''), amount_left_state FROM capacity_task_bindings ORDER BY binding_id`,
		`SELECT event_id, event_type, COALESCE(from_state,''), to_state, COALESCE(reason_code,'') FROM race_events ORDER BY event_id`,
		`SELECT submit_intent_id, dispatch_state FROM submit_intents ORDER BY submit_intent_id`,
	}
	digest := sha256.New()
	for _, query := range queries {
		rows, err := database.Query(query)
		if err != nil {
			return "", fmt.Errorf("oracle query: %w", err)
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			return "", fmt.Errorf("oracle columns: %w", err)
		}
		values := make([]any, len(columns))
		scan := make([]sql.NullString, len(columns))
		for i := range values {
			values[i] = &scan[i]
		}
		for rows.Next() {
			if err := rows.Scan(values...); err != nil {
				rows.Close()
				return "", fmt.Errorf("oracle scan: %w", err)
			}
			parts := make([]string, len(columns))
			for i := range scan {
				parts[i] = scan[i].String
			}
			fmt.Fprintln(digest, strings.Join(parts, "\x1f"))
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return "", fmt.Errorf("oracle rows: %w", err)
		}
		rows.Close()
		fmt.Fprintln(digest, "---")
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func assertIntentNotDispatched(ctx context.Context, database *sql.DB, intentID string) error {
	var dispatch string
	if err := database.QueryRowContext(ctx, `SELECT dispatch_state FROM submit_intents WHERE submit_intent_id = ?`, intentID).Scan(&dispatch); err != nil {
		return err
	}
	if dispatch != "NOT_STARTED" {
		return fmt.Errorf("intent %s dispatch_state = %s, recovery must not dispatch", intentID, dispatch)
	}
	return nil
}

func assertReadyWithoutNewAttempt(ctx context.Context, database *sql.DB, jobID string) error {
	var jobState string
	var current sql.NullString
	if err := database.QueryRowContext(ctx, `SELECT job_state, current_attempt_id FROM race_jobs WHERE job_id = ?`, jobID).Scan(&jobState, &current); err != nil {
		return err
	}
	if jobState != "READY" || current.Valid {
		return fmt.Errorf("job %s = %s/%v, want READY without pointer", jobID, jobState, current)
	}
	var attempts int
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM placement_attempts WHERE job_id = ?`, jobID).Scan(&attempts); err != nil {
		return err
	}
	if attempts != 1 {
		return fmt.Errorf("job %s has %d attempts after requeue", jobID, attempts)
	}
	return nil
}

// seedReservedChain 固定 RESERVED 链在崩溃点的两种形态（waiter 已/未注册）。
func seedReservedChain(withWaiter bool) func(TestingT, *sql.DB) {
	return func(t TestingT, database *sql.DB) {
		seedChain(t, database, chainSpec{attemptState: "RESERVED", intentDispatch: "NOT_STARTED", withWaiter: withWaiter})
	}
}

// seedUnknownChain 固定 UNKNOWN 链（waiter 崩溃遗留状态可选）。
func seedUnknownChain(withWaiter bool) func(TestingT, *sql.DB) {
	return func(t TestingT, database *sql.DB) {
		seedChain(t, database, chainSpec{attemptState: "UNKNOWN", intentDispatch: "STARTED", withWaiter: withWaiter})
	}
}

func seedAwaitingChain() func(TestingT, *sql.DB) {
	return func(t TestingT, database *sql.DB) {
		seedChain(t, database, chainSpec{attemptState: "AWAITING_CONFIRMATION", intentDispatch: "STARTED", withWaiter: true})
	}
}

func seedClosedFactChain() func(TestingT, *sql.DB) {
	return func(t TestingT, database *sql.DB) {
		seedChain(t, database, chainSpec{attemptState: "ADD_FAILED", intentDispatch: "STARTED", withWaiter: true})
	}
}

func seedReservedPresentChain() func(TestingT, *sql.DB) {
	return func(t TestingT, database *sql.DB) {
		seedChain(t, database, chainSpec{attemptState: "RESERVED", intentDispatch: "NOT_STARTED", withWaiter: false, preAddFact: "PRESENT", preAddTorrent: "torrent-9"})
	}
}

type chainSpec struct {
	attemptState   string
	intentDispatch string
	withWaiter     bool
	preAddFact     string
	preAddTorrent  string
}

func seedChain(t TestingT, database *sql.DB, spec chainSpec) {
	t.Helper()
	timestamp := time.Date(2026, 8, 29, 1, 0, 0, 0, time.UTC).Format(time.RFC3339)
	attemptState := spec.attemptState
	dispatchState := "NOT_STARTED"
	sideEffect := "NULL"
	if attemptState == "ADD_FAILED" {
		dispatchState = "STARTED"
		sideEffect = "'FAILED_NO_EFFECT'"
	} else if attemptState != "RESERVED" {
		dispatchState = "STARTED"
		sideEffect = "'POSSIBLE_EFFECT'"
	}
	if spec.preAddFact == "PRESENT" && attemptState != "RESERVED" {
		t.Fatalf("PRESENT baseline cannot dispatch")
	}
	fact := spec.preAddFact
	if fact == "" {
		fact = "ABSENT"
	}
	torrentSQL := "NULL"
	if spec.preAddTorrent != "" {
		torrentSQL = "'" + spec.preAddTorrent + "'"
	}
	intentStartedAt := "NULL"
	if spec.intentDispatch == "STARTED" {
		intentStartedAt = "'" + timestamp + "'"
	}
	statements := []string{
		`INSERT INTO storage_targets(storage_target_id, instance_id, path, storage_verification_state, created_at, updated_at) VALUES ('target-1','qb-1','/data','VERIFIED','` + timestamp + `','` + timestamp + `')`,
		`INSERT OR IGNORE INTO sync_instance_state(instance_id, sync_state, sync_generation, rid, snapshot_revision, snapshot_digest, updated_at) VALUES ('qb-1','READY',3,0,7,'baseline','` + timestamp + `')`,
		`INSERT OR IGNORE INTO sync_snapshots(instance_id, sync_generation, snapshot_revision, rid, snapshot_digest, snapshot_path, is_complete, created_at) VALUES ('qb-1',3,7,0,'baseline','/snapshots/baseline.json',1,'` + timestamp + `')`,
		`INSERT INTO race_jobs(job_id, source_id, identity_key, job_state, rule_state, torrent_state, placement_state, participation_deadline, observation_deadline, created_at, updated_at) VALUES ('job-1','source-1','identity-1','READY','MATCHED','READY','PREPARED','2026-08-29T08:00:00Z','2026-08-29T08:00:00Z','` + timestamp + `','` + timestamp + `')`,
		`INSERT INTO placement_attempts(attempt_id, job_id, identity_key, attempt_no, instance_id, storage_target_id, attempt_state, dispatch_state, side_effect_fact, allocation_epoch, reserved_bytes, started_at) VALUES ('attempt-1','job-1','identity-1',1,'qb-1','target-1','` + attemptState + `','` + dispatchState + `',` + sideEffect + `,1,100,'` + timestamp + `')`,
		`INSERT INTO capacity_reservations(reservation_id, attempt_id, storage_target_id, reserved_bytes, allocation_epoch, reservation_state, reflected_physical_bytes) VALUES ('reservation-1','attempt-1','target-1',100,1,'ACTIVE',0)`,
		`INSERT INTO submit_intents(submit_intent_id, attempt_id, instance_id, identity_key, request_fingerprint, target_fingerprint, dispatch_state, pre_add_sync_generation, pre_add_snapshot_revision, pre_add_snapshot_digest, pre_add_identity_fact, pre_add_qb_torrent_id, created_at, dispatch_started_at) VALUES ('intent-1','attempt-1','qb-1','identity-1','req','target','` + spec.intentDispatch + `',3,7,'baseline','` + fact + `',` + torrentSQL + `,'` + timestamp + `',` + intentStartedAt + `)`,
	}
	pointer := "'attempt-1'"
	switch attemptState {
	case "ADD_FAILED", "ALREADY_PRESENT", "RECONCILED_NO_EFFECT":
		pointer = "NULL"
	}
	statements = append(statements, `UPDATE race_jobs SET job_state='`+attemptState+`', current_attempt_id=`+pointer+` WHERE job_id='job-1'`)
	if spec.withWaiter {
		statements = append(statements, `INSERT INTO sync_waiters(waiter_id, attempt_id, instance_id, registered_generation, source_snapshot_revision, identity_key, confirmation_deadline, reconciliation_deadline, waiter_state, created_at) VALUES ('waiter-1','attempt-1','qb-1',3,7,'identity-1','2026-08-29T01:10:00Z','2026-08-29T02:00:00Z','ACTIVE','`+timestamp+`')`)
	}
	for _, statement := range statements {
		if _, err := database.Exec(statement); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
}
