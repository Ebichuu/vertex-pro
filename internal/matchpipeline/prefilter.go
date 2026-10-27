package matchpipeline

import (
	"context"
	"fmt"

	"github.com/vertex-pro/vertex-pro/internal/rules"
)

// stableFields are facts known from the RSS entry alone that the torrent
// artifact cannot change. RSS enclosure size is not authoritative content size.
var stableFields = map[string]bool{"site": true, "rss": true, "title": true, "published_at": true, "category": true, "publisher": true, "group": true, "discovery_delay": true}

// Prefilter rejects only when every enabled rule is definitely false using
// stable RSS fields. True/unknown permits acquisition only, never Add.
func Prefilter(ctx context.Context, deps Deps, item Item) (Outcome, bool, error) {
	if deps.Rules == nil {
		return Outcome{}, false, fmt.Errorf("rules service is required")
	}
	list, err := deps.Rules.ListCompiled(ctx, item.Source.SiteID)
	if err != nil {
		return Outcome{}, false, err
	}
	facts := buildFacts(item)
	for field := range facts {
		if !stableFields[field] {
			delete(facts, field)
		}
	}
	outcome := Outcome{SkippedReason: "RSS_PREFILTER_NO_POSSIBLE_MATCH"}
	possible := false
	for _, compiled := range list {
		rule := compiled.Rule
		state := RuleState{RuleID: rule.ID, RuleName: rule.Name, Revision: rule.Revision, State: rules.FalseState, ReasonCode: "RULE_DISABLED"}
		if rule.Enabled {
			if compiled.Err != nil {
				return Outcome{}, false, compiled.Err
			}
			state.State = compiled.Condition.EvaluatePartial(facts)
			state.WasConsidered = true
			state.ReasonCode = "RSS_PREFILTER_" + string(state.State)
			possible = possible || state.State != rules.FalseState
		}
		outcome.RuleStates = append(outcome.RuleStates, state)
	}
	return outcome, !possible, nil
}
