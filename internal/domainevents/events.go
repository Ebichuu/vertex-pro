package domainevents

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
)

// 事件类型按合同划分：race_events 三类由 Job/Attempt 聚合产生；其余类型
// 只进入可重放 event_stream。
var raceEventTypes = map[string]bool{
	"job.preparation.changed": true,
	"job.state.changed":       true,
	"attempt.state.changed":   true,
}

var aggregateTypes = map[string]bool{
	"race_job": true, "placement_attempt": true, "api_command": true,
	"storage_target": true, "instance": true, "system": true,
}

var ErrEventInvalid = errors.New("domain event is invalid")

// Entry 是一次状态迁移伴随的领域事件。SafeDataJSON 必须已脱敏。
type Entry struct {
	EventType         string
	AggregateType     string
	AggregateID       string
	AggregateRevision int64
	JobID             string
	AttemptID         string
	FromState         string
	ToState           string
	ReasonCode        string
	SafeDataJSON      string
	OccurredAt        time.Time
	ExpiresAt         time.Time
}

// Emit 在调用方事务内同时写入 race_events 与 event_stream：两行要么随
// 事务一起提交、要么一起回滚，禁止"先改状态、稍后补事件"。
func Emit(transaction *db.ImmediateTx, entry Entry) error {
	if err := validate(entry); err != nil {
		return err
	}
	timestamp := entry.OccurredAt.UTC().Format(time.RFC3339)
	expiresAt := entry.ExpiresAt.UTC().Format(time.RFC3339)
	// event_stream 的聚合修订号由触发器强制单调：本事务内按当前最大值 +1 计算。
	var aggregateRevision int64
	if err := transaction.QueryRowContext(context.Background(), `SELECT COALESCE(MAX(aggregate_revision) + 1, 0) FROM event_stream WHERE aggregate_type = ? AND aggregate_id = ?`, entry.AggregateType, entry.AggregateID).Scan(&aggregateRevision); err != nil {
		return fmt.Errorf("compute aggregate revision: %w", err)
	}
	if raceEventTypes[entry.EventType] {
		attemptID := any(nil)
		if strings.TrimSpace(entry.AttemptID) != "" {
			attemptID = entry.AttemptID
		}
		if _, err := transaction.ExecContext(context.Background(), `INSERT INTO race_events(job_id, attempt_id, event_type, from_state, to_state, reason_code, safe_metadata_json, occurred_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			entry.JobID, attemptID, entry.EventType, entry.FromState, entry.ToState, entry.ReasonCode, entry.SafeDataJSON, timestamp); err != nil {
			return fmt.Errorf("insert race event: %w", err)
		}
	}
	if _, err := transaction.ExecContext(context.Background(), `INSERT INTO event_stream(event_type, aggregate_type, aggregate_id, aggregate_revision, safe_data_json, occurred_at, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		entry.EventType, entry.AggregateType, entry.AggregateID, aggregateRevision, entry.SafeDataJSON, timestamp, expiresAt); err != nil {
		return fmt.Errorf("insert event stream row: %w", err)
	}
	return nil
}

// Append 在独立事务中写入单条事件（无状态迁移的独立告警/健康事件）。
func Append(ctx context.Context, database *sql.DB, entry Entry) error {
	if database == nil {
		return errors.New("database is required")
	}
	return db.RunImmediate(ctx, database, func(ctx context.Context, transaction *db.ImmediateTx) error {
		return Emit(transaction, entry)
	})
}

func validate(entry Entry) error {
	if strings.TrimSpace(entry.EventType) == "" {
		return fmt.Errorf("%w: event type is required", ErrEventInvalid)
	}
	if !aggregateTypes[entry.AggregateType] {
		return fmt.Errorf("%w: aggregate type %s", ErrEventInvalid, entry.AggregateType)
	}
	if strings.TrimSpace(entry.AggregateID) == "" || strings.TrimSpace(entry.SafeDataJSON) == "" {
		return fmt.Errorf("%w: aggregate id and safe data are required", ErrEventInvalid)
	}
	if entry.AggregateRevision < 0 {
		return fmt.Errorf("%w: aggregate revision is negative", ErrEventInvalid)
	}
	if entry.OccurredAt.IsZero() || entry.ExpiresAt.IsZero() || !entry.ExpiresAt.After(entry.OccurredAt) {
		return fmt.Errorf("%w: event times are invalid", ErrEventInvalid)
	}
	if raceEventTypes[entry.EventType] && strings.TrimSpace(entry.JobID) == "" {
		return fmt.Errorf("%w: race event requires job id", ErrEventInvalid)
	}
	return nil
}
