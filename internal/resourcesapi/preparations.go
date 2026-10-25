package resourcesapi

import (
	"database/sql"
	"encoding/json"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/httpapi"
	"github.com/vertex-pro/vertex-pro/internal/observability"
	"github.com/vertex-pro/vertex-pro/internal/rsssource"
)

var preparationCode = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,79}$`)

// Cursor contains only a local row number; source keys, URLs and credentials
// remain private. A fixed page can be refreshed without moving its boundaries.
func (d Deps) listPreparations(w http.ResponseWriter, r *http.Request) {
	service, err := d.rssService()
	if err != nil {
		httpapi.WriteError(w, r, "INTERNAL_ERROR", "feed service unavailable")
		return
	}
	if _, err = service.GetFeed(r.Context(), r.PathValue("id")); err != nil {
		writeFeedReadError(w, r, err)
		return
	}
	before := int64(math.MaxInt64)
	limit := 50
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		before, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || before <= 0 {
			httpapi.WriteError(w, r, "VALIDATION_FAILED", "invalid cursor")
			return
		}
	}
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 100 {
			httpapi.WriteError(w, r, "VALIDATION_FAILED", "limit must be 1..100")
			return
		}
	}
	rows, err := d.Database.QueryContext(r.Context(), `SELECT c.rowid,c.source_json,c.state,c.attempts,c.next_attempt_at,c.deadline_at,c.last_error_code,c.discovered_at,c.matched,c.rate_wait_started_at,c.rate_wait_ms,c.preparation_started_at,
 COALESCE((SELECT j.job_id FROM race_jobs j WHERE j.source_id=c.site_id||'/'||c.source_key LIMIT 1),'')
 FROM rss_source_consumers c WHERE c.feed_id=? AND c.rowid<? ORDER BY c.rowid DESC LIMIT ?`, r.PathValue("id"), before, limit+1)
	if err != nil {
		httpapi.WriteError(w, r, "INTERNAL_ERROR", "preparation state unavailable")
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0, limit)
	next := ""
	var lastID int64
	now := d.currentTime()
	for rows.Next() {
		if len(items) == limit {
			next = strconv.FormatInt(lastID, 10)
			break
		}
		var id, waitMS int64
		var raw, state, nextAt, deadline, code, discovered, job string
		var attempts int
		var matched bool
		var wait, started sql.NullString
		if err = rows.Scan(&id, &raw, &state, &attempts, &nextAt, &deadline, &code, &discovered, &matched, &wait, &waitMS, &started, &job); err != nil {
			break
		}
		var source rsssource.Source
		if err = json.Unmarshal([]byte(raw), &source); err != nil {
			break
		}
		due, _ := time.Parse(time.RFC3339Nano, nextAt)
		expiry, _ := time.Parse(time.RFC3339Nano, deadline)
		if wait.Valid {
			since, parseErr := time.Parse(time.RFC3339Nano, wait.String)
			if parseErr == nil {
				end := now
				if due.Before(end) {
					end = due
				}
				waitMS += max(0, end.Sub(since).Milliseconds())
			}
		}
		switch state {
		case "DONE":
			if matched {
				state = "READY"
			} else {
				state = "FILTERED"
			}
		case "SKIPPED":
			state = "BASELINE"
		case "PENDING":
			switch {
			case !now.Before(expiry):
				state = "FAILED"
				code = "PROCESSING_DEADLINE"
			case !now.Before(due) && attempts >= rsssource.MaxConsumerAttempts:
				state = "FAILED"
				code = "PROCESSING_ATTEMPTS_EXHAUSTED"
			case started.Valid && now.Before(due):
				if wait.Valid {
					state = "RATE_WAIT"
				} else {
					state = "PREPARING"
				}
			case attempts > 0 && now.Before(due):
				state = "RETRY_WAIT"
			default:
				state = "QUEUED"
			}
		}
		if !preparationCode.MatchString(code) {
			code = ""
		}
		items = append(items, map[string]any{"id": strconv.FormatInt(id, 10), "title": observability.RedactString(source.Title), "size_bytes": source.SizeBytes, "state": state, "attempts": attempts, "max_attempts": rsssource.MaxConsumerAttempts, "rate_wait_ms": waitMS, "discovered_at": discovered, "next_attempt_at": nextAt, "deadline_at": deadline, "last_error_code": code, "job_id": job})
		lastID = id
	}
	if err != nil || rows.Err() != nil {
		httpapi.WriteError(w, r, "INTERNAL_ERROR", "preparation state unavailable")
		return
	}
	d.writeData(w, r, map[string]any{"items": items, "next_cursor": next})
}
