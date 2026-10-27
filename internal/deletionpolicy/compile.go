package deletionpolicy

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/qui/control"
)

// Condition mirrors qui's RuleCondition (models.RuleCondition at bfe94b5e).
// Value is always a string in qui; numeric values must never be empty
// because qui compares an empty numeric value as 0.
type Condition struct {
	Field      string       `json:"field,omitempty"`
	Operator   string       `json:"operator"`
	Value      string       `json:"value,omitempty"`
	Negate     bool         `json:"negate,omitempty"`
	Conditions []*Condition `json:"conditions,omitempty"`
}

func leaf(field, operator, value string) *Condition {
	return &Condition{Field: field, Operator: operator, Value: value}
}

func group(operator string, children ...*Condition) *Condition {
	return &Condition{Operator: operator, Conditions: children}
}

// Role names one compiled automation of a policy.
type Role string

const (
	RoleTriggers  Role = "triggers"
	RoleWatermark Role = "watermark"
	RoleStalled   Role = "stalled"
)

// DeleteAction mirrors qui's models.DeleteAction for the fields Vertex uses.
type DeleteAction struct {
	Enabled                       bool       `json:"enabled"`
	Mode                          string     `json:"mode"`
	ConditionMatchDurationSeconds int        `json:"conditionMatchDurationSeconds,omitempty"`
	Condition                     *Condition `json:"condition"`
}

// ScoreRule mirrors qui's models.ScoreRule.
type ScoreRule struct {
	Type            string           `json:"type"`
	FieldMultiplier *FieldMultiplier `json:"fieldMultiplier,omitempty"`
	Conditional     *Conditional     `json:"conditional,omitempty"`
}

type FieldMultiplier struct {
	Field      string  `json:"field"`
	Multiplier float64 `json:"multiplier"`
}

type Conditional struct {
	Condition *Condition `json:"condition"`
	Score     float64    `json:"score"`
}

// Sorting mirrors qui's models.SortingConfig for score sorting.
type Sorting struct {
	SchemaVersion string      `json:"schemaVersion"`
	Type          string      `json:"type"`
	Direction     string      `json:"direction"`
	ScoreRules    []ScoreRule `json:"scoreRules"`
}

// Compiled is one qui automation produced from a policy. Delete and Sorting
// are the typed forms of what Automation carries as JSON.
type Compiled struct {
	Role       Role
	Delete     DeleteAction
	Sorting    *Sorting
	Automation control.Automation
}

// AutomationName is the stable qui automation name of a policy role; later
// revisions update the same automation.
func AutomationName(policyID string, role Role) string {
	return "vertex-del-" + policyID + "-" + string(role)
}

// Compile validates policy and safety and returns one automation per
// configured role (triggers, watermark, stalled), each wrapped in the safety
// envelope. Non-live policies compile disabled and in dry-run mode.
func Compile(policy Policy, safety Safety) ([]Compiled, error) {
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	if err := safety.validate(); err != nil {
		return nil, err
	}
	var result []Compiled
	if len(policy.Triggers) > 0 {
		alternatives := make([]*Condition, 0, len(policy.Triggers))
		for _, trigger := range policy.Triggers {
			alternatives = append(alternatives, group("AND", thresholds(trigger.All)...))
		}
		conditions := append(safety.envelope(true), group("OR", alternatives...))
		result = append(result, policy.compiled(RoleTriggers, group("AND", conditions...), nil, nil))
	}
	if watermark := policy.Watermark; watermark != nil {
		conditions := append(safety.envelope(true), leaf("FREE_SPACE", "LESS_THAN", strconv.FormatInt(watermark.MinFreeBytes, 10)))
		conditions = append(conditions, thresholds(watermark.Eligible)...)
		freeSpace := map[string]any{"type": "qbittorrent"}
		if watermark.FreeSpacePath != "" {
			freeSpace = map[string]any{"type": "path", "path": watermark.FreeSpacePath}
		}
		result = append(result, policy.compiled(RoleWatermark, group("AND", conditions...), watermark.sorting(), freeSpace))
	}
	if stalled := policy.Stalled; stalled != nil {
		conditions := append(safety.envelope(false),
			leaf("ADDED_ON_AGE", "GREATER_THAN_OR_EQUAL", seconds(stalled.MinAddedAge)),
			leaf("LAST_ACTIVITY_AGE", "GREATER_THAN_OR_EQUAL", seconds(stalled.MinLastActivityAge)),
		)
		result = append(result, policy.compiled(RoleStalled, group("AND", conditions...), nil, nil))
	}
	return result, nil
}

func (policy Policy) compiled(role Role, condition *Condition, sorting *Sorting, freeSpace map[string]any) Compiled {
	action := DeleteAction{Enabled: true, Mode: deleteModes[policy.DeleteMode], ConditionMatchDurationSeconds: int(policy.ConfirmFor / time.Second), Condition: condition}
	interval := policy.Interval
	if interval == 0 {
		interval = DefaultInterval
	}
	automation := control.Automation{
		Name:            AutomationName(policy.ID, role),
		TrackerPattern:  "*",
		Enabled:         policy.Live,
		DryRun:          !policy.Live,
		Notify:          false,
		IntervalSeconds: int(interval / time.Second),
		Conditions:      asMap(map[string]any{"schemaVersion": "1", "delete": action}),
		FreeSpaceSource: freeSpace,
	}
	if sorting != nil {
		automation.SortingConfig = asMap(sorting)
	}
	return Compiled{Role: role, Delete: action, Sorting: sorting, Automation: automation}
}

func thresholds(list []Threshold) []*Condition {
	conditions := make([]*Condition, 0, len(list))
	for _, threshold := range list {
		conditions = append(conditions, threshold.condition())
	}
	return conditions
}

func (threshold Threshold) condition() *Condition {
	spec := metrics[threshold.Metric]
	if threshold.Metric == Unregistered {
		return leaf(spec.field, "EQUAL", "true")
	}
	operator := comparisonOperators[threshold.Op]
	switch {
	case spec.duration:
		return leaf(spec.field, operator, seconds(threshold.Duration))
	case spec.float:
		return leaf(spec.field, operator, decimal(threshold.Value))
	default:
		return leaf(spec.field, operator, strconv.FormatInt(int64(threshold.Value), 10))
	}
}

func (watermark Watermark) sorting() *Sorting {
	rules := make([]ScoreRule, 0, len(watermark.Order)+len(watermark.SiteScores))
	for _, weight := range watermark.Order {
		rules = append(rules, ScoreRule{Type: "field_multiplier", FieldMultiplier: &FieldMultiplier{Field: metrics[weight.Metric].field, Multiplier: weight.Multiplier}})
	}
	sites := make([]string, 0, len(watermark.SiteScores))
	for site := range watermark.SiteScores {
		sites = append(sites, site)
	}
	slices.Sort(sites)
	for _, site := range sites {
		rules = append(rules, ScoreRule{Type: "conditional", Conditional: &Conditional{Condition: leaf("TAGS", "EQUAL", SiteTag(site)), Score: watermark.SiteScores[site]}})
	}
	return &Sorting{SchemaVersion: "1", Type: "score", Direction: "DESC", ScoreRules: rules}
}

func decimal(value float64) string { return strconv.FormatFloat(value, 'f', -1, 64) }

// asMap converts typed qui JSON into the map form control.Automation holds.
func asMap(value any) map[string]any {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Sprintf("deletionpolicy: encode qui JSON: %v", err))
	}
	var result map[string]any
	if err := json.Unmarshal(encoded, &result); err != nil {
		panic(fmt.Sprintf("deletionpolicy: decode qui JSON: %v", err))
	}
	return result
}
