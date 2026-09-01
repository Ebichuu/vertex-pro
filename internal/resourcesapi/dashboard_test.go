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

func TestDashboardInstanceCardsScaleToTwentyInstances(t *testing.T) {
	database := openMigrated(t)
	seedResourceFixture(t, database)
	timestamp := time.Date(2026, 8, 29, 7, 0, 0, 0, time.UTC).Format(time.RFC3339)
	for index := 2; index <= 20; index++ {
		id := fmt.Sprintf("qb-%d", index)
		state := "READY"
		switch {
		case index%5 == 0:
			state = "STALE"
		case index%7 == 0:
			state = "OFFLINE"
		}
		statements := []struct {
			query string
			args  []any
		}{
			{"INSERT INTO sync_instance_state(instance_id, sync_state, sync_generation, rid, snapshot_revision, snapshot_digest, updated_at) VALUES (?, ?, 5, 0, 2, ?, ?)", []any{id, state, "d-" + id, timestamp}},
			{"INSERT INTO storage_targets(storage_target_id, instance_id, path, storage_verification_state, created_at, updated_at) VALUES (?, ?, ?, 'VERIFIED', ?, ?)", []any{"target-" + id, id, "/data/" + id, timestamp, timestamp}},
		}
		for _, statement := range statements {
			if _, err := database.Exec(statement.query, statement.args...); err != nil {
				t.Fatal(err)
			}
		}
	}
	auth := newAuth(t, database)
	session := login(t, auth)
	router := Router(Deps{Database: database, Auth: auth, Web: &httpapi.WebProtection{AllowedHosts: []string{"vertex.local"}}, Now: func() time.Time { return time.Date(2026, 8, 29, 8, 0, 0, 0, time.UTC) }})
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard", nil)
	request.Host = "vertex.local"
	request.AddCookie(&http.Cookie{Name: httpapi.SessionCookieName, Value: session})
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
	}
	var payload struct {
		Instances []struct {
			ID        string   `json:"id"`
			Freshness string   `json:"freshness"`
			Reasons   []string `json:"reason_codes"`
		} `json:"instances"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Instances) != 20 {
		t.Fatalf("instance cards = %d want 20", len(payload.Instances))
	}
	nonFresh := 0
	for _, card := range payload.Instances {
		if card.Freshness != "FRESH" {
			nonFresh++
			if len(card.Reasons) == 0 || card.Reasons[0] != "SNAPSHOT_STALE" {
				t.Fatalf("non-fresh card %s lacks reason: %+v", card.ID, card)
			}
		}
	}
	// 20 个新实例中：5/10/15/20 为 STALE，7/14 为 OFFLINE（7 与 5/10/15/20 无重叠）。
	if nonFresh != 6 {
		t.Fatalf("non-fresh cards = %d want 6", nonFresh)
	}
}
