package db

import (
	"context"
	"database/sql"
	"testing"
)

func TestSiteFeedRuleIdentityConstraints(t *testing.T) {
	database := openTestDatabase(t)
	runner, err := DefaultRunner()
	if err != nil {
		t.Fatalf("DefaultRunner() error = %v", err)
	}
	if err := runner.Apply(context.Background(), database); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}

	insertSite(t, database, "site-a", "Site A", "https://a.example")
	insertSite(t, database, "site-b", "Site B", "https://b.example")
	insertFeed(t, database, "feed-a", "site-a", "Race feed", "https://a.example/rss")
	if _, err := database.Exec("INSERT INTO feeds(feed_id, site_id, name, feed_url, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)", "feed-a-duplicate", "site-a", "Duplicate", "https://a.example/rss", nowText(), nowText()); err == nil {
		t.Fatal("duplicate feed source error = nil, want constraint error")
	}
	insertFeed(t, database, "feed-b", "site-b", "Same URL on another site", "https://a.example/rss")

	insertIdentity(t, database, "identity-1", "canonical-1")
	if _, err := database.Exec("INSERT INTO identities(identity_id, canonical_hash, title, created_at, updated_at) VALUES (?, ?, ?, ?, ?)", "identity-duplicate", "canonical-1", "Duplicate", nowText(), nowText()); err == nil {
		t.Fatal("duplicate canonical identity error = nil, want constraint error")
	}
	insertIdentitySource(t, database, "identity-1", "site-a", "source-1")
	insertIdentitySource(t, database, "identity-1", "site-b", "source-1")
	if _, err := database.Exec("INSERT INTO identity_sources(identity_id, site_id, source_key, created_at) VALUES (?, ?, ?, ?)", "identity-1", "site-a", "source-1", nowText()); err == nil {
		t.Fatal("duplicate source within site error = nil, want constraint error")
	}

	if _, err := database.Exec("INSERT INTO rules(rule_id, site_id, name, priority, revision, state, definition_json, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)", "rule-1", "site-a", "Large", 100, 1, "PENDING", `{"min_size":100}`, nowText(), nowText()); err != nil {
		t.Fatalf("insert rule error = %v", err)
	}
	if _, err := database.Exec("UPDATE rules SET revision = ?, state = ?, updated_at = ? WHERE rule_id = ?", 2, "MATCHED", nowText(), "rule-1"); err != nil {
		t.Fatalf("update rule error = %v", err)
	}
	var revision int
	var state string
	if err := database.QueryRow("SELECT revision, state FROM rules WHERE rule_id = ?", "rule-1").Scan(&revision, &state); err != nil {
		t.Fatalf("read rule error = %v", err)
	}
	if revision != 2 || state != "MATCHED" {
		t.Fatalf("rule = revision %d/state %s, want 2/MATCHED", revision, state)
	}
}

func insertSite(t *testing.T, database *sql.DB, id, name, baseURL string) {
	t.Helper()
	if _, err := database.Exec("INSERT INTO sites(site_id, name, base_url, created_at, updated_at) VALUES (?, ?, ?, ?, ?)", id, name, baseURL, nowText(), nowText()); err != nil {
		t.Fatalf("insert site %s error = %v", id, err)
	}
}

func insertFeed(t *testing.T, database *sql.DB, id, siteID, name, feedURL string) {
	t.Helper()
	if _, err := database.Exec("INSERT INTO feeds(feed_id, site_id, name, feed_url, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)", id, siteID, name, feedURL, nowText(), nowText()); err != nil {
		t.Fatalf("insert feed %s error = %v", id, err)
	}
}

func insertIdentity(t *testing.T, database *sql.DB, id, canonicalHash string) {
	t.Helper()
	if _, err := database.Exec("INSERT INTO identities(identity_id, canonical_hash, title, created_at, updated_at) VALUES (?, ?, ?, ?, ?)", id, canonicalHash, "Title", nowText(), nowText()); err != nil {
		t.Fatalf("insert identity %s error = %v", id, err)
	}
}

func insertIdentitySource(t *testing.T, database *sql.DB, identityID, siteID, sourceKey string) {
	t.Helper()
	if _, err := database.Exec("INSERT INTO identity_sources(identity_id, site_id, source_key, created_at) VALUES (?, ?, ?, ?)", identityID, siteID, sourceKey, nowText()); err != nil {
		t.Fatalf("insert identity source %s/%s error = %v", siteID, sourceKey, err)
	}
}

func nowText() string { return "2026-08-24T00:00:00Z" }
