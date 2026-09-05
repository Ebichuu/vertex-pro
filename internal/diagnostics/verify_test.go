package diagnostics

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/buildidentity"
	"github.com/vertex-pro/vertex-pro/internal/db"
)

func TestVerifyChecksPassOnMigratedDatabaseAndReportReasons(t *testing.T) {
	database, err := db.Open(t.Context(), db.Config{Path: t.TempDir() + "/verify.db"})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	runner, err := db.DefaultRunner()
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Apply(t.Context(), database); err != nil {
		t.Fatal(err)
	}

	checks := VerifyChecks(t.Context(), database, runner, len(runner.Migrations))
	if !AllOK(checks) {
		t.Fatalf("checks on migrated database = %+v", checks)
	}
	names := map[string]bool{}
	for _, check := range checks {
		names[check.Name] = true
	}
	for _, name := range []string{"database_ping", "schema_migrations", "startup_consistency", "foreign_key_check"} {
		if !names[name] {
			t.Fatalf("missing check %q: %+v", name, checks)
		}
	}

	// 未迁移的空库：schema 检查必须给出可读原因而不是伪造通过。
	empty, err := db.Open(t.Context(), db.Config{Path: t.TempDir() + "/empty.db"})
	if err != nil {
		t.Fatal(err)
	}
	defer empty.Close()
	emptyChecks := VerifyChecks(t.Context(), empty, runner, len(runner.Migrations))
	if AllOK(emptyChecks) {
		t.Fatalf("unmigrated database must not pass: %+v", emptyChecks)
	}
}

func TestCountsAndSupportBundle(t *testing.T) {
	database, err := db.Open(t.Context(), db.Config{Path: t.TempDir() + "/bundle.db"})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	runner, err := db.DefaultRunner()
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Apply(t.Context(), database); err != nil {
		t.Fatal(err)
	}
	nowText := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC).Format(time.RFC3339)
	if _, err := database.Exec(`INSERT INTO race_jobs(job_id, source_id, identity_key, job_state, rule_state, torrent_state, placement_state, participation_deadline, observation_deadline, created_at, updated_at) VALUES ('job-1','src-1','id-1','READY','MATCHED','READY','PREPARED','2026-09-07T00:00:00Z','2026-09-07T00:00:00Z',?,?)`, nowText, nowText); err != nil {
		t.Fatal(err)
	}

	counts, err := Counts(t.Context(), database)
	if err != nil {
		t.Fatal(err)
	}
	if counts["race_jobs"] != 1 || counts["event_stream"] != 0 {
		t.Fatalf("counts = %+v", counts)
	}

	report, err := Build("test", buildidentity.Identity{Available: true, Clean: true, Revision: "abc123"}, map[string]any{"database_path": "bundle.db"}, database, time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := BuildSupportBundle(t.Context(), report, database, map[string]any{"events": "7d"})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Digest == "" || !json.Valid([]byte(payload)) {
		t.Fatalf("bundle invalid: %s", payload)
	}
}
