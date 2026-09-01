package sseserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	EventReady         = "stream.ready"
	EventResetRequired = "stream.reset_required"
	EventRealtime      = "stats.realtime"
)

// Row 是 event_stream 的一行（或一条瞬时实时事件的内存表示）。
type Row struct {
	EventID           int64
	EventType         string
	AggregateType     string
	AggregateID       string
	AggregateRevision int64
	SafeDataJSON      string
	OccurredAt        time.Time
}

// Transient 标记不占 event ID、不可重放的瞬时事件（stats.realtime）。
func (r Row) Transient() bool { return r.EventID == 0 }

type Config struct {
	MaxReplayEvents   int
	HeartbeatInterval time.Duration
	WriteTimeout      time.Duration
}

// Hub 是进程内事件总线；订阅者通道满即被摘除并关闭（慢客户端策略），
// 客户端凭 Last-Event-ID 重连后由重放恢复。
type Hub struct {
	mu          sync.Mutex
	subscribers map[chan Row]struct{}
}

func NewHub() *Hub {
	return &Hub{subscribers: make(map[chan Row]struct{})}
}

func (h *Hub) Subscribe() (chan Row, func()) {
	channel := make(chan Row, 64)
	h.mu.Lock()
	h.subscribers[channel] = struct{}{}
	h.mu.Unlock()
	return channel, func() {
		h.mu.Lock()
		if _, ok := h.subscribers[channel]; ok {
			delete(h.subscribers, channel)
			close(channel)
		}
		h.mu.Unlock()
	}
}

func (h *Hub) Publish(row Row) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for channel := range h.subscribers {
		select {
		case channel <- row:
		default:
			// 慢客户端：缓冲已满，摘除并关闭，客户端断线重连恢复。
			delete(h.subscribers, channel)
			close(channel)
		}
	}
}

func (h *Hub) SubscriberCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subscribers)
}

// Server 实现 GET /api/v1/events/stream 的服务端协议（设计 §16.6）。
type Server struct {
	Database *sql.DB
	Hub      *Hub
	Config   Config
	Now      func() time.Time
}

func (s *Server) config() Config {
	config := s.Config
	if config.MaxReplayEvents <= 0 {
		config.MaxReplayEvents = 5000
	}
	if config.HeartbeatInterval <= 0 {
		config.HeartbeatInterval = 15 * time.Second
	}
	if config.WriteTimeout <= 0 {
		config.WriteTimeout = 10 * time.Second
	}
	return config
}

func (s *Server) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	if s.Hub == nil || s.Database == nil {
		http.Error(response, "event stream is not configured", http.StatusServiceUnavailable)
		return
	}
	flusher, ok := response.(http.Flusher)
	if !ok {
		http.Error(response, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	config := s.config()
	now := s.Now
	if now == nil {
		now = time.Now
	}
	header := response.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-cache")
	header.Set("Connection", "keep-alive")
	response.WriteHeader(http.StatusOK)
	flusher.Flush()

	write := func(payload string) error {
		if deadlineSetter, ok := response.(interface{ SetWriteDeadline(time.Time) error }); ok {
			if err := deadlineSetter.SetWriteDeadline(now().Add(config.WriteTimeout)); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprint(response, payload); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}

	lastEventID := parseLastEventID(request)
	minID, maxID := s.streamBounds(request.Context())

	// 先订阅再重放，避免重放与订阅之间漏事件；live 循环按 ID 去重。
	live, unsubscribe := s.Hub.Subscribe()
	defer unsubscribe()

	if lastEventID > 0 {
		reset, payload := s.replayPlan(request.Context(), lastEventID, minID, maxID, config.MaxReplayEvents)
		if reset {
			_ = write(sseEvent(EventResetRequired, 0, payload))
			return
		}
		replayed, err := s.replay(request.Context(), lastEventID, config.MaxReplayEvents)
		if err != nil {
			return
		}
		sentIDs := make(map[int64]bool, len(replayed))
		for _, row := range replayed {
			if err := write(sseRow(row)); err != nil {
				return
			}
			sentIDs[row.EventID] = true
		}
		_ = write(sseEvent(EventReady, 0, readyPayload(maxID)))
		_ = write("")
		s.liveLoop(request.Context(), live, write, sentIDs, config, now)
		return
	}
	_ = write(sseEvent(EventReady, 0, readyPayload(maxID)))
	_ = write("")
	s.liveLoop(request.Context(), live, write, map[int64]bool{}, config, now)
}

func (s *Server) liveLoop(ctx context.Context, live chan Row, write func(string) error, sent map[int64]bool, config Config, now func() time.Time) {
	heartbeat := time.NewTicker(config.HeartbeatInterval)
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case row, ok := <-live:
			if !ok {
				return
			}
			if row.Transient() {
				_ = write(sseRow(row))
				continue
			}
			if sent[row.EventID] {
				continue
			}
			sent[row.EventID] = true
			if err := write(sseRow(row)); err != nil {
				return
			}
		case <-heartbeat.C:
			if err := write(fmt.Sprintf(": heartbeat %s\n\n", now().UTC().Format(time.RFC3339))); err != nil {
				return
			}
		}
	}
}

