// Package historytimeline 构建 History Detail 的投递时间线（HIS-03）：
// 发现/准备/Add/确认/上传/删除等事件，按发生时间与事件 ID 稳定排序。
package historytimeline

import (
	"context"
	"database/sql"
	"fmt"
)

// Event 是时间线上的一条事件。
type Event struct {
	EventID    int64  `json:"event_id"`
	EventType  string `json:"event_type"`
	FromState  string `json:"from_state,omitempty"`
	ToState    string `json:"to_state,omitempty"`
	ReasonCode string `json:"reason_code,omitempty"`
	OccurredAt string `json:"occurred_at"`
}

// Stage 把内部事件映射为用户可读的阶段名（§16.6：普通视图无内部术语）。
func (e Event) Stage() string {
	switch e.EventType {
	case "observation":
		switch e.ToState {
		case "DELETED":
			return "删除"
		case "SEEDING", "COMPLETED":
			return "上传"
		case "DOWNLOADING":
			return "下载"
		case "DISCOVERED":
			return "发现"
		}
		return "状态变更"
	case "job.preparation.changed":
		return "准备"
	case "job.state.changed", "attempt.state.changed":
		switch e.ToState {
		case "RESERVED", "ADD_SENT":
			return "添加"
		case "AWAITING_CONFIRMATION", "HASH_SEEN", "RECONCILED_CONFIRMED":
			return "确认"
		case "ALREADY_PRESENT":
			return "已存在"
		case "ADD_FAILED", "RECONCILED_NO_EFFECT":
			return "失败"
		case "UNKNOWN", "EXPIRED_UNKNOWN":
			return "待确认"
		default:
			return "状态变更"
		}
	default:
		return "状态变更"
	}
}

// ForJob 返回单个 Job 的全部时间线事件：先按 occurred_at 升序，
// 相同发生时间按 event_id 升序（稳定排序，HIS-03 验收项）。
func ForJob(ctx context.Context, database *sql.DB, jobID string) ([]Event, error) {
	if database == nil {
		return nil, fmt.Errorf("database is required")
	}
	rows, err := database.QueryContext(ctx, `SELECT event_id,event_type,from_state,to_state,reason_code,occurred_at FROM (
 SELECT event_id,event_type,COALESCE(from_state,'') AS from_state,COALESCE(to_state,'') AS to_state,COALESCE(reason_code,'') AS reason_code,occurred_at FROM race_events WHERE job_id=?
 UNION ALL SELECT 1000000000000+id,'observation','',state,'SYNC_SNAPSHOT',observed_at FROM job_observation_events WHERE job_id=?
 ) ORDER BY julianday(occurred_at),event_id`, jobID, jobID)
	if err != nil {
		return nil, fmt.Errorf("query timeline: %w", err)
	}
	defer rows.Close()
	events := make([]Event, 0)
	for rows.Next() {
		var event Event
		if err := rows.Scan(&event.EventID, &event.EventType, &event.FromState, &event.ToState, &event.ReasonCode, &event.OccurredAt); err != nil {
			return nil, fmt.Errorf("scan timeline event: %w", err)
		}
		events = append(events, event)
	}
	return events, rows.Err()
}
