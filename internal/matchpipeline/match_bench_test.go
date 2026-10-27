package matchpipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/vertex-pro/vertex-pro/internal/racejob"
	"github.com/vertex-pro/vertex-pro/internal/racejobstore"
	"github.com/vertex-pro/vertex-pro/internal/ruleaggregate"
	"github.com/vertex-pro/vertex-pro/internal/ruleservice"
)

// A realistic brushing rule: title regex AND a size window AND a site check.
// None match the benchmark item, so every rule is fully evaluated and no race
// job is written; the benchmark measures rule loading and evaluation only.
func benchRuleTree(i int) json.RawMessage {
	tree := map[string]any{"version": 1, "root": map[string]any{"kind": "group", "operator": "AND", "children": []any{
		map[string]any{"kind": "leaf", "field": "site", "operator": "equals", "value": map[string]any{"type": "string", "string": "site-1"}},
		map[string]any{"kind": "leaf", "field": "size", "operator": "between", "value": map[string]any{"type": "number_range", "numbers": []float64{1 << 30, 64 << 30}}},
		map[string]any{"kind": "group", "operator": "OR", "children": []any{
			map[string]any{"kind": "leaf", "field": "title", "operator": "regex", "value": map[string]any{"type": "string", "string": fmt.Sprintf(`(?i)\bShow%02d\b.*(2160p|1080p).*(WEB-?DL|BluRay)`, i)}},
			map[string]any{"kind": "leaf", "field": "title", "operator": "contains", "value": map[string]any{"type": "string", "string": fmt.Sprintf("Series-%02d", i)}},
		}},
	}}}
	payload, _ := json.Marshal(map[string]any{"condition_tree": tree})
	return payload
}

func benchDeps(b *testing.B, rules int) Deps {
	b.Helper()
	database := pipelineDatabase(b)
	seedSite(b, database, "site-1")
	service, err := ruleservice.New(database, nil)
	if err != nil {
		b.Fatal(err)
	}
	for i := 0; i < rules; i++ {
		seedRule(b, service, "site-1", fmt.Sprintf("rule-%02d", i), i, benchRuleTree(i))
	}
	store, err := racejobstore.New(database)
	if err != nil {
		b.Fatal(err)
	}
	creator, err := racejob.New(store, nil)
	if err != nil {
		b.Fatal(err)
	}
	return Deps{Rules: &service, JobCreator: creator, MatchFactory: func(ruleservice.Rule) (ruleaggregate.Match, error) {
		return ruleaggregate.Match{}, nil
	}}
}

func BenchmarkPrefilter(b *testing.B) {
	for _, n := range []int{5, 20, 50} {
		b.Run(fmt.Sprintf("rules=%d", n), func(b *testing.B) {
			deps := benchDeps(b, n)
			candidate := item()
			candidate.Source.Title = "Ubuntu.Linux.24.04.1080p.WEB-DL-GROUP"
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if _, _, err := Prefilter(context.Background(), deps, candidate); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkProcess(b *testing.B) {
	for _, n := range []int{5, 20, 50} {
		b.Run(fmt.Sprintf("rules=%d", n), func(b *testing.B) {
			deps := benchDeps(b, n)
			candidate := item()
			size := int64(4 << 30)
			candidate.Source.SizeBytes = &size
			candidate.Source.Title = "Ubuntu.Linux.24.04.1080p.WEB-DL-GROUP"
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				outcome, err := Process(context.Background(), deps, candidate)
				if err != nil || outcome.Matched {
					b.Fatalf("outcome=%+v err=%v", outcome, err)
				}
			}
		})
	}
}
