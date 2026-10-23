package resourcesapi

import (
	"database/sql"
	"encoding/json"
	"net/http"

	"github.com/vertex-pro/vertex-pro/internal/ruleaggregate"
)

func (d Deps) addJobFacts(request *http.Request, id string, payload map[string]any) {
	var state, observed string
	var uploaded, downloaded int64
	var ratio sql.NullFloat64
	if err := d.Database.QueryRowContext(request.Context(), `SELECT last_known_observation,last_uploaded_bytes,last_downloaded_bytes,last_ratio,deleted_at FROM history_deletion_facts WHERE job_id=? ORDER BY deleted_at DESC LIMIT 1`, id).Scan(&state, &uploaded, &downloaded, &ratio, &observed); err == nil {
		labels := map[string]string{"DOWNLOADING": "下载中", "SEEDING": "做种中", "COMPLETED": "已完成"}
		transfer := map[string]any{"state_label": labels[state], "uploaded_bytes": uploaded, "downloaded_bytes": downloaded, "observed_at": observed}
		if ratio.Valid {
			transfer["ratio"] = ratio.Float64
		}
		payload["last_transfer"] = transfer
	}
	var raw string
	if err := d.Database.QueryRowContext(request.Context(), `SELECT aggregation_json FROM job_rule_snapshots WHERE job_id=?`, id).Scan(&raw); err == nil {
		var result ruleaggregate.Result
		if json.Unmarshal([]byte(raw), &result) == nil {
			items := make([]map[string]any, 0, len(result.Matches))
			for _, match := range result.Matches {
				items = append(items, map[string]any{"id": match.RuleID, "revision": match.RuleRevision, "priority": match.Priority, "winner": match.IsWinner})
			}
			payload["rules"] = items
			payload["final_decision"] = map[string]any{"downloader": result.Winner.TargetInstanceID, "category": result.Winner.Category, "tags": result.Tags, "save_path": result.Winner.SavePath}
			payload["winner_reason"] = result.Reason
		}
	}
}
