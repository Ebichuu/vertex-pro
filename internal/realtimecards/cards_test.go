package realtimecards

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/sseserver"
)

func TestUpdateBroadcastsTransientEventWithoutEventID(t *testing.T) {
	hub := sseserver.NewHub()
	subscriber, unsubscribe := hub.Subscribe()
	defer unsubscribe()
	registry := NewRegistry(hub, 30*time.Second)
	now := time.Date(2026, 8, 29, 11, 0, 0, 0, time.UTC)
	registry.Update(Card{InstanceID: "qb-1", SyncState: "READY", DownloadRate: 1024, UploadRate: 128, DownloadingCount: 3, SeedingCount: 5, Source: "SYNC_SERVER_STATE"}, now)
	select {
	case row := <-subscriber:
		if row.EventID != 0 || row.EventType != "stats.realtime" {
			t.Fatalf("row = %+v", row)
		}
		if !strings.Contains(row.SafeDataJSON, `"download_rate":1024`) && !strings.Contains(row.SafeDataJSON, `"DownloadRate":1024`) {
			t.Fatalf("payload = %s", row.SafeDataJSON)
		}
		var decoded map[string]any
		if err := json.Unmarshal([]byte(row.SafeDataJSON), &decoded); err != nil {
			t.Fatalf("payload not JSON: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("no transient event received")
	}
}

func TestFreshnessMarksStaleCardsWithoutFabrication(t *testing.T) {
	registry := NewRegistry(sseserver.NewHub(), 30*time.Second)
	now := time.Date(2026, 8, 29, 11, 0, 0, 0, time.UTC)
	registry.Update(Card{InstanceID: "qb-1", DownloadRate: 1024, Source: "TRANSFER_INFO"}, now)
	fresh := registry.CardForInstance("qb-1", now)
	if fresh.Freshness != "FRESH" {
		t.Fatalf("fresh = %s", fresh.Freshness)
	}
	stale := registry.CardForInstance("qb-1", now.Add(31*time.Second))
	if stale.Freshness != "NOT_FRESH" || stale.DownloadRate != 1024 {
		t.Fatalf("stale = %+v", stale)
	}
	unknown := registry.CardForInstance("qb-2", now)
	if unknown.Freshness != "NOT_FRESH" || unknown.DownloadRate != 0 {
		t.Fatalf("unknown = %+v", unknown)
	}
}
