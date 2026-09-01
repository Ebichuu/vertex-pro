package startuprecover

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
)

func TestRecoverReordersChainsDeterministically(t *testing.T) {
	database := openMigrated(t)
	// attempt-1：崩溃于 dispatch 开始前，waiter 尚未注册 -> 恢复注册并可恢复 dispatch。
	seedChain(t, database, "1", "RESERVED", "NOT_STARTED", false, "")
	// attempt-2：等待确认，waiter 存活 -> 保持。
	seedChain(t, database, "2", "AWAITING_CONFIRMATION", "STARTED", true, "ACTIVE")
	// attempt-3：UNKNOWN 对账中，waiter 丢失 -> 重新注册。
	seedChain(t, database, "3", "UNKNOWN", "STARTED", false, "")
	// attempt-4：已确认 HASH_SEEN，waiter 崩溃遗留 ACTIVE -> 关闭。
	seedChain(t, database, "4", "HASH_SEEN", "STARTED", true, "ACTIVE")
	// attempt-5：dispatch 已开始，waiter 存活 -> 继续等待事实。
	seedChain(t, database, "5", "ADD_SENT", "STARTED", true, "ACTIVE")

	input := Input{Now: time.Date(2026, 8, 29, 3, 0, 0, 0, time.UTC), ConfirmationWindow: 10 * time.Minute, ReconciliationWindow: time.Hour}
	result, err := Recover(context.Background(), database, input)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(result.ResumedChains, ",") != "attempt-1" {
		t.Fatalf("resumed=%v", result.ResumedChains)
	}
	if strings.Join(result.HeldChains, ",") != "attempt-2,attempt-3,attempt-5" {
		t.Fatalf("held=%v", result.HeldChains)
	}
	if strings.Join(result.ClosedWaiters, ",") != "waiter-4" {
		t.Fatalf("closed=%v", result.ClosedWaiters)
	}
	if strings.Join(result.RestoredWaiters, ",") != "waiter-recovered-attempt-1,waiter-recovered-attempt-3" {
		t.Fatalf("restored=%v", result.RestoredWaiters)
	}
	if strings.Join(result.KeptWaiters, ",") != "waiter-2,waiter-5" {
		t.Fatalf("kept=%v", result.KeptWaiters)
	}
	if len(result.Violations) != 0 {
		t.Fatalf("violations=%v", result.Violations)
	}
	assertWaiterState(t, database, "waiter-recovered-attempt-1", "ACTIVE")
	assertWaiterState(t, database, "waiter-recovered-attempt-3", "ACTIVE")
	assertWaiterState(t, database, "waiter-4", "CLOSED")
	assertWaiterState(t, database, "waiter-2", "ACTIVE")

	// 第二次重启：确定性收敛，不再产生新的关闭或注册。
	second, err := Recover(context.Background(), database, input)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.ClosedWaiters) != 0 || len(second.RestoredWaiters) != 0 {
		t.Fatalf("second run=%+v", second)
	}
	if strings.Join(second.ResumedChains, ",") != "attempt-1" || strings.Join(second.HeldChains, ",") != "attempt-2,attempt-3,attempt-5" {
		t.Fatalf("second chains=%+v", second)
	}
	assertWaiterState(t, database, "waiter-4", "CLOSED")
}

func TestRecoverFailsClosedOnInvariantViolations(t *testing.T) {
	// Reservation 的 reflected_physical_bytes 不变量由数据库 CHECK 强制，
	// 恢复函数内的同项检查是迁移导入场景的纵深防御。
	input := Input{Now: time.Date(2026, 8, 29, 3, 0, 0, 0, time.UTC), ConfirmationWindow: 10 * time.Minute, ReconciliationWindow: time.Hour}
	broken := openMigrated(t)
	seedChain(t, broken, "1", "RESERVED", "NOT_STARTED", false, "")
	if _, err := broken.Exec(`UPDATE race_jobs SET job_state='READY', current_attempt_id=NULL WHERE job_id='job-1'`); err != nil {
		t.Fatal(err)
	}
	mirror, err := Recover(context.Background(), broken, input)
	if err == nil || len(mirror.Violations) == 0 {
		t.Fatalf("mirror violations=%v err=%v", mirror.Violations, err)
	}
	var count int
	if err := broken.QueryRow(`SELECT COUNT(*) FROM sync_waiters`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("waiter rows = %d, recovery must not write on violation", count)
	}

	ambiguous := openMigrated(t)
	seedChain(t, ambiguous, "1", "RESERVED", "STARTED", false, "")
	ambiguousResult, err := Recover(context.Background(), ambiguous, input)
	if err == nil || !strings.Contains(strings.Join(ambiguousResult.Violations, ","), "RESERVED_CHAIN_WITH_STARTED_INTENT") {
		t.Fatalf("ambiguous violations=%v err=%v", ambiguousResult.Violations, err)
	}
}

func TestRecoverValidatesInput(t *testing.T) {
	database := openMigrated(t)
	if _, err := Recover(context.Background(), database, Input{ConfirmationWindow: time.Minute, ReconciliationWindow: time.Hour}); err == nil {
		t.Fatal("zero time error = nil")
	}
	if _, err := Recover(context.Background(), database, Input{Now: time.Now(), ConfirmationWindow: time.Hour, ReconciliationWindow: time.Minute}); err == nil {
		t.Fatal("invalid windows error = nil")
	}
}

