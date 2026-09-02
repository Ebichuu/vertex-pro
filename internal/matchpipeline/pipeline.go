// Package matchpipeline composes the production front half of the Add chain:
// enabled rules for a site are evaluated against a deduped feed source, the
// three-valued evaluator result gates matching (UNKNOWN never matches), the
// surviving rule matches are aggregated, and a winner creates or reuses the
// race job through the persisted race job store.
package matchpipeline

import (
	"context"
	"fmt"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/racejob"
	"github.com/vertex-pro/vertex-pro/internal/rsssource"
	"github.com/vertex-pro/vertex-pro/internal/ruleaggregate"
	"github.com/vertex-pro/vertex-pro/internal/rules"
	"github.com/vertex-pro/vertex-pro/internal/ruleservice"
)

// Item is one deduped feed source with the identity computed from its torrent
// artifact by the torrent stage.
type Item struct {
	Source      rsssource.Source
	IdentityKey string
	Category    string
	Publisher   string
	PublishedAt time.Time
}

// MatchFactory converts a matched rule into an aggregate match. Target and
// add-parameter semantics belong to the rule definition schema, so the
// assembly supplies this mapping instead of the pipeline.
type MatchFactory func(rule ruleservice.Rule) (ruleaggregate.Match, error)

// Deps wires the pipeline to its stores.
type Deps struct {
	Rules        *ruleservice.Service
	JobCreator   *racejob.Creator
	MatchFactory MatchFactory
	Now          func() time.Time
}

// Outcome reports what the pipeline decided for one item.
type Outcome struct {
	RuleStates    []RuleState
	Matched       bool
	Rejected      bool
	Rejection     string
	Aggregation   *ruleaggregate.Result
	Job           racejob.Job
	JobCreated    bool
	JobDuplicate  bool
	SkippedReason string
}

// RuleState records the three-valued evaluation of one rule for the item.
type RuleState struct {
	RuleID        string
	RuleName      string
	Revision      int
	State         rules.State
	ReasonCode    string
	WasConsidered bool
}

// Process runs the pipeline for one item. A zero error with Matched=false is a
// normal outcome (no rule matched or the aggregation rejected the item).
func Process(ctx context.Context, deps Deps, item Item) (Outcome, error) {
	if deps.Rules == nil || deps.JobCreator == nil {
		return Outcome{}, fmt.Errorf("rules service and job creator are required")
	}
	if item.IdentityKey == "" {
		return Outcome{}, fmt.Errorf("identity key is required")
	}
	now := deps.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	ruleList, err := deps.Rules.List(ctx, item.Source.SiteID)
	if err != nil {
		return Outcome{}, fmt.Errorf("list rules: %w", err)
	}
	facts := buildFacts(item)
	outcome := Outcome{}
	matches := make([]ruleaggregate.Match, 0, len(ruleList))
	for _, rule := range ruleList {
		state := RuleState{RuleID: rule.ID, RuleName: rule.Name, Revision: rule.Revision}
		if !rule.Enabled {
			state.State = rules.FalseState
			state.ReasonCode = "RULE_DISABLED"
			outcome.RuleStates = append(outcome.RuleStates, state)
			continue
		}
		tree, err := rules.Parse(rule.Definition)
		if err != nil {
			return Outcome{}, fmt.Errorf("parse rule %s: %w", rule.ID, err)
		}
		result, err := rules.Evaluate(tree, facts)
		if err != nil {
			return Outcome{}, fmt.Errorf("evaluate rule %s: %w", rule.ID, err)
		}
		state.State = result.State
		state.ReasonCode = result.Reason.Code
		state.WasConsidered = true
		outcome.RuleStates = append(outcome.RuleStates, state)
		if result.State != rules.TrueState {
			// Fail closed: UNKNOWN and FALSE never match.
			continue
		}
		if deps.MatchFactory == nil {
			return Outcome{}, fmt.Errorf("match factory is required for matching rules")
		}
		match, err := deps.MatchFactory(rule)
		if err != nil {
			return Outcome{}, fmt.Errorf("build match for rule %s: %w", rule.ID, err)
		}
		match.RuleID = rule.ID
		match.RuleRevision = rule.Revision
		match.Priority = rule.Priority
		matches = append(matches, match)
	}
	if len(matches) == 0 {
		outcome.SkippedReason = "NO_RULE_MATCHED"
		return outcome, nil
	}
	aggregated, err := ruleaggregate.Aggregate(matches, nil)
	if err != nil {
		return Outcome{}, fmt.Errorf("aggregate rule matches: %w", err)
	}
	outcome.Aggregation = &aggregated
	if aggregated.Rejected {
		outcome.Rejected = true
		outcome.Rejection = aggregated.Reason
		return outcome, nil
	}
	outcome.Matched = true
	jobResult, err := deps.JobCreator.Create(ctx, racejob.Input{
		SourceID:              item.Source.SiteID + "/" + item.Source.SourceKey,
		IdentityKey:           item.IdentityKey,
		ParticipationDeadline: now().Add(aggregated.ParticipationWindow),
		ObservationDeadline:   now().Add(aggregated.ParticipationWindow),
	})
	if err != nil {
		return Outcome{}, fmt.Errorf("create race job: %w", err)
	}
	outcome.Job = jobResult.Job
	outcome.JobCreated = jobResult.Created
	outcome.JobDuplicate = jobResult.Duplicate
	return outcome, nil
}

// buildFacts maps the deduped source onto the rule field catalog. Fields the
// source does not carry stay absent so the evaluator resolves them as UNKNOWN
// and the rule fails closed.
func buildFacts(item Item) rules.Facts {
	facts := rules.Facts{}
	stringFact := func(field, value string) {
		if value != "" {
			facts[field] = rules.Fact{Present: true, Value: rules.Value{Type: rules.StringValue, String: &value}}
		}
	}
	stringFact("site", item.Source.SiteID)
	stringFact("rss", item.Source.URL)
	stringFact("title", item.Source.Title)
	stringFact("category", item.Category)
	stringFact("publisher", item.Publisher)
	if item.Source.SizeBytes != nil {
		number := float64(*item.Source.SizeBytes)
		facts["size"] = rules.Fact{Present: true, Value: rules.Value{Type: rules.NumberValue, Number: &number}}
	}
	if !item.PublishedAt.IsZero() {
		stamp := item.PublishedAt.UTC().Format(time.RFC3339)
		facts["published_at"] = rules.Fact{Present: true, Value: rules.Value{Type: rules.TimeValue, Time: &stamp}}
	}
	return facts
}
