package resourcesapi

import (
	"database/sql"
	"fmt"
	"net/http"
	"time"
)

// scanCard 组装单个实例的 Dashboard 卡片（§18.2 最低字段）。
func scanCard(rows *sql.Rows, card *map[string]any, d Deps, request *http.Request) error {
	var instanceID, syncState string
	var revision int64
	var capacityState string
	var physicalFree, effectiveFree int64
	var storageState string
	if err := rows.Scan(&instanceID, &syncState, &revision, &capacityState, &physicalFree, &effectiveFree, &storageState); err != nil {
		return fmt.Errorf("scan instance card: %w", err)
	}
	view := map[string]any{
		"id":                   instanceID,
		"sync_state":           syncState,
		"snapshot_revision":    revision,
		"freshness":            freshnessFor(syncState),
		"capacity_state":       capacityState,
		"physical_free_bytes":  physicalFree,
		"effective_free_bytes": effectiveFree,
		"storage_state":        storageState,
		"reason_codes":         qualityReasons(syncState, capacityState, storageState),
		"download_rate":        int64(0),
		"upload_rate":          int64(0),
		"downloading_count":    0,
		"seeding_count":        0,
		"realtime_freshness":   "NOT_FRESH",
		"realtime_source":      "",
	}
	if d.Realtime != nil {
		now := time.Now()
		if d.Now != nil {
			now = d.Now()
		}
		card2 := d.Realtime.CardForInstance(instanceID, now)
		view["download_rate"] = card2.DownloadRate
		view["upload_rate"] = card2.UploadRate
		view["downloading_count"] = card2.DownloadingCount
		view["seeding_count"] = card2.SeedingCount
		view["realtime_freshness"] = card2.Freshness
		view["realtime_source"] = card2.Source
	}
	*card = view
	return nil
}
