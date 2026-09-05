package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"

	dbmigrations "github.com/vertex-pro/vertex-pro/internal/db/migrations"
)

const migrationTable = `CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    checksum TEXT NOT NULL,
    applied_at TEXT NOT NULL
)`

type Migration struct {
	Version int
	Name    string
	SQL     string
}

type Runner struct {
	Migrations []Migration
}

func NewRunner(migrations []Migration) (Runner, error) {
	ordered := append([]Migration(nil), migrations...)
	sort.Slice(ordered, func(left, right int) bool { return ordered[left].Version < ordered[right].Version })
	for index, migration := range ordered {
		if migration.Version <= 0 || strings.TrimSpace(migration.Name) == "" || strings.TrimSpace(migration.SQL) == "" {
			return Runner{}, fmt.Errorf("invalid migration at index %d", index)
		}
		if index > 0 && ordered[index-1].Version == migration.Version {
			return Runner{}, fmt.Errorf("duplicate migration version %d", migration.Version)
		}
	}
	return Runner{Migrations: ordered}, nil
}

func DefaultRunner() (Runner, error) {
	migrations, err := embeddedMigrations(dbmigrations.FS)
	if err != nil {
		return Runner{}, err
	}
	return NewRunner(migrations)
}

func (runner Runner) Apply(ctx context.Context, database *sql.DB) error {
	if database == nil {
		return errors.New("database is required")
	}
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = transaction.Rollback()
		}
	}()

	if _, err := transaction.ExecContext(ctx, migrationTable); err != nil {
		return fmt.Errorf("create migration table: %w", err)
	}
	applied, err := appliedMigrations(ctx, transaction)
	if err != nil {
		return err
	}
	for _, migration := range runner.Migrations {
		checksum := checksum(migration.SQL)
		if existing, ok := applied[migration.Version]; ok {
			if existing.name != migration.Name || existing.checksum != checksum {
				return fmt.Errorf("migration %d checksum mismatch: database has %s/%s, binary has %s/%s", migration.Version, existing.name, existing.checksum, migration.Name, checksum)
			}
			continue
		}
		if _, err := transaction.ExecContext(ctx, migration.SQL); err != nil {
			return fmt.Errorf("apply migration %d %s: %w", migration.Version, migration.Name, err)
		}
		if _, err := transaction.ExecContext(ctx, "INSERT INTO schema_migrations(version, name, checksum, applied_at) VALUES (?, ?, ?, ?)", migration.Version, migration.Name, checksum, time.Now().UTC().Format(time.RFC3339)); err != nil {
			return fmt.Errorf("record migration %d %s: %w", migration.Version, migration.Name, err)
		}
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit migrations: %w", err)
	}
	committed = true
	return nil
}

type appliedMigration struct {
	name     string
	checksum string
}

func appliedMigrations(ctx context.Context, transaction *sql.Tx) (map[int]appliedMigration, error) {
	rows, err := transaction.QueryContext(ctx, "SELECT version, name, checksum FROM schema_migrations ORDER BY version")
	if err != nil {
		return nil, fmt.Errorf("read applied migrations: %w", err)
	}
	defer rows.Close()
	applied := make(map[int]appliedMigration)
	for rows.Next() {
		var version int
		var migration appliedMigration
		if err := rows.Scan(&version, &migration.name, &migration.checksum); err != nil {
			return nil, fmt.Errorf("scan applied migration: %w", err)
		}
		applied[version] = migration
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate applied migrations: %w", err)
	}
	return applied, nil
}

func checksum(sqlText string) string {
	digest := sha256.Sum256([]byte(sqlText))
	return hex.EncodeToString(digest[:])
}

func embeddedMigrations(files embed.FS) ([]Migration, error) {
	entries, err := fs.ReadDir(files, ".")
	if err != nil {
		return nil, fmt.Errorf("read embedded migrations: %w", err)
	}
	migrations := make([]Migration, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		version, name, err := parseMigrationName(entry.Name())
		if err != nil {
			return nil, err
		}
		sqlBytes, err := fs.ReadFile(files, entry.Name())
		if err != nil {
			return nil, fmt.Errorf("read embedded migration %s: %w", entry.Name(), err)
		}
		migrations = append(migrations, Migration{Version: version, Name: name, SQL: string(sqlBytes)})
	}
	return migrations, nil
}

func parseMigrationName(filename string) (int, string, error) {
	base := strings.TrimSuffix(filename, ".sql")
	parts := strings.SplitN(base, "_", 2)
	if len(parts) != 2 {
		return 0, "", fmt.Errorf("invalid migration filename %q", filename)
	}
	version, err := strconv.Atoi(parts[0])
	if err != nil || version <= 0 || parts[1] == "" {
		return 0, "", fmt.Errorf("invalid migration filename %q", filename)
	}
	return version, parts[1], nil
}

// AppliedVersion 返回已应用的最高迁移版本；schema_migrations 尚不存在或
// 为空时返回 0（调用方据此区分"未迁移"与"已迁移"）。
func (runner Runner) AppliedVersion(ctx context.Context, database *sql.DB) (int, error) {
	var version sql.NullInt64
	if err := database.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil {
		return 0, err
	}
	return int(version.Int64), nil
}
