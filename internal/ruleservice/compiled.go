package ruleservice

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/vertex-pro/vertex-pro/internal/rules"
)

// CompiledRule is a listed rule with its condition parsed, validated and
// compiled. Condition is nil when the rule definition is invalid (Err set).
type CompiledRule struct {
	Rule
	Condition *rules.Compiled
	Err       error
}

// compileCache keeps compiled conditions per rule. An entry is reused only for
// the exact revision and definition bytes the database returned, so edits,
// re-creations and direct writes are never served stale. Listing still reads
// the database, so enable/archive/priority changes apply immediately.
type compileCache struct {
	mu      sync.Mutex
	entries map[string]compiledEntry
}

type compiledEntry struct {
	revision   int
	definition string
	condition  *rules.Compiled
	err        error
}

// Bounds memory if rule IDs churn; a reset only costs recompilation.
const maxCompiledRules = 4096

func newCompileCache() *compileCache {
	return &compileCache{entries: map[string]compiledEntry{}}
}

func (cache *compileCache) get(rule Rule) (*rules.Compiled, error) {
	if cache == nil {
		return compileCondition(rule)
	}
	definition := string(rule.Definition)
	cache.mu.Lock()
	entry, ok := cache.entries[rule.ID]
	cache.mu.Unlock()
	if ok && entry.revision == rule.Revision && entry.definition == definition {
		return entry.condition, entry.err
	}
	condition, err := compileCondition(rule)
	cache.mu.Lock()
	if len(cache.entries) >= maxCompiledRules {
		cache.entries = map[string]compiledEntry{}
	}
	cache.entries[rule.ID] = compiledEntry{revision: rule.Revision, definition: definition, condition: condition, err: err}
	cache.mu.Unlock()
	return condition, err
}

func compileCondition(rule Rule) (*rules.Compiled, error) {
	tree, err := ConditionTree(rule.Definition)
	if err != nil {
		return nil, err
	}
	return rules.Compile(tree)
}

// ListCompiled lists rules like List and attaches each compiled condition.
func (service Service) ListCompiled(ctx context.Context, siteID string) ([]CompiledRule, error) {
	list, err := service.List(ctx, siteID)
	if err != nil {
		return nil, err
	}
	compiled := make([]CompiledRule, len(list))
	for index, rule := range list {
		compiled[index] = CompiledRule{Rule: rule}
		if rule.Enabled {
			compiled[index].Condition, compiled[index].Err = service.compiled.get(rule)
		}
	}
	return compiled, nil
}

// rejectUnavailableFields refuses to save a condition that references fields
// production matching never supplies: such a rule could never match. Saved
// definitions without a parseable condition keep their former acceptance.
func rejectUnavailableFields(definition []byte) error {
	tree, err := ConditionTree(definition)
	if err != nil {
		return nil
	}
	if fields := rules.UnavailableFields(tree); len(fields) > 0 {
		return fmt.Errorf("invalid rule condition: fields have no live data source yet and would never match: %s", strings.Join(fields, ", "))
	}
	return nil
}
