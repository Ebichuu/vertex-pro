package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestDefaultRunnerAppliesAndSkipsAppliedMigrations(t *testing.T) {
	database := openTestDatabase(t)
	runner, err := DefaultRunner()
	if err != nil {
		t.Fatalf("DefaultRunner() error = %v", err)
	}
	if err := runner.Apply(context.Background(), database); err != nil {
		t.Fatalf("first Apply() error = %v", err)
	}
	if err := runner.Apply(context.Background(), database); err != nil {
		t.Fatalf("second Apply() error = %v", err)
	}
	var count int
	if err := database.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&count); err != nil {
		t.Fatalf("migration count query error = %v", err)
	}
	if count != len(runner.Migrations) {
		t.Fatalf("migration count = %d, want %d", count, len(runner.Migrations))
	}
	var table string
	if err := database.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='vertex_pro_meta'").Scan(&table); err != nil {
		t.Fatalf("baseline table query error = %v", err)
	}
	if table != "vertex_pro_meta" {
		t.Fatalf("baseline table = %q", table)
	}
}

func TestRunnerRollsBackWhenMigrationFails(t *testing.T) {
	database := openTestDatabase(t)
	runner, err := NewRunner([]Migration{
		{Version: 1, Name: "first", SQL: "CREATE TABLE first_table (id INTEGER PRIMARY KEY);"},
		{Version: 2, Name: "broken", SQL: "THIS IS NOT SQL;"},
	})
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	if err := runner.Apply(context.Background(), database); err == nil {
		t.Fatal("Apply() error = nil, want error")
	}
	if tableExists(t, database, "schema_migrations") || tableExists(t, database, "first_table") {
		t.Fatal("failed migration left transactional tables behind")
	}
}

func TestRunnerRejectsChecksumMismatch(t *testing.T) {
	database := openTestDatabase(t)
	first, err := NewRunner([]Migration{{Version: 1, Name: "first", SQL: "CREATE TABLE first_table (id INTEGER PRIMARY KEY);"}})
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	if err := first.Apply(context.Background(), database); err != nil {
		t.Fatalf("first Apply() error = %v", err)
	}
	second, err := NewRunner([]Migration{{Version: 1, Name: "first", SQL: "CREATE TABLE changed_table (id INTEGER PRIMARY KEY);"}})
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	if err := second.Apply(context.Background(), database); err == nil {
		t.Fatal("checksum mismatch error = nil, want error")
	}
}

func TestNewRunnerRejectsInvalidMigrations(t *testing.T) {
	for name, migrations := range map[string][]Migration{
		"zero version": {{Version: 0, Name: "zero", SQL: "SELECT 1;"}},
		"empty name":   {{Version: 1, SQL: "SELECT 1;"}},
		"empty sql":    {{Version: 1, Name: "empty"}},
		"duplicate":    {{Version: 1, Name: "one", SQL: "SELECT 1;"}, {Version: 1, Name: "again", SQL: "SELECT 1;"}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewRunner(migrations); err == nil {
				t.Fatal("NewRunner() error = nil, want error")
			}
		})
	}
}

func openTestDatabase(t *testing.T) *sql.DB {
	database, err := Open(context.Background(), Config{Path: filepath.Join(t.TempDir(), "test.db")})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

func tableExists(t *testing.T, database *sql.DB, name string) bool {
	var count int
	if err := database.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", name).Scan(&count); err != nil {
		t.Fatalf("table query error = %v", err)
	}
	return count == 1
}

func openMigratedDatabase(t *testing.T) *sql.DB {
	database := openTestDatabase(t)
	runner, err := DefaultRunner()
	if err != nil {
		t.Fatalf("DefaultRunner() error = %v", err)
	}
	if err := runner.Apply(context.Background(), database); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	return database
}