func (s *Server) streamBounds(ctx context.Context) (int64, int64) {
	var minID, maxID sql.NullInt64
	_ = s.Database.QueryRowContext(ctx, `SELECT MIN(event_id), MAX(event_id) FROM event_stream`).Scan(&minID, &maxID)
	return minID.Int64, maxID.Int64
}

// replayPlan 判定 Last-Event-ID 是否可续传：晚于当前最大、早于最老保留
// 事件或重放超限时返回 reset_required。
func (s *Server) replayPlan(ctx context.Context, lastEventID, minID, maxID int64, maxReplay int) (bool, string) {
	if lastEventID > maxID {
		return true, resetPayload(minID, maxID)
	}
	if minID > 0 && lastEventID < minID-1 {
		return true, resetPayload(minID, maxID)
	}
	count := 0
	_ = s.Database.QueryRowContext(ctx, `SELECT COUNT(*) FROM event_stream WHERE event_id > ?`, lastEventID).Scan(&count)
	if count > maxReplay {
		return true, resetPayload(minID, maxID)
	}
	return false, ""
}

func (s *Server) replay(ctx context.Context, lastEventID int64, maxReplay int) ([]Row, error) {
	rows, err := s.Database.QueryContext(ctx, `SELECT event_id, event_type, aggregate_type, aggregate_id, aggregate_revision, safe_data_json, occurred_at FROM event_stream WHERE event_id > ? ORDER BY event_id LIMIT ?`, lastEventID, maxReplay)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]Row, 0)
	for rows.Next() {
		var row Row
		var occurredAt string
		if err := rows.Scan(&row.EventID, &row.EventType, &row.AggregateType, &row.AggregateID, &row.AggregateRevision, &row.SafeDataJSON, &occurredAt); err != nil {
			return nil, err
		}
		row.OccurredAt, _ = time.Parse(time.RFC3339, occurredAt)
		result = append(result, row)
	}
	return result, rows.Err()
}

func parseLastEventID(request *http.Request) int64 {
	header := strings.TrimSpace(request.Header.Get("Last-Event-ID"))
	if header == "" {
		return 0
	}
	value, err := strconv.ParseInt(header, 10, 64)
	if err != nil || value < 0 {
		return 0
	}
	return value
}

func readyPayload(currentEventID int64) string {
	payload, _ := json.Marshal(map[string]any{"schema_version": 1, "current_event_id": currentEventID})
	return string(payload)
}

func resetPayload(oldestEventID, currentEventID int64) string {
	payload, _ := json.Marshal(map[string]any{"schema_version": 1, "oldest_event_id": oldestEventID, "current_event_id": currentEventID})
	return string(payload)
}

func sseEvent(name string, eventID int64, data string) string {
	builder := &strings.Builder{}
	builder.WriteString("event: " + name + "\n")
	if eventID > 0 {
		builder.WriteString("id: " + strconv.FormatInt(eventID, 10) + "\n")
	}
	builder.WriteString("data: " + strings.ReplaceAll(data, "\n", " ") + "\n\n")
	return builder.String()
}

// sseRow 按合同编码一行事件：data 为单行 UTF-8 JSON（schema_version=1），
// 瞬时事件不带 id。
func sseRow(row Row) string {
	data, _ := json.Marshal(map[string]any{
		"schema_version": 1,
		"event_id":       row.EventID,
		"occurred_at":    row.OccurredAt.UTC().Format(time.RFC3339Nano),
		"aggregate_type": row.AggregateType,
		"aggregate_id":   row.AggregateID,
		"revision":       row.AggregateRevision,
		"payload":        json.RawMessage(row.SafeDataJSON),
	})
	builder := &strings.Builder{}
	if !row.Transient() {
		builder.WriteString("id: " + strconv.FormatInt(row.EventID, 10) + "\n")
	}
	builder.WriteString("event: " + row.EventType + "\n")
	builder.WriteString("data: " + string(data) + "\n\n")
	return builder.String()
}
