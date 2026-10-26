package resourcesapi

import (
	"net/http"
	"strings"
	"time"
)

// Filtering happens before cursor pagination; groups never filter just the loaded page.
func statusMatches(filter, status string) bool {
	for _, value := range strings.Split(filter, ",") {
		if strings.TrimSpace(value) == status {
			return true
		}
	}
	return false
}

// Daily source counts are deduplicated RSS discoveries. Job confirmation is a
// historical fact (any attempt), so deleting a torrent does not undo its count.
func (d Deps) todaySites(request *http.Request, now time.Time) ([]map[string]any, error) {
	now = now.UTC()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).Format(time.RFC3339)
	end := now.Format(time.RFC3339Nano)
	rows, err := d.Database.QueryContext(request.Context(), `SELECT s.site_id,s.name,s.enabled,
 (SELECT COUNT(*) FROM feeds f WHERE f.site_id=s.site_id),
 (SELECT COUNT(*) FROM rss_sources r WHERE r.site_id=s.site_id AND julianday(r.first_seen_at)>=julianday(?) AND julianday(r.first_seen_at)<=julianday(?)),
 (SELECT COUNT(*) FROM race_jobs j JOIN rss_sources r ON j.source_id=r.site_id||'/'||r.source_key WHERE r.site_id=s.site_id AND julianday(j.created_at)>=julianday(?) AND julianday(j.created_at)<=julianday(?)),
 (SELECT COUNT(*) FROM race_jobs j JOIN rss_sources r ON j.source_id=r.site_id||'/'||r.source_key WHERE r.site_id=s.site_id AND EXISTS(SELECT 1 FROM placement_attempts a WHERE a.job_id=j.job_id AND a.attempt_state IN ('HASH_SEEN','RECONCILED_CONFIRMED') AND julianday(a.confirmed_at)>=julianday(?) AND julianday(a.confirmed_at)<=julianday(?))),
 (SELECT COUNT(*) FROM race_jobs j JOIN rss_sources r ON j.source_id=r.site_id||'/'||r.source_key WHERE r.site_id=s.site_id AND julianday(j.created_at)>=julianday(?) AND julianday(j.created_at)<=julianday(?) AND j.job_state IN ('UNKNOWN','EXPIRED_UNKNOWN','PREPARE_FAILED','ADD_FAILED'))
 FROM sites s ORDER BY s.name,s.site_id`, start, end, start, end, start, end, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []map[string]any{}
	for rows.Next() {
		var id, name string
		var enabled bool
		var feeds, discovered, jobs, confirmed, attention int
		if err := rows.Scan(&id, &name, &enabled, &feeds, &discovered, &jobs, &confirmed, &attention); err != nil {
			return nil, err
		}
		result = append(result, map[string]any{"id": id, "name": name, "enabled": enabled, "feeds": feeds, "discovered": discovered, "jobs": jobs, "confirmed": confirmed, "attention": attention})
	}
	return result, rows.Err()
}
