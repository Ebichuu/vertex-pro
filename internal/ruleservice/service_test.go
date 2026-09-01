package ruleservice

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
)

type fixedClock struct{ value time.Time }

func (clock fixedClock) Now() time.Time { return clock.value }

func TestServiceCreatesListsAndFiltersRules(t *testing.T) {
	database := openDatabase(t)
	defer database.Close()
	if _, err := database.Exec(`INSERT INTO sites(site_id,name,base_url,created_at,updated_at) VALUES(?,?,?,?,?)`, "site-a", "Site A", "https://a.example", nowText(), nowText()); err != nil {
		t.Fatal(err)
	}
	when := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	service, err := New(database, fixedClock{value: when})
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.Create(context.Background(), CreateInput{SiteID: "site-a", Name: "Low", Enabled: true, Priority: 10, Definition: json.RawMessage(`{"condition_tree":{}}`)})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Create(context.Background(), CreateInput{SiteID: "site-a", Name: "High", Enabled: false, Priority: 100})
	if err != nil {
		t.Fatal(err)
	}
	if first.Revision != 1 || first.State != "PENDING" || string(first.Definition) != `{"condition_tree":{}}` {
		t.Fatalf("first=%+v", first)
	}
	if second.ID == first.ID || string(second.Definition) != "{}" {
		t.Fatalf("second=%+v", second)
	}
	all, err := service.List(context.Background(), "site-a")
	if err != nil || len(all) != 2 {
		t.Fatalf("all=%+v err=%v", all, err)
	}
	if all[0].Name != "High" || all[1].Name != "Low" {
		t.Fatalf("ordering=%+v", all)
	}
	if !all[0].UpdatedAt.Equal(when) {
		t.Fatalf("updated_at=%v want %v", all[0].UpdatedAt, when)
	}
	if filtered, err := service.List(context.Background(), "missing"); err != nil || len(filtered) != 0 {
		t.Fatalf("missing site rules=%+v err=%v", filtered, err)
	}
}

func TestServiceRejectsInvalidDefinitionsAndInputs(t *testing.T) {
	database := openDatabase(t)
	defer database.Close()
	service, _ := New(database, nil)
	cases := []CreateInput{
		{ID: "bad id", Name: "Rule"},
		{ID: "rule-a", Name: ""},
		{ID: "rule-b", Name: "Rule", Priority: -1},
		{ID: "rule-c", Name: "Rule", Definition: json.RawMessage(`[]`)},
		{ID: "rule-d", Name: "Rule", Definition: json.RawMessage(`{} {}`)},
		{ID: "rule-e", SiteID: "missing", Name: "Rule"},
	}
	for _, input := range cases {
		if _, err := service.Create(context.Background(), input); err == nil {
			t.Fatalf("accepted input=%+v", input)
		}
	}
}

func TestServiceUpdateUsesRevisionGuard(t *testing.T) {
	database := openDatabase(t)
	defer database.Close()
	service, _ := New(database, fixedClock{value: time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)})
	rule, err := service.Create(context.Background(), CreateInput{ID: "rule-a", Name: "Original", Enabled: true, Priority: 10})
	if err != nil {
		t.Fatal(err)
	}
	name := "Updated"
	priority := 20
	updated, err := service.Update(context.Background(), rule.ID, 1, UpdateInput{Name: &name, Priority: &priority})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Revision != 2 || updated.Name != "Updated" || updated.Priority != 20 || !updated.Enabled {
		t.Fatalf("updated=%+v", updated)
	}
	enabled := false
	if _, err := service.Update(context.Background(), rule.ID, 1, UpdateInput{Enabled: &enabled}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale update error=%v", err)
	}
	if _, err := service.Get(context.Background(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing get error=%v", err)
	}
}

func openDatabase(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.Open(context.Background(), db.Config{Path: filepath.Join(t.TempDir(), "ruleservice.db"), MaxOpenConns: 1, MaxIdleConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := db.DefaultRunner()
	if err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err := runner.Apply(context.Background(), database); err != nil {
		database.Close()
		t.Fatal(err)
	}
	return database
}

func nowText() string { return time.Date(2026, 8, 31, 11, 0, 0, 0, time.UTC).Format(time.RFC3339Nano) }
