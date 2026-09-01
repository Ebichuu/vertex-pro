package unknownreconciler

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
)

// ExpireRequest 是 EXPIRE_UNRESOLVED 受控命令的输入（§16.8 矩阵：
// UNKNOWN + EXPIRE_UNRESOLVED -> EXPIRED_UNKNOWN，不释放 Reservation）。
type ExpireRequest struct {
	CommandID     string
	JobID         string
	AttemptID     string
	ReservationID string
	WaiterID      string
	Now           time.Time
}

type ExpireResult struct {
	CommandID string
}

// ExpireUnresolved 把仍处于 UNKNOWN 的尝试手动推进到 EXPIRED_UNKNOWN：
// 关闭 waiter 停止自动对账，但保留 Reservation 与 Identity 活动链，等待
// 两条受审计人工闭环；写入 SUCCEEDED api_commands 审计。绝不重放 Add。
func ExpireUnresolved(ctx context.Context, database *sql.DB, request ExpireRequest) (ExpireResult, error) {
	if database == nil {
		return ExpireResult{}, fmt.Errorf("database is required")
	}
	if request.CommandID == "" || request.AttemptID == "" || request.JobID == "" {
		return ExpireResult{}, fmt.Errorf("command id, job id and attempt id are required")
	}
	if request.Now.IsZero() {
		return ExpireResult{}, fmt.Errorf("command time is required")
	}
	item := candidate{attemptID: request.AttemptID, jobID: request.JobID, reservationID: request.ReservationID, waiterID: request.WaiterID}
	if err := recordExpired(ctx, database, item, request.Now); err != nil {
		return ExpireResult{}, err
	}
	err := db.RunImmediate(ctx, database, func(ctx context.Context, transaction *db.ImmediateTx) error {
		timestamp := request.Now.UTC().Format(time.RFC3339)
		if _, err := transaction.ExecContext(ctx, `INSERT INTO api_commands(command_id, command_type, subject_id, aggregate_type, aggregate_id, command_status, request_fingerprint, result_json, created_at, started_at, finished_at, expires_at) VALUES (?, 'EXPIRE_UNRESOLVED', ?, 'race_job', ?, 'SUCCEEDED', ?, '{"outcome":"EXPIRED_UNKNOWN"}', ?, ?, ?, ?)
			ON CONFLICT(command_id) DO NOTHING`, request.CommandID, request.JobID, request.JobID, fingerprintOf(request.AttemptID), timestamp, timestamp, timestamp, timestamp); err != nil {
			return fmt.Errorf("insert command audit: %w", err)
		}
		return nil
	})
	if err != nil {
		return ExpireResult{}, err
	}
	return ExpireResult{CommandID: request.CommandID}, nil
}

func fingerprintOf(attemptID string) string {
	digest := sha256.Sum256([]byte("EXPIRE_UNRESOLVED|" + attemptID))
	return hex.EncodeToString(digest[:])
}

// FixedProvider 把单次请求携带的快照证据包装为 SnapshotProvider，供
// CONTINUE_RECONCILIATION 的即时对账使用。
type FixedProvider struct {
	Snapshot Snapshot
}

func (p FixedProvider) FreshestComplete(_ context.Context, _ string) (Snapshot, bool) {
	if p.Snapshot.SnapshotDigest == "" {
		return Snapshot{}, false
	}
	return p.Snapshot, true
}
