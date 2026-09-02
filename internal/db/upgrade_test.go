package db

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpgradeCreatesBackupAndMigratesSupportedOldSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vertex-pro.db")
	config := Config{Path: path}
	database := openTestDatabaseAtPath(t, path)
	oldRunner, err := NewRunner([]Migration{{Version: 1, Name: "baseline", SQL: migrationsSQL(t, 1)}})
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	if err := oldRunner.Apply(context.Background(), database); err != nil {
		t.Fatalf("old schema Apply() error = %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close old database: %v", err)
	}

	backupPath := filepath.Join(t.TempDir(), "vertex-pro.db.bak")
	actualBackup, err := Upgrade(context.Background(), config, UpgradeOptions{BackupPath: backupPath})
	if err != nil {
		t.Fatalf("Upgrade() error = %v", err)
	}
	if actualBackup != backupPath {
		t.Fatalf("backup path = %q, want %q", actualBackup, backupPath)
	}
	if _, err := os.Stat(backupPath); err != nil {
		t.Fatalf("backup stat error = %v", err)
	}
	upgraded := openTestDatabaseAtPath(t, path)
	defer upgraded.Close()
	runner, err := DefaultRunner()
	if err != nil {
		t.Fatalf("DefaultRunner() error = %v", err)
	}
	assertMigrationCount(t, upgraded, len(runner.Migrations))
	if err := CheckStartupConsistency(context.Background(), upgraded); err != nil {
		t.Fatalf("upgraded consistency error = %v", err)
	}
}

func TestUpgradeRestoresOriginalDatabaseWhenMigrationFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vertex-pro.db")
	database := openTestDatabaseAtPath(t, path)
	if _, err := database.Exec(`CREATE TABLE legacy_marker (value TEXT NOT NULL); INSERT INTO legacy_marker(value) VALUES ('original');`); err != nil {
		t.Fatalf("create legacy database: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close legacy database: %v", err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read original database: %v", err)
	}
	brokenRunner, err := NewRunner([]Migration{{Version: 1, Name: "broken", SQL: "THIS IS NOT SQL;"}})
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	backupPath, err := Upgrade(context.Background(), Config{Path: path}, UpgradeOptions{BackupPath: filepath.Join(t.TempDir(), "backup.db"), Runner: brokenRunner})
	if err == nil {
		t.Fatal("Upgrade() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "original database restored") {
		t.Fatalf("Upgrade() error = %v, want restore marker", err)
	}
	if backupPath == "" {
		t.Fatal("backup path is empty")
	}
	restored, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read restored database: %v", err)
	}
	if string(restored) != string(original) {
		t.Fatal("restored database bytes differ from original")
	}
	check := openTestDatabaseAtPath(t, path)
	defer check.Close()
	var marker string
	if err := check.QueryRow("SELECT value FROM legacy_marker").Scan(&marker); err != nil {
		t.Fatalf("read restored marker: %v", err)
	}
	if marker != "original" {
		t.Fatalf("marker = %q, want original", marker)
	}
}

func TestRestoreReplacesDatabaseFromBackup(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "vertex-pro.db")
	backupPath := filepath.Join(directory, "backup.db")
	database := openTestDatabaseAtPath(t, path)
	if _, err := database.Exec("CREATE TABLE current_marker (value TEXT NOT NULL); INSERT INTO current_marker(value) VALUES ('current');"); err != nil {
		t.Fatalf("create current database: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close current database: %v", err)
	}
	backupDatabase := openTestDatabaseAtPath(t, backupPath)
	if _, err := backupDatabase.Exec("CREATE TABLE backup_marker (value TEXT NOT NULL); INSERT INTO backup_marker(value) VALUES ('backup');"); err != nil {
		t.Fatalf("create backup database: %v", err)
	}
	if err := backupDatabase.Close(); err != nil {
		t.Fatalf("close backup database: %v", err)
	}
	if err := Restore(Config{Path: path}, backupPath); err != nil {
		t.Fatalf("Restore() error = %v", err)
	}
	restored := openTestDatabaseAtPath(t, path)
	defer restored.Close()
	var marker string
	if err := restored.QueryRow("SELECT value FROM backup_marker").Scan(&marker); err != nil {
		t.Fatalf("read backup marker: %v", err)
	}
	if marker != "backup" {
		t.Fatalf("marker = %q, want backup", marker)
	}
}

func assertMigrationCount(t *testing.T, database *sql.DB, want int) {
	t.Helper()
	var count int
	if err := database.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&count); err != nil {
		t.Fatalf("migration count query error = %v", err)
	}
	if count != want {
		t.Fatalf("migration count = %d, want %d", count, want)
	}
}
func openTestDatabaseAtPath(t *testing.T, path string) *sql.DB {
	t.Helper()
	database, err := Open(context.Background(), Config{Path: path})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	return database
}

func migrationsSQL(t *testing.T, version int) string {
	t.Helper()
	runner, err := DefaultRunner()
	if err != nil {
		t.Fatalf("DefaultRunner() error = %v", err)
	}
	for _, migration := range runner.Migrations {
		if migration.Version == version {
			return migration.SQL
		}
	}
	t.Fatalf("migration %d not found", version)
	return ""
}
