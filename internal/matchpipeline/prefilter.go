package matchpipeline

import (
	"context"
	"fmt"
	"github.com/vertex-pro/vertex-pro/internal/rules"
	"github.com/vertex-pro/vertex-pro/internal/ruleservice"
)

// Prefilter rejects only when every enabled rule is definitely false using
// stable RSS fields. RSS enclosure size is not authoritative content size.
// True/unknown permits acquisition only, never Add.
func Prefilter(ctx context.Context, deps Deps, item Item) (Outcome, bool, error) {
	if deps.Rules == nil {
		return Outcome{}, false, fmt.Errorf("rules service is required")
	}
	list, err := deps.Rules.List(ctx, item.Source.SiteID)
	if err != nil {
		return Outcome{}, false, err
	}
	facts := buildFacts(item)
	for field := range facts {
		if field != "site" && field != "rss" && field != "title" && field != "published_at" {
			delete(facts, field)
		}
	}
	outcome := Outcome{SkippedReason: "RSS_PREFILTER_NO_POSSIBLE_MATCH"}
	possible := false
	for _, rule := range list {
		state := RuleState{RuleID: rule.ID, RuleName: rule.Name, Revision: rule.Revision, State: rules.FalseState, ReasonCode: "RULE_DISABLED"}
		if rule.Enabled {
			tree, err := ruleservice.ConditionTree(rule.Definition)
			if err != nil {
				return Outcome{}, false, err
			}
			state.State, err = rules.EvaluatePartial(tree, facts)
			if err != nil {
				return Outcome{}, false, err
			}
			state.WasConsidered = true
			state.ReasonCode = "RSS_PREFILTER_" + string(state.State)
			possible = possible || state.State != rules.FalseState
		}
		outcome.RuleStates = append(outcome.RuleStates, state)
	}
	return outcome, !possible, nil
}
