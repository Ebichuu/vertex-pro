package resourcesapi

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"

	"github.com/vertex-pro/vertex-pro/internal/apipaging"
	"github.com/vertex-pro/vertex-pro/internal/historyproject"
	"github.com/vertex-pro/vertex-pro/internal/httpapi"
)

func readPage(request *http.Request) (int, apipaging.Cursor, error) {
	limit := 50
	if raw := request.URL.Query().Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 200 {
			return 0, apipaging.Cursor{}, fmt.Errorf("limit must be between 1 and 200")
		}
		limit = value
	}
	var cursor apipaging.Cursor
	if raw := request.URL.Query().Get("cursor"); raw != "" {
		var err error
		cursor, err = apipaging.Decode(raw)
		if err != nil || cursor.ID == "" || cursor.SortKey == "" {
			return 0, apipaging.Cursor{}, fmt.Errorf("cursor is invalid")
		}
	}
	return limit, cursor, nil
}

func (d Deps) writePage(response http.ResponseWriter, request *http.Request, items []map[string]any, key func(map[string]any) string) {
	limit, cursor, err := readPage(request)
	if err != nil {
		httpapi.WriteError(response, request, "INVALID_REQUEST", err.Error())
		return
	}
	if key == nil {
		key = func(item map[string]any) string { return fmt.Sprint(item["id"]) }
	}
	sort.SliceStable(items, func(i, j int) bool { return key(items[i]) < key(items[j]) })
	paged := make([]map[string]any, 0, limit)
	next := ""
	for _, item := range items {
		if cursor.SortKey != "" && key(item) <= cursor.SortKey {
			continue
		}
		if len(paged) == limit {
			last := paged[len(paged)-1]
			next = apipaging.Encode(apipaging.Cursor{SortKey: key(last), ID: fmt.Sprint(last["id"])})
			break
		}
		paged = append(paged, item)
	}
	d.writeData(response, request, map[string]any{"items": paged, "next_cursor": next})
}

func historyPayload(entry historyproject.Entry) map[string]any {
	return map[string]any{"id": entry.JobID, "title": entry.Title, "site": entry.SiteName, "downloader": entry.InstanceID, "user_status": string(entry.UserStatus.ID), "user_status_label": entry.UserStatus.Label, "discovered_at": entry.DiscoveredAt, "added_at": entry.AddedAt, "confirmed_at": entry.ConfirmedAt, "finished_at": entry.FinishedAt, "deleted_at": entry.DeletedAt, "deletion_source": entry.DeletionSource, "attempt_count": entry.AttemptCount}
}
