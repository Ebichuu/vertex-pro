package resourcesapi

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/httpapi"
	"github.com/vertex-pro/vertex-pro/internal/ruleaggregate"
	"github.com/vertex-pro/vertex-pro/internal/rules"
	"github.com/vertex-pro/vertex-pro/internal/ruleservice"
)

type dryRunInput struct {
	Tree   json.RawMessage `json:"condition_tree,omitempty"`
	Facts  rules.Facts     `json:"facts"`
	SiteID string          `json:"site_id,omitempty"`
}

func (d Deps) dryRunRule(response http.ResponseWriter, request *http.Request) {
	var input dryRunInput
	if err := httpapi.DecodeJSON(response, request, &input, 128<<10); err != nil {
		writeRuleRequestError(response, request, err)
		return
	}
	service, err := d.ruleService()
	if err != nil {
		httpapi.WriteError(response, request, "INTERNAL_ERROR", "rule service unavailable")
		return
	}
	rule, err := service.Get(request.Context(), request.PathValue("id"))
	if err != nil {
		writeRuleError(response, request, err)
		return
	}
	tree, err := ruleservice.ConditionTree(rule.Definition)
	if err != nil {
		httpapi.WriteError(response, request, "VALIDATION_FAILED", err.Error())
		return
	}
	result, err := rules.Evaluate(tree, input.Facts)
	if err != nil {
		httpapi.WriteError(response, request, "VALIDATION_FAILED", err.Error())
		return
	}
	httpapi.WriteJSON(response, http.StatusOK, map[string]any{"state": result.State, "reason": reasonPayload(result.Reason), "rule_id": rule.ID, "revision": rule.Revision, "evaluated_at": d.currentTime().Format(time.RFC3339Nano)})
}

func (d Deps) dryRunRules(response http.ResponseWriter, request *http.Request) {
	var input dryRunInput
	if err := httpapi.DecodeJSON(response, request, &input, 128<<10); err != nil {
		writeRuleRequestError(response, request, err)
		return
	}
	if len(input.Tree) != 0 {
		tree, err := ruleservice.ConditionTree(input.Tree)
		if err != nil {
			httpapi.WriteError(response, request, "VALIDATION_FAILED", err.Error())
			return
		}
		result, err := rules.Evaluate(tree, input.Facts)
		if err != nil {
			httpapi.WriteError(response, request, "VALIDATION_FAILED", err.Error())
			return
		}
		httpapi.WriteJSON(response, http.StatusOK, map[string]any{"state": result.State, "reason": reasonPayload(result.Reason), "evaluated_at": d.currentTime().Format(time.RFC3339Nano)})
		return
	}
	service, err := d.ruleService()
	if err != nil {
		httpapi.WriteError(response, request, "INTERNAL_ERROR", "rule service unavailable")
		return
	}
	if input.SiteID == "" {
		if fact, ok := input.Facts["site"]; ok && fact.Present && fact.Value.String != nil {
			input.SiteID = *fact.Value.String
		}
	}
	stored, err := service.List(request.Context(), input.SiteID)
	if err != nil {
		writeRuleError(response, request, err)
		return
	}
	var defaultInstance string
	var count int
	if err := d.Database.QueryRowContext(request.Context(), `SELECT COUNT(*),COALESCE(MIN(instance_id),'') FROM sync_instance_state`).Scan(&count, &defaultInstance); err != nil || count != 1 {
		defaultInstance = ""
	}
	items := make([]map[string]any, 0, len(stored))
	matches := make([]ruleaggregate.Match, 0, len(stored))
	blockers := make([]string, 0)
	for _, rule := range stored {
		item := map[string]any{"id": rule.ID, "name": rule.Name, "priority": rule.Priority, "revision": rule.Revision, "winner": false}
		if !rule.Enabled {
			item["state"] = rules.FalseState
			item["reason"] = map[string]any{"state": rules.FalseState, "code": "RULE_DISABLED"}
			items = append(items, item)
			continue
		}
		tree, parseErr := ruleservice.ConditionTree(rule.Definition)
		if parseErr != nil {
			item["state"] = rules.UnknownState
			item["reason"] = map[string]any{"state": rules.UnknownState, "code": "INVALID_RULE_DEFINITION"}
			blockers = append(blockers, rule.ID+":INVALID_RULE_DEFINITION")
			items = append(items, item)
			continue
		}
		result, evalErr := rules.Evaluate(tree, input.Facts)
		if evalErr != nil {
			httpapi.WriteError(response, request, "VALIDATION_FAILED", evalErr.Error())
			return
		}
		item["state"] = result.State
		item["reason"] = reasonPayload(result.Reason)
		if result.State == rules.TrueState {
			match, matchErr := ruleservice.MatchFor(rule, defaultInstance)
			if matchErr != nil || (match.TargetMode == "FIXED_INSTANCE" && match.TargetInstanceID == "") {
				blockers = append(blockers, rule.ID+":TARGET_NOT_CONFIGURED")
			} else {
				matches = append(matches, match)
			}
		}
		items = append(items, item)
	}
	var aggregation any
	if len(matches) > 0 && len(blockers) == 0 {
		result, aggregateErr := ruleaggregate.Aggregate(matches, nil)
		if aggregateErr != nil {
			httpapi.WriteError(response, request, "VALIDATION_FAILED", aggregateErr.Error())
			return
		}
		aggregation = aggregationPayload(result)
		if !result.Rejected {
			for _, item := range items {
				item["winner"] = item["id"] == result.Winner.RuleID
			}
		}
	}
	httpapi.WriteJSON(response, http.StatusOK, map[string]any{"evaluated_at": d.currentTime().Format(time.RFC3339Nano), "rules": items, "aggregation": aggregation, "blockers": blockers})
}

func reasonPayload(reason rules.Reason) map[string]any {
	children := make([]map[string]any, 0, len(reason.Children))
	for _, child := range reason.Children {
		children = append(children, reasonPayload(child))
	}
	return map[string]any{"state": reason.State, "code": reason.Code, "field": reason.Field, "operator": reason.Operator, "children": children}
}

func aggregationPayload(result ruleaggregate.Result) map[string]any {
	matches := make([]map[string]any, 0, len(result.Matches))
	for _, match := range result.Matches {
		matches = append(matches, map[string]any{"rule_id": match.RuleID, "rule_revision": match.RuleRevision, "priority": match.Priority, "is_winner": match.IsWinner})
	}
	winner := result.Winner
	return map[string]any{"algorithm_version": result.AlgorithmVersion, "matches": matches, "winner": map[string]any{"rule_id": winner.RuleID, "target_mode": winner.TargetMode, "target_instance_id": winner.TargetInstanceID, "target_group_id": winner.TargetGroupID, "category": winner.Category, "save_path": winner.SavePath, "add_params": winner.AddParams}, "tags": result.Tags, "allowed_profiles": result.AllowedProfiles, "storage_labels": result.StorageLabels, "participation_window_minutes": int(result.ParticipationWindow / time.Minute), "rejected": result.Rejected, "reason": result.Reason}
}
