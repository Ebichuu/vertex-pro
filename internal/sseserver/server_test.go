package sseserver

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
)

func TestNoLastEventIDSendsReadyAndLiveOnly(t *testing.T) {
	database := openMigrated(t)
	hub := NewHub()
	server := httptest.NewServer(&Server{Database: database, Hub: hub, Config: Config{HeartbeatInterval: time.Hour}})
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	ready := readEvent(t, reader)
	if !strings.Contains(ready, "event: stream.ready") {
		t.Fatalf("first event = %s", ready)
	}
	seedEvent(t, database, "job.state.changed", `{"job_state":"ADD_SENT"}`)
	hub.Publish(Row{EventID: 1, EventType: "job.state.changed", AggregateType: "race_job", AggregateID: "job-1", AggregateRevision: 1, SafeDataJSON: `{"job_state":"ADD_SENT"}`, OccurredAt: time.Now().UTC()})
	live := readEvent(t, reader)
	if !strings.Contains(live, "id: 1") || !strings.Contains(live, `"schema_version":1`) {
		t.Fatalf("live event = %s", live)
	}
}

func TestReplayWithinRetentionThenLive(t *testing.T) {
	database := openMigrated(t)
	hub := NewHub()
	for index := 1; index <= 3; index++ {
		seedEvent(t, database, "job.state.changed", `{"n":`+string(rune('0'+index))+`}`)
	}
	server := httptest.NewServer(&Server{Database: database, Hub: hub, Config: Config{HeartbeatInterval: time.Hour}})
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	request.Header.Set("Last-Event-ID", "1")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	replayed := readEvent(t, reader)
	if !strings.Contains(replayed, "id: 2") {
		t.Fatalf("replay = %s", replayed)
	}
	replayed2 := readEvent(t, reader)
	if !strings.Contains(replayed2, "id: 3") {
		t.Fatalf("replay = %s", replayed2)
	}
	if !strings.Contains(readEvent(t, reader), "event: stream.ready") {
		t.Fatal("ready missing after replay")
	}
}

func TestResetRequiredWhenBeyondCapOrStale(t *testing.T) {
	database := openMigrated(t)
	hub := NewHub()
	for index := 1; index <= 4; index++ {
		seedEvent(t, database, "job.state.changed", "{}")
	}
	server := httptest.NewServer(&Server{Database: database, Hub: hub, Config: Config{MaxReplayEvents: 2, HeartbeatInterval: time.Hour}})
	defer server.Close()

	// 重放超过 5000 上限（此处配置为 2）。
	response := stream(t, server, "1")
	if !strings.Contains(readEvent(t, bufio.NewReader(response.Body)), "event: stream.reset_required") {
		t.Fatal("cap exceeded must reset")
	}
	response.Body.Close()

	// Last-Event-ID 晚于当前最大。
	response = stream(t, server, "999")
	if !strings.Contains(readEvent(t, bufio.NewReader(response.Body)), "event: stream.reset_required") {
		t.Fatal("future id must reset")
	}
	response.Body.Close()
}

func TestResetRequiredWhenOlderThanRetention(t *testing.T) {
	database := openMigrated(t)
	hub := NewHub()
	for index := 1; index <= 7; index++ {
		seedEvent(t, database, "job.state.changed", "{}")
	}
	// 模拟保留清理：删除 event_id 1..4，最老保留为 5。
	if _, err := database.Exec(`DELETE FROM event_stream WHERE event_id <= 4`); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(&Server{Database: database, Hub: hub, Config: Config{HeartbeatInterval: time.Hour}})
	defer server.Close()
	response := stream(t, server, "2")
	body := readEvent(t, bufio.NewReader(response.Body))
	if !strings.Contains(body, "event: stream.reset_required") || !strings.Contains(body, `"oldest_event_id":5`) {
		t.Fatalf("reset = %s", body)
	}
	response.Body.Close()
}

func TestHeartbeatAndSlowClientDrop(t *testing.T) {
	database := openMigrated(t)
	hub := NewHub()
	server := httptest.NewServer(&Server{Database: database, Hub: hub, Config: Config{HeartbeatInterval: 40 * time.Millisecond}})
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	readEvent(t, reader) // ready
	if !strings.Contains(readEvent(t, reader), ": heartbeat") {
		t.Fatal("heartbeat comment missing")
	}
	// 慢客户端：不读的订阅者在缓冲耗尽后被摘除。
	slow, unsubscribe := hub.Subscribe()
	_ = unsubscribe
	for index := 0; index < 100; index++ {
		hub.Publish(Row{EventID: 0, EventType: EventRealtime, AggregateType: "instance", AggregateID: "qb-1", SafeDataJSON: "{}", OccurredAt: time.Now().UTC()})
	}
	if hub.SubscriberCount() != 0 {
		t.Fatal("slow subscriber was not dropped")
	}
	for range slow {
		// 摘除后仍需排空缓冲中的遗留事件。
	}
}

func TestClientDisconnectUnsubscribes(t *testing.T) {
	database := openMigrated(t)
	hub := NewHub()
	server := httptest.NewServer(&Server{Database: database, Hub: hub, Config: Config{HeartbeatInterval: time.Hour}})
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(response.Body)
	readEvent(t, reader) // ready
	time.Sleep(50 * time.Millisecond)
	if hub.SubscriberCount() != 1 {
		t.Fatalf("subscribers = %d", hub.SubscriberCount())
	}
	cancel()
	time.Sleep(80 * time.Millisecond)
	if hub.SubscriberCount() != 0 {
		t.Fatalf("subscribers after disconnect = %d", hub.SubscriberCount())
	}
	response.Body.Close()
}

func stream(t *testing.T, server *httptest.Server, lastEventID string) *http.Response {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	if lastEventID != "" {
		request.Header.Set("Last-Event-ID", lastEventID)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func readEvent(t *testing.T, reader *bufio.Reader) string {
	t.Helper()
	event := &strings.Builder{}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				t.Fatal("timed out waiting for event")
			}
			t.Fatalf("read event: %v (partial=%s)", err, event.String())
		}
		event.WriteString(line)
		if line == "\n" {
			if strings.TrimSpace(event.String()) == "" {
				continue
			}
			return event.String()
		}
	}
}

func seedEvent(t *testing.T, database *sql.DB, eventType, payload string) int64 {
	t.Helper()
	var next int64
	if err := database.QueryRow(`SELECT COALESCE(MAX(aggregate_revision) + 1, 0) FROM event_stream WHERE aggregate_type = 'race_job' AND aggregate_id = 'job-1'`).Scan(&next); err != nil {
		t.Fatal(err)
	}
	result, err := database.Exec(`INSERT INTO event_stream(event_type, aggregate_type, aggregate_id, aggregate_revision, safe_data_json, occurred_at, expires_at) VALUES (?, 'race_job', 'job-1', ?, ?, ?, ?)`,
		eventType, next, payload,
		eventType, payload, time.Now().UTC().Format(time.RFC3339), time.Now().UTC().Add(7*24*time.Hour).Format(time.RFC3339))
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func openMigrated(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.Open(context.Background(), db.Config{Path: filepath.Join(t.TempDir(), "sse.db"), MaxOpenConns: 1, MaxIdleConns: 1})
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
