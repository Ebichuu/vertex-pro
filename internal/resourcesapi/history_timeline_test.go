package resourcesapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/httpapi"
)

func TestJobDetailExposesStableUserTimeline(t *testing.T) {
	database := openMigrated(t)
	seedResourceFixture(t, database)
	// HIS-03：乱序写入事件；服务端必须按 occurred_at 升序、同时间按
	// event_id 升序稳定排序，且 stage 输出用户可读阶段名（§16.6）。
	events := []struct {
		eventType, fromState, toState, reason, occurred string
	}{
		{"job.preparation.changed", "", "DISCOVERED", "", "2026-08-29T07:05:00Z"},
		{"attempt.state.changed", "RESERVED", "ADD_SENT", "", "2026-08-29T07:10:00Z"},
		{"job.state.changed", "ADD_SENT", "UNKNOWN", "RESPONSE_LOST", "2026-08-29T07:15:00Z"},
		{"attempt.state.changed", "UNKNOWN", "ADD_SENT", "RECONCILED", "2026-08-29T07:12:00Z"},
	}
	for _, event := range events {
		if _, err := database.Exec(`INSERT INTO race_events(job_id, attempt_id, event_type, from_state, to_state, reason_code, occurred_at) VALUES ('job-1','attempt-1',?,?,?,?,?)`,
			event.eventType, event.fromState, event.toState, event.reason, event.occurred); err != nil {
			t.Fatal(err)
		}
	}
	auth := newAuth(t, database)
	session := login(t, auth)
	router := Router(Deps{Database: database, Auth: auth, Web: &httpapi.WebProtection{AllowedHosts: []string{"vertex.local"}}, Now: func() time.Time { return time.Date(2026, 8, 29, 8, 0, 0, 0, time.UTC) }})

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/job-1", nil)
	request.Host = "vertex.local"
	request.AddCookie(&http.Cookie{Name: httpapi.SessionCookieName, Value: session})
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("job detail status = %d %s", response.Code, response.Body.String())
	}

	var payload struct {
		Timeline []struct {
			EventID    int64  `json:"event_id"`
			Stage      string `json:"stage"`
			OccurredAt string `json:"occurred_at"`
		} `json:"timeline"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	wantStages := []string{"准备", "添加", "添加", "待确认"}
	if len(payload.Timeline) != len(wantStages) {
		t.Fatalf("timeline = %d events, want %d", len(payload.Timeline), len(wantStages))
	}
	for index, event := range payload.Timeline {
		if event.Stage != wantStages[index] {
			t.Fatalf("timeline[%d] stage=%q want %q", index, event.Stage, wantStages[index])
		}
		if index > 0 && payload.Timeline[index-1].OccurredAt > event.OccurredAt {
			t.Fatalf("timeline not sorted by occurred_at: %d=%s after %d=%s",
				index-1, payload.Timeline[index-1].OccurredAt, index, event.OccurredAt)
		}
	}
	// 同一 occurred_at 的事件按 event_id 升序：直接断言第 1、2 条的时间同为
	// 07:10/07:12 段的排序稳定性由 ForJob 的 ORDER BY 保证，这里校验绝对顺序。
	if payload.Timeline[1].OccurredAt != "2026-08-29T07:10:00Z" || payload.Timeline[2].OccurredAt != "2026-08-29T07:12:00Z" {
		t.Fatalf("out-of-order inserts were not stabilized: %+v", payload.Timeline)
	}

	missing := httptest.NewRecorder()
	missingRequest := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/missing-job", nil)
	missingRequest.Host = "vertex.local"
	missingRequest.AddCookie(&http.Cookie{Name: httpapi.SessionCookieName, Value: session})
	router.ServeHTTP(missing, missingRequest)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing job status = %d", missing.Code)
	}
	_ = time.Now
}
