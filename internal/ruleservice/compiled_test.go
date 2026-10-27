package ruleservice

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vertex-pro/vertex-pro/internal/db"
)

func compiledServiceDatabase(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.Open(context.Background(), db.Config{Path: filepath.Join(t.TempDir(), "rules.db"), MaxOpenConns: 1, MaxIdleConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := db.DefaultRunner()
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Apply(context.Background(), database); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func leafDefinition(field, operator string, value map[string]any) json.RawMessage {
	leaf := map[string]any{"kind": "leaf", "field": field, "operator": operator}
	if value != nil {
		leaf["value"] = value
	}
	payload, _ := json.Marshal(map[string]any{"condition_tree": map[string]any{"version": 1, "root": leaf}})
	return payload
}

func TestSaveRejectsFieldsWithoutLiveSource(t *testing.T) {
	service, err := New(compiledServiceDatabase(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	_, err = service.Create(ctx, CreateInput{Name: "free only", Enabled: true, Definition: leafDefinition("promotion", "equals", map[string]any{"type": "boolean", "boolean": true})})
	if err == nil || !strings.HasPrefix(err.Error(), "invalid ") || !strings.Contains(err.Error(), "promotion") {
		t.Fatalf("promotion rule error = %v", err)
	}
	rule, err := service.Create(ctx, CreateInput{Name: "movies", Enabled: true, Definition: leafDefinition("category", "equals", map[string]any{"type": "string", "string": "Movies"})})
	if err != nil {
		t.Fatalf("category rule error = %v", err)
	}
	if _, err := service.Update(ctx, rule.ID, rule.Revision, UpdateInput{Definition: leafDefinition("storage_labels", "exists", nil)}); err == nil || !strings.Contains(err.Error(), "storage_labels") {
		t.Fatalf("update to placement field error = %v", err)
	}
}

func TestListCompiledReusesAndInvalidatesByRevision(t *testing.T) {
	service, err := New(compiledServiceDatabase(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	rule, err := service.Create(ctx, CreateInput{Name: "linux", Enabled: true, Definition: leafDefinition("title", "contains", map[string]any{"type": "string", "string": "Linux"})})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Create(ctx, CreateInput{Name: "off", Enabled: false, Definition: leafDefinition("title", "contains", map[string]any{"type": "string", "string": "x"})}); err != nil {
		t.Fatal(err)
	}
	first, err := service.ListCompiled(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.ListCompiled(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	byName := func(list []CompiledRule, name string) CompiledRule {
		for _, item := range list {
			if item.Name == name {
				return item
			}
		}
		t.Fatalf("rule %s not listed", name)
		return CompiledRule{}
	}
	if byName(first, "linux").Condition == nil || byName(first, "linux").Condition != byName(second, "linux").Condition {
		t.Fatal("unchanged rule was recompiled")
	}
	if byName(first, "off").Condition != nil {
		t.Fatal("disabled rule was compiled")
	}
	if _, err := service.Update(ctx, rule.ID, rule.Revision, UpdateInput{Definition: leafDefinition("title", "contains", map[string]any{"type": "string", "string": "BSD"})}); err != nil {
		t.Fatal(err)
	}
	third, err := service.ListCompiled(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if byName(third, "linux").Condition == byName(first, "linux").Condition || byName(third, "linux").Revision != 2 {
		t.Fatal("edited rule served a stale compiled condition")
	}
}
