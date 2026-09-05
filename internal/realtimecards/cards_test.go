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

// STAT-05 验收：慢/断线客户端。慢订阅者（停止读取）在缓冲耗尽后被摘除，
// 不阻塞生产者；新订阅者照常收到实时事件；REST 快照（Snapshot/
// CardForInstance）始终可用作断线重建源，且未上报实例保持 NOT_FRESH。
func TestSlowSubscriberDroppedAndSnapshotRebuilds(t *testing.T) {
	hub := sseserver.NewHub()
	slow, unsubscribeSlow := hub.Subscribe()
	defer unsubscribeSlow()
	registry := NewRegistry(hub, 30*time.Second)
	now := time.Date(2026, 8, 29, 11, 0, 0, 0, time.UTC)

	for index := 0; index < 200; index++ {
		registry.Update(Card{InstanceID: "qb-1", SyncState: "READY", DownloadRate: int64(index), Source: "SYNC_SERVER_STATE"}, now.Add(time.Duration(index)*time.Second))
	}
	// 慢订阅者被摘除后其通道被关闭：排干缓冲后读到关闭信号。
	drained := 0
	dropped := false
	for {
		select {
		case _, ok := <-slow:
			if !ok {
				dropped = true
			} else {
				drained++
			}
			if drained > 64+8 {
				t.Fatalf("slow subscriber buffered %d rows, want <= 64", drained)
			}
		default:
			if !dropped {
				t.Fatalf("slow subscriber was not dropped after overflow")
			}
			// 收到关闭信号或已排干且未再投递；继续走断言。
			if !dropped {
				t.Fatal("unreachable")
			}
		}
		if dropped || drained > 64 {
			break
		}
	}

	fresh, unsubscribeFresh := hub.Subscribe()
	defer unsubscribeFresh()
	registry.Update(Card{InstanceID: "qb-1", SyncState: "READY", DownloadRate: 4096, Source: "SYNC_SERVER_STATE"}, now.Add(time.Minute))
	select {
	case row := <-fresh:
		if row.EventID != 0 {
			t.Fatalf("transient event carries an event id: %+v", row)
		}
	case <-time.After(time.Second):
		t.Fatal("fresh subscriber received no live update")
	}

	// 断线客户端由 REST 快照重建：最新卡片 FRESH，未知实例 NOT_FRESH。
	view := registry.CardForInstance("qb-1", now.Add(time.Minute))
	if view.Freshness != "FRESH" || view.DownloadRate != 4096 {
		t.Fatalf("rebuild view = %+v", view)
	}
	if unknown := registry.CardForInstance("qb-unknown", now.Add(time.Minute)); unknown.Freshness != "NOT_FRESH" {
		t.Fatalf("unknown instance = %+v", unknown)
	}
}
