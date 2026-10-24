package app

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
	"github.com/vertex-pro/vertex-pro/internal/qui/control"
	"github.com/vertex-pro/vertex-pro/internal/quipolicy"
	"github.com/vertex-pro/vertex-pro/internal/ruleaggregate"
)

type cachePolicyClient struct {
	list          []control.Automation
	reads, writes int
}

func (c *cachePolicyClient) Automations(context.Context, string) ([]control.Automation, error) {
	c.reads++
	return c.list, nil
}
func (c *cachePolicyClient) SaveAutomation(_ context.Context, _ string, a control.Automation) (control.Automation, error) {
	c.writes++
	a.ID = 1
	c.list = []control.Automation{a}
	return a, nil
}

func TestPolicyCachePrewarmIsLocalAndExternalDriftFailsClosed(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, db.Config{Path: filepath.Join(t.TempDir(), "policy.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	migrations, _ := db.DefaultRunner()
	if err := migrations.Apply(ctx, database); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO rules(rule_id,name,enabled,revision,definition_json,created_at,updated_at) VALUES('r','R',1,1,'{}','now','now')`); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	client := &cachePolicyClient{}
	cache := newPolicyCache(quipolicy.Service{DB: database, Client: client, Instance: "2", Now: func() time.Time { return now }})
	match := ruleaggregate.Match{RuleID: "r", RuleRevision: 1, SeedTimeSeconds: 3600, DeleteMode: "WITH_FILES"}
	if _, err := cache.ready(ctx, match); err == nil {
		t.Fatal("unwarmed policy was ready")
	}
	if _, err := cache.warm(ctx, match); err != nil {
		t.Fatal(err)
	}
	reads, writes := client.reads, client.writes
	for range 30 {
		if _, err := cache.ready(ctx, match); err != nil {
			t.Fatal(err)
		}
	}
	if client.reads != reads || client.writes != writes {
		t.Fatal("ready path performed HTTP")
	}
	changed := match
	changed.RuleRevision++
	if _, err := cache.ready(ctx, changed); err == nil {
		t.Fatal("wrong revision reused")
	}
	now = now.Add(31 * time.Second)
	if _, err := cache.ready(ctx, match); err == nil {
		t.Fatal("expired policy reused")
	}
	if _, err := cache.warm(ctx, match); err != nil {
		t.Fatal(err)
	}
	if client.writes != writes {
		t.Fatal("unchanged remote policy was rewritten")
	}
	client.list[0].Enabled = false
	now = now.Add(31 * time.Second)
	if _, err := cache.warm(ctx, match); err == nil {
		t.Fatal("external disable was accepted")
	}
	if _, err := cache.ready(ctx, match); err == nil {
		t.Fatal("failed refresh retained ready cache")
	}
	if client.list[0].Enabled || client.writes != writes {
		t.Fatal("external disable was overwritten")
	}
}
