package apiidempotency

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
)

var (
	ErrKeyReused       = errors.New("idempotency key reused with different request body")
	ErrMissingKey      = errors.New("idempotency key is required")
	ErrCommandFinished = errors.New("idempotent command already finished")
)

// RecordTTL 固定 24 小时（API-06：幂等记录 24h）；命令本身保留 7 天
// （API-07），幂等记录过期后命令仍可按 command_id 查询。
const (
	RecordTTL  = 24 * time.Hour
	CommandTTL = 7 * 24 * time.Hour
)

// Begin 在单个事务中注册幂等键并创建 ACCEPTED 命令：
//   - 键不存在 -> 创建记录 + ACCEPTED 命令行，Replayed=false（调用方执行动作）；
//   - 键存在且指纹相同且未过期 -> Replayed=true（响应丢失后的重试：不重复
//     执行，调用方返回已存命令的结果）；
//   - 键存在但指纹不同 -> ErrKeyReused（409 IDEMPOTENCY_KEY_REUSED）；
//   - 键已过期 -> 清除后按新键注册。
//
// 指纹为请求体的 SHA-256：同 key 同 body 幂等重放，同 key 异 body 拒绝。
func Begin(ctx context.Context, database *sql.DB, key, subjectID, fingerprint string, now time.Time) (Result, error) {
	if key == "" {
		return Result{}, ErrMissingKey
	}
	if fingerprint == "" {
		return Result{}, errors.New("request fingerprint is required")
	}
	var stored Result
	err := db.RunImmediate(ctx, database, func(ctx context.Context, transaction *db.ImmediateTx) error {
		var commandID, storedFingerprint, createdAt string
		err := transaction.QueryRowContext(ctx, `SELECT c.command_id, r.request_fingerprint, r.created_at FROM api_idempotency_records r JOIN api_commands c ON c.command_id = r.command_id WHERE r.idempotency_key = ?`, key).Scan(&commandID, &storedFingerprint, &createdAt)
		switch {
		case err == nil:
			expiresAt, parseErr := time.Parse(time.RFC3339, createdAt)
			if parseErr != nil {
				return fmt.Errorf("parse record created_at: %w", parseErr)
			}
			if now.UTC().Sub(expiresAt) >= RecordTTL {
				if _, err := transaction.ExecContext(ctx, `DELETE FROM api_idempotency_records WHERE idempotency_key = ?`, key); err != nil {
					return fmt.Errorf("expire stale record: %w", err)
				}
				return register(ctx, transaction, key, subjectID, fingerprint, now, &stored)
			}
			if storedFingerprint != fingerprint {
				return ErrKeyReused
			}
			stored = Result{Replayed: true, CommandID: commandID}
			return nil
		case !errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("read idempotency record: %w", err)
		}
		return register(ctx, transaction, key, subjectID, fingerprint, now, &stored)
	})
	if err != nil {
		return Result{}, err
	}
	return stored, nil
}

func register(ctx context.Context, transaction *db.ImmediateTx, key, subjectID, fingerprint string, now time.Time, result *Result) error {
	timestamp := now.UTC().Format(time.RFC3339)
	commandID := fmt.Sprintf("cmd-%s-%012x", fingerprint[:16], now.UTC().UnixNano())
	if _, err := transaction.ExecContext(ctx, `INSERT INTO api_commands(command_id, command_type, subject_id, aggregate_type, command_status, request_fingerprint, created_at, started_at, expires_at) VALUES (?, 'IDEMPOTENT_COMMAND', ?, NULL, 'ACCEPTED', ?, ?, ?, ?)`,
		commandID, subjectID, fingerprint, timestamp, timestamp, now.UTC().Add(RecordTTL).Format(time.RFC3339)); err != nil {
		return fmt.Errorf("insert command: %w", err)
	}
	if _, err := transaction.ExecContext(ctx, `INSERT INTO api_idempotency_records(idempotency_key, subject_id, request_fingerprint, command_id, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?)`,
		key, subjectID, fingerprint, commandID, timestamp, now.UTC().Add(RecordTTL).Format(time.RFC3339)); err != nil {
		return fmt.Errorf("insert idempotency record: %w", err)
	}
	result.Replayed = false
	result.CommandID = commandID
	return nil
}

// Complete 把命令推进到终态（SUCCEEDED/FAILED + result/error），供执行方在
// 动作完成后调用；重放请求不经过 Complete。状态机约束为
// ACCEPTED -> RUNNING -> 终态，每跳 revision 单调递增。
func Complete(ctx context.Context, database *sql.DB, commandID, errorCode, resultJSON string, now time.Time) error {
	return db.RunImmediate(ctx, database, func(ctx context.Context, transaction *db.ImmediateTx) error {
		timestamp := now.UTC().Format(time.RFC3339)
		if _, err := transaction.ExecContext(ctx, `UPDATE api_commands SET command_status='RUNNING', revision=revision+1 WHERE command_id=? AND command_status='ACCEPTED'`, commandID); err != nil {
			return fmt.Errorf("start command: %w", err)
		}
		var resultPtr, errorPtr any
		status := "SUCCEEDED"
		if errorCode != "" {
			status = "FAILED"
			errorPtr = errorCode
		} else {
			resultPtr = resultJSON
		}
		update, err := transaction.ExecContext(ctx, `UPDATE api_commands SET command_status=?, result_json=?, error_code=?, finished_at=?, revision=revision+1 WHERE command_id=? AND command_status='RUNNING'`,
			status, resultPtr, errorPtr, timestamp, commandID)
		if err != nil {
			return fmt.Errorf("complete command: %w", err)
		}
		if rows, err := update.RowsAffected(); err != nil || rows != 1 {
			return ErrCommandFinished
		}
		return nil
	})
}

// Fingerprint 计算请求体指纹（SHA-256 hex）。
func Fingerprint(body []byte) string {
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}

// Result 是 Begin 的结果。
type Result struct {
	Replayed  bool
	CommandID string
}