func openMigrated(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.Open(context.Background(), db.Config{Path: filepath.Join(t.TempDir(), "startup-recover.db"), MaxOpenConns: 1, MaxIdleConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	runner, err := db.DefaultRunner()
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Apply(context.Background(), database); err != nil {
		t.Fatal(err)
	}
	return database
}

// seedChain 建立一条完整投递链；attemptState/dispatchStarted 决定崩溃点。
func seedChain(t *testing.T, database *sql.DB, suffix, attemptState, intentDispatch string, withWaiter bool, waiterState string) {
	t.Helper()
	now := time.Date(2026, 8, 29, 1, 0, 0, 0, time.UTC).Format(time.RFC3339)
	attemptID := "attempt-" + suffix
	jobID := "job-" + suffix
	sideEffect := "NULL"
	extraColumns := ""
	extraValues := ""
	if attemptState == "HASH_SEEN" {
		sideEffect = "'OBSERVED_EFFECT'"
		extraColumns = ", qb_torrent_id, confirmation_sync_generation, confirmation_snapshot_revision, confirmation_evidence_digest"
		extraValues = ", 'torrent-" + suffix + "', 5, 2, 'evidence-" + suffix + "'"
	} else if attemptState != "RESERVED" {
		sideEffect = "'POSSIBLE_EFFECT'"
	}
	statements := []struct {
		query string
		args  []any
	}{
		{"INSERT INTO storage_targets(storage_target_id, instance_id, path, storage_verification_state, created_at, updated_at) VALUES ('target-" + suffix + "','qb-1','/data-" + suffix + "','VERIFIED',?,?)", []any{now, now}},
		{"INSERT OR IGNORE INTO sync_instance_state(instance_id, sync_state, sync_generation, rid, snapshot_revision, snapshot_digest, updated_at) VALUES ('qb-1','READY',3,0,7,'baseline',?)", []any{now}},
		{"INSERT OR IGNORE INTO sync_snapshots(instance_id, sync_generation, snapshot_revision, rid, snapshot_digest, snapshot_path, is_complete, created_at) VALUES ('qb-1',3,7,0,'baseline','/snapshots/baseline.json',1,?)", []any{now}},
		{"INSERT INTO race_jobs(job_id, source_id, identity_key, job_state, rule_state, torrent_state, placement_state, participation_deadline, observation_deadline, created_at, updated_at) VALUES ('" + jobID + "','source-1','identity-" + suffix + "','READY','MATCHED','READY','PREPARED',?,?,?,?)", []any{now, now, now, now}},
		{"INSERT INTO placement_attempts(attempt_id, job_id, identity_key, attempt_no, instance_id, storage_target_id, attempt_state, dispatch_state, side_effect_fact, allocation_epoch, reserved_bytes, started_at" + extraColumns + ") VALUES ('" + attemptID + "','" + jobID + "','identity-" + suffix + "'," + suffix + ",'qb-1','target-" + suffix + "','" + attemptState + "','" + dispatchStateFor(attemptState) + "'," + sideEffect + ",1,100,?" + extraValues + ")", []any{now}},
		{"INSERT INTO capacity_reservations(reservation_id, attempt_id, storage_target_id, reserved_bytes, allocation_epoch, reservation_state, reflected_physical_bytes) VALUES ('reservation-" + suffix + "','" + attemptID + "','target-" + suffix + "',100,1,'ACTIVE',0)", nil},
		{"INSERT INTO submit_intents(submit_intent_id, attempt_id, instance_id, identity_key, request_fingerprint, target_fingerprint, dispatch_state, pre_add_sync_generation, pre_add_snapshot_revision, pre_add_snapshot_digest, pre_add_identity_fact, created_at, dispatch_started_at) VALUES ('intent-" + suffix + "','" + attemptID + "','qb-1','identity-" + suffix + "','req','target','" + intentDispatch + "',3,7,'baseline','ABSENT',?, " + startedAtFor(intentDispatch, now) + ")", []any{now}},
		{"UPDATE race_jobs SET job_state='" + attemptState + "', current_attempt_id='" + attemptID + "' WHERE job_id='" + jobID + "'", nil},
	}
	if withWaiter {
		statements = append(statements, struct {
			query string
			args  []any
		}{"INSERT INTO sync_waiters(waiter_id, attempt_id, instance_id, registered_generation, source_snapshot_revision, identity_key, confirmation_deadline, reconciliation_deadline, waiter_state, created_at, closed_at) VALUES ('waiter-" + suffix + "','" + attemptID + "','qb-1',3,7,'identity-" + suffix + "','2026-08-29T01:10:00Z','2026-08-29T02:00:00Z','" + waiterState + "',?, " + closedAtFor(waiterState, now) + ")", []any{now}})
	}
	for _, statement := range statements {
		if _, err := database.Exec(statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
}

func dispatchStateFor(attemptState string) string {
	if attemptState == "RESERVED" {
		return "NOT_STARTED"
	}
	return "STARTED"
}

func startedAtFor(intentDispatch, now string) string {
	if intentDispatch == "STARTED" {
		return "'" + now + "'"
	}
	return "NULL"
}

func closedAtFor(waiterState, now string) string {
	if waiterState == "CLOSED" {
		return "'" + now + "'"
	}
	return "NULL"
}

func assertWaiterState(t *testing.T, database *sql.DB, waiterID, want string) {
	t.Helper()
	var state string
	if err := database.QueryRow(`SELECT waiter_state FROM sync_waiters WHERE waiter_id=?`, waiterID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != want {
		t.Fatalf("waiter %s = %s want %s", waiterID, state, want)
	}
}
