package quipolicy

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
	"github.com/vertex-pro/vertex-pro/internal/qui/control"
	"github.com/vertex-pro/vertex-pro/internal/ruleaggregate"
)

type fakeClient struct {
	rules       []control.Automation
	fail        bool
	ignoreWrite bool
}

func (f *fakeClient) Automations(context.Context, string) ([]control.Automation, error) {
	if f.fail {
		return nil, fmt.Errorf("offline")
	}
	return f.rules, nil
}
func (f *fakeClient) SaveAutomation(_ context.Context, _ string, a control.Automation) (control.Automation, error) {
	if f.fail {
		return a, fmt.Errorf("offline")
	}
	a.ID = 1
	if a.SortOrder == 0 && len(f.rules) == 0 {
		a.SortOrder = 7
	}
	a.TrackerDomains = []string{"*"}
	if !f.ignoreWrite {
		f.rules = []control.Automation{a}
	}
	return a, nil
}
func TestPolicyReadbackIdempotencyAndExternalEdit(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, db.Config{Path: filepath.Join(t.TempDir(), "policy.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	runner, _ := db.DefaultRunner()
	if err := runner.Apply(ctx, database); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO rules(rule_id,name,enabled,definition_json,created_at,updated_at) VALUES('r','Rule',1,'{}','now','now')`); err != nil {
		t.Fatal(err)
	}
	fake := &fakeClient{}
	service := Service{DB: database, Client: fake, Instance: "2", Now: time.Now}
	match := ruleaggregate.Match{RuleID: "r", RuleRevision: 1, SeedTimeSeconds: 3600, DeleteMode: "TORRENT_ONLY"}
	fake.ignoreWrite = true
	if _, err := service.Ensure(ctx, match); err == nil {
		t.Fatal("unpersisted policy was accepted")
	}
	var count int
	database.QueryRow(`SELECT COUNT(*) FROM qui_policy_bindings`).Scan(&count)
	if count != 0 {
		t.Fatal("unverified binding saved")
	}
	fake.ignoreWrite = false
	tag, err := service.Ensure(ctx, match)
	if err != nil {
		t.Fatal(err)
	}
	again, err := service.Ensure(ctx, match)
	if err != nil || again != tag || len(fake.rules) != 1 {
		t.Fatalf("not idempotent %s %v", again, err)
	}
	if fake.rules[0].SortOrder != 7 || len(fake.rules[0].TrackerDomains) != 1 {
		t.Fatalf("qui readback defaults were lost: %+v", fake.rules[0])
	}
	fake.rules[0].SortingConfig = map[string]any{"schemaVersion": "1", "type": "simple", "direction": "ASC", "field": "ADDED_ON"}
	if _, err := service.Ensure(ctx, match); err == nil || fake.rules[0].SortingConfig == nil {
		t.Fatal("external sorting configuration was overwritten")
	}
	fake.rules[0].SortingConfig = nil
	fake.rules[0].FreeSpaceSource = map[string]any{"type": "path", "path": "/other"}
	if _, err := service.Ensure(ctx, match); err == nil || fake.rules[0].FreeSpaceSource == nil {
		t.Fatal("external free-space source was overwritten")
	}
	fake.rules[0].FreeSpaceSource = nil
	fake.rules[0].Name = "renamed"
	if _, err := service.Ensure(ctx, match); err == nil || len(fake.rules) != 1 || fake.rules[0].Name != "renamed" {
		t.Fatal("renamed managed policy was replaced with a duplicate")
	}
	fake.rules[0].Name = tag
	fake.rules[0].Enabled = false
	if _, err := service.Ensure(ctx, match); err == nil || fake.rules[0].Enabled {
		t.Fatal("externally disabled policy was re-enabled")
	}
	fake.rules[0].Enabled = true
	changed := fake.rules[0]
	changed.TrackerPattern = "other.example"
	fake.rules[0] = changed
	if _, err := service.Ensure(ctx, match); err == nil {
		t.Fatal("external policy edit overwritten")
	}
	fake.rules[0].TrackerPattern = "*"
	database.Exec(`UPDATE rules SET enabled=0 WHERE rule_id='r'`)
	fake.rules[0].IntervalSeconds = 1
	if err := service.SyncDisabled(ctx); err == nil || !fake.rules[0].Enabled {
		t.Fatal("external policy edit was overwritten during disable")
	}
	var code string
	database.QueryRow(`SELECT last_error_code FROM qui_policy_bindings`).Scan(&code)
	if code != "QUI_POLICY_CONFLICT" {
		t.Fatal(code)
	}
	fake.rules[0].IntervalSeconds = 60
	fake.fail = true
	if err := service.SyncDisabled(ctx); err == nil {
		t.Fatal("offline disable accepted")
	}
	database.QueryRow(`SELECT last_error_code FROM qui_policy_bindings`).Scan(&code)
	if code != "QUI_POLICY_DISABLE_FAILED" {
		t.Fatal(code)
	}
	fake.fail = false
	if err := service.SyncDisabled(ctx); err != nil {
		t.Fatal(err)
	}
	if fake.rules[0].Enabled {
		t.Fatal("policy not disabled")
	}
	if fake.rules[0].SortOrder != 7 {
		t.Fatalf("qui rule order changed during disable: %d", fake.rules[0].SortOrder)
	}
	database.QueryRow(`SELECT COUNT(*) FROM qui_policy_bindings WHERE enabled=0 AND last_error_code=''`).Scan(&count)
	if count != 1 {
		t.Fatal("disable not persisted")
	}
	if _, err := database.Exec(`UPDATE rules SET enabled=1 WHERE rule_id='r'`); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Ensure(ctx, match); err != nil || !fake.rules[0].Enabled {
		t.Fatalf("Vertex-managed policy was not restored after rule re-enable: %v", err)
	}
}
