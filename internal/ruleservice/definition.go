package ruleservice

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/ruleaggregate"
	"github.com/vertex-pro/vertex-pro/internal/rules"
)

// ConditionTree accepts the persisted definition envelope and legacy bare ASTs.
// Both production matching and dry-run use this function so their semantics agree.
func ConditionTree(definition json.RawMessage) (rules.ConditionTree, error) {
	var envelope struct {
		Tree json.RawMessage `json:"condition_tree"`
	}
	if err := json.Unmarshal(definition, &envelope); err != nil {
		return rules.ConditionTree{}, fmt.Errorf("invalid rule definition: %w", err)
	}
	raw := definition
	if len(envelope.Tree) != 0 {
		raw = envelope.Tree
	}
	tree, err := rules.Parse(raw)
	if err != nil {
		return rules.ConditionTree{}, fmt.Errorf("invalid rule condition: %w", err)
	}
	if err := rules.ValidateCatalog(tree); err != nil {
		return rules.ConditionTree{}, fmt.Errorf("invalid rule condition: %w", err)
	}
	return tree, nil
}

// MatchFor maps the persisted action snapshot into the shared aggregation model.
func MatchFor(rule Rule, defaultInstanceID string) (ruleaggregate.Match, error) {
	var envelope struct {
		Actions json.RawMessage `json:"action_set_json"`
	}
	if err := json.Unmarshal(rule.Definition, &envelope); err != nil {
		return ruleaggregate.Match{}, fmt.Errorf("invalid rule definition: %w", err)
	}
	match := ruleaggregate.Match{RuleID: rule.ID, RuleRevision: rule.Revision, Priority: rule.Priority, TargetMode: "FIXED_INSTANCE", TargetInstanceID: defaultInstanceID, ParticipationWindow: time.Hour}
	if len(envelope.Actions) == 0 || string(envelope.Actions) == "null" {
		return match, nil
	}
	raw := envelope.Actions
	if len(raw) > 0 && raw[0] == '"' {
		var encoded string
		if err := json.Unmarshal(raw, &encoded); err != nil {
			return ruleaggregate.Match{}, fmt.Errorf("invalid rule actions: %w", err)
		}
		raw = json.RawMessage(encoded)
	}
	var actions struct {
		TargetMode         string            `json:"target_mode"`
		TargetInstanceID   string            `json:"target_instance_id"`
		TargetGroupID      string            `json:"target_group_id"`
		Category           string            `json:"category"`
		SavePath           string            `json:"save_path"`
		SavePathPolicy     string            `json:"save_path_policy"`
		AddParams          map[string]string `json:"add_params"`
		Tags               []string          `json:"tags"`
		AllowedProfiles    []string          `json:"allowed_profiles"`
		StorageLabels      []string          `json:"storage_labels"`
		Window             int               `json:"participation_window_minutes"`
		SequentialDownload *bool             `json:"sequential_download"`
	}
	if err := json.Unmarshal(raw, &actions); err != nil {
		return ruleaggregate.Match{}, fmt.Errorf("invalid rule actions: %w", err)
	}
	if actions.TargetMode != "" {
		match.TargetMode = actions.TargetMode
	}
	if actions.TargetInstanceID != "" {
		match.TargetInstanceID = actions.TargetInstanceID
	}
	match.TargetGroupID = actions.TargetGroupID
	if match.TargetMode == "INSTANCE_GROUP" {
		match.TargetInstanceID = ""
	}
	match.Category, match.SavePath = actions.Category, actions.SavePath
	match.Tags, match.AllowedProfiles, match.StorageLabels = actions.Tags, actions.AllowedProfiles, actions.StorageLabels
	match.AddParams = actions.AddParams
	if match.AddParams == nil {
		match.AddParams = map[string]string{}
	}
	if actions.SavePathPolicy != "" {
		match.AddParams["save_path_policy"] = actions.SavePathPolicy
	}
	if actions.SequentialDownload != nil {
		match.AddParams["sequential_download"] = fmt.Sprint(*actions.SequentialDownload)
	}
	if actions.Window < 0 {
		return ruleaggregate.Match{}, fmt.Errorf("invalid rule participation window")
	}
	if actions.Window > 0 {
		match.ParticipationWindow = time.Duration(actions.Window) * time.Minute
	}
	if _, err := ruleaggregate.Aggregate([]ruleaggregate.Match{match}, nil); err != nil {
		return ruleaggregate.Match{}, fmt.Errorf("invalid rule actions: %w", err)
	}
	return match, nil
}
