package resourcesapi

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"

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

	var liveUploaded, liveDownloaded sql.NullInt64
	if err := d.Database.QueryRowContext(request.Context(), `SELECT state,uploaded,downloaded,ratio,observed_at FROM job_observations WHERE job_id=? AND state!=''`, id).Scan(&state, &liveUploaded, &liveDownloaded, &ratio, &observed); err == nil {
		labels := map[string]string{"DOWNLOADING": "下载中", "SEEDING": "做种中", "COMPLETED": "已完成"}
		transfer := map[string]any{"state_label": labels[state], "observed_at": observed}
		if liveUploaded.Valid {
			transfer["uploaded_bytes"] = liveUploaded.Int64
		}
		if liveDownloaded.Valid {
			transfer["downloaded_bytes"] = liveDownloaded.Int64
		}
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
				items = append(items, map[string]any{"id": match.RuleID, "name": match.RuleName, "revision": match.RuleRevision, "priority": match.Priority, "winner": match.IsWinner})
			}
			payload["rules"] = items
			var instance, savePath string
			_ = d.Database.QueryRowContext(request.Context(), `SELECT a.instance_id,t.path FROM race_jobs j JOIN placement_attempts a ON a.attempt_id=j.current_attempt_id JOIN storage_targets t ON t.storage_target_id=a.storage_target_id WHERE j.job_id=?`, id).Scan(&instance, &savePath)
			if instance == "" {
				instance = result.Winner.TargetInstanceID
			}
			if result.Winner.SavePath != "" {
				savePath = result.Winner.SavePath
			}
			if result.Winner.AddParams["save_path_policy"] == "CATEGORY_SUBDIRECTORY" {
				savePath = strings.TrimRight(savePath, "/\\") + "/" + result.Winner.Category
			}
			payload["final_decision"] = map[string]any{"downloader": instance, "category": result.Winner.Category, "tags": result.Tags, "save_path": savePath}
			payload["winner_reason"] = result.Reason
		}
	}
}
