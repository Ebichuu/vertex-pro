package resourcesapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/httpapi"
)

// TestHistoryListCursorFiltersAndDeletionRetention 覆盖 UI-07 验收：
// cursor 分页、筛选与删除后仍保留。
func TestHistoryListCursorFiltersAndDeletionRetention(t *testing.T) {
	database := openMigrated(t)
	seedResourceFixture(t, database)
	timestamp := time.Date(2026, 8, 29, 7, 0, 0, 0, time.UTC).Format(time.RFC3339)
	for index := 1; index <= 5; index++ {
		jobID := fmt.Sprintf("history-job-%d", index)
		if _, err := database.Exec(`INSERT INTO race_jobs(job_id, source_id, identity_key, job_state, rule_state, torrent_state, placement_state, observation_state, participation_deadline, observation_deadline, created_at, updated_at) VALUES (?, 'src-1', 'identity-1', 'DISCOVERED', 'PENDING', 'PENDING', 'PENDING', 'NOT_STARTED', ?, ?, ?, ?)`,
			jobID, timestamp, timestamp, timestamp, timestamp); err != nil {
			t.Fatal(err)
		}
	}
	// 删除事实：history-job-1 已被权威删除。
	if _, err := database.Exec(`INSERT INTO history_deletion_facts(fact_id, job_id, identity_key, qb_torrent_id, storage_target_id, deletion_source, deleted_at, last_known_observation, last_uploaded_bytes, last_downloaded_bytes, last_seen_snapshot_digest, last_seen_snapshot_revision, created_at) VALUES ('fact-1','history-job-1','identity-1','torrent-x','target-1','QB_USER_REMOVED','2026-08-29T09:00:00Z','SEEDING',4096,2048,'after',2,?)`, timestamp); err != nil {
		t.Fatal(err)
	}
	auth := newAuth(t, database)
	session := login(t, auth)
	router := Router(Deps{Database: database, Auth: auth, Web: &httpapi.WebProtection{AllowedHosts: []string{"vertex.local"}}, Now: func() time.Time { return time.Date(2026, 8, 29, 8, 0, 0, 0, time.UTC) }})
	fetch := func(path string) (items []map[string]any, next string) {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Host = "vertex.local"
		request.AddCookie(&http.Cookie{Name: httpapi.SessionCookieName, Value: session})
		router.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
		}
		var payload struct {
			Items      []map[string]any `json:"items"`
			NextCursor string           `json:"next_cursor"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		return payload.Items, payload.NextCursor
	}

	page1, next := fetch("/api/v1/jobs?limit=3")
	if len(page1) != 3 || next == "" {
		t.Fatalf("page1 = %d items, next = %q", len(page1), next)
	}
	page2, next2 := fetch("/api/v1/jobs?limit=3&cursor=" + next)
	if len(page2) == 0 || next2 != "" {
		t.Fatalf("page2 = %d items, next = %q", len(page2), next2)
	}
	seen := map[string]bool{}
	for _, item := range append(append([]map[string]any{}, page1...), page2...) {
		id := item["id"].(string)
		if seen[id] {
			t.Fatalf("duplicate job across pages: %s", id)
		}
		seen[id] = true
	}

	// 筛选：user_status=WAITING_RULE 应包含 DISCOVERED 任务、排除 HASH_SEED 任务。
	filtered, _ := fetch("/api/v1/jobs?user_status=WAITING_RULE")
	if len(filtered) == 0 {
		t.Fatal("WAITING_RULE filter matched nothing")
	}
	for _, item := range filtered {
		if item["user_status"] != "WAITING_RULE" {
			t.Fatalf("filter leaked status %v", item["user_status"])
		}
	}

	// 删除后仍保留：已删除任务仍在列表且状态为已删除、带删除时间与来源。
	// OR groups apply before pagination; intervening WAITING_RULE rows cannot
	// truncate a page or hide a matching later record.
	group1, groupNext := fetch("/api/v1/jobs?limit=1&user_status=DELETED,NEEDS_CONFIRMATION")
	if len(group1) != 1 || groupNext == "" {
		t.Fatalf("group page1=%v cursor=%q", group1, groupNext)
	}
	group2, groupEnd := fetch("/api/v1/jobs?limit=1&user_status=DELETED,NEEDS_CONFIRMATION&cursor=" + groupNext)
	if len(group2) != 1 || groupEnd != "" || group1[0]["id"] == group2[0]["id"] {
		t.Fatalf("group page2=%v cursor=%q", group2, groupEnd)
	}
	groupIDs := map[any]bool{group1[0]["id"]: true, group2[0]["id"]: true}
	if !groupIDs["job-1"] || !groupIDs["history-job-1"] {
		t.Fatalf("wrong grouped jobs=%v", groupIDs)
	}

	var deletedEntry map[string]any
	for _, item := range append(page1, page2...) {
		if item["id"] == "history-job-1" {
			deletedEntry = item
		}
	}
	if deletedEntry == nil {
		t.Fatal("deleted job missing from history")
	}
	if deletedEntry["user_status"] != "DELETED" || deletedEntry["deleted_at"] == "" || deletedEntry["deletion_source"] != "QB_USER_REMOVED" {
		t.Fatalf("deleted entry = %+v", deletedEntry)
	}
}
