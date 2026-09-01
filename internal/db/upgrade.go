package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// UpgradeOptions controls the file-level safety boundary around migrations.
type UpgradeOptions struct {
	BackupPath string
	Runner     Runner
	Check      func(context.Context, *sql.DB) error
}

// Upgrade creates a cold backup before applying migrations. If migration or
// consistency validation fails, the database file is restored from that backup.
func Upgrade(ctx context.Context, config Config, options UpgradeOptions) (string, error) {
	if strings.TrimSpace(config.Path) == "" || config.Path == ":memory:" {
		return "", errors.New("file-backed database path is required")
	}
	if _, err := os.Stat(config.Path); err != nil {
		return "", fmt.Errorf("stat database: %w", err)
	}
	backupPath := options.BackupPath
	if strings.TrimSpace(backupPath) == "" {
		backupPath = config.Path + ".pre-upgrade.bak"
	}
	if samePath(config.Path, backupPath) {
		return "", errors.New("backup path must differ from database path")
	}
	runner := options.Runner
	if len(runner.Migrations) == 0 {
		var err error
		runner, err = DefaultRunner()
		if err != nil {
			return "", fmt.Errorf("load default migrations: %w", err)
		}
	}
	check := options.Check
	if check == nil {
		check = CheckStartupConsistency
	}

	database, err := Open(ctx, config)
	if err != nil {
		return "", err
	}
	if err := checkpointAndClose(ctx, database, config.Path); err != nil {
		return "", err
	}
	if err := copyDatabaseFile(config.Path, backupPath); err != nil {
		return "", fmt.Errorf("create database backup: %w", err)
	}

	database, err = Open(ctx, config)
	if err == nil {
		err = runner.Apply(ctx, database)
		if err == nil {
			err = check(ctx, database)
		}
		closeErr := database.Close()
		if err == nil {
			err = closeErr
		}
	}
	if err == nil {
		return backupPath, nil
	}
	if restoreErr := restoreDatabaseFile(backupPath, config.Path); restoreErr != nil {
		return backupPath, fmt.Errorf("upgrade failed: %w; restore failed: %v", err, restoreErr)
	}
	return backupPath, fmt.Errorf("upgrade failed; original database restored: %w", err)
}

// Restore replaces a file-backed database with a previously created backup.
func Restore(config Config, backupPath string) error {
	if strings.TrimSpace(config.Path) == "" || config.Path == ":memory:" {
		return errors.New("file-backed database path is required")
	}
	if strings.TrimSpace(backupPath) == "" {
		return errors.New("backup path is required")
	}
	if samePath(config.Path, backupPath) {
		return errors.New("backup path must differ from database path")
	}
	if _, err := os.Stat(backupPath); err != nil {
		return fmt.Errorf("stat backup: %w", err)
	}
	return restoreDatabaseFile(backupPath, config.Path)
}

func checkpointAndClose(ctx context.Context, database *sql.DB, path string) error {
	checkpointErr := func() error {
		_, err := database.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)")
		return err
	}()
	closeErr := database.Close()
	if checkpointErr != nil {
		return fmt.Errorf("checkpoint WAL for %s: %w", path, checkpointErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close database %s: %w", path, closeErr)
	}
	return nil
}

func copyDatabaseFile(sourcePath, destinationPath string) error {
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()
	if err := os.MkdirAll(filepath.Dir(destinationPath), 0o755); err != nil {
		return err
	}
	temporaryPath := destinationPath + ".tmp-upgrade"
	_ = os.Remove(temporaryPath)
	destination, err := os.OpenFile(temporaryPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(destination, source); err != nil {
		_ = destination.Close()
		_ = os.Remove(temporaryPath)
		return err
	}
	if err := destination.Sync(); err != nil {
		_ = destination.Close()
		_ = os.Remove(temporaryPath)
		return err
	}
	if err := destination.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		return err
	}
	if err := os.Rename(temporaryPath, destinationPath); err != nil {
		_ = os.Remove(temporaryPath)
		return err
	}
	return nil
}

func restoreDatabaseFile(backupPath, databasePath string) error {
	removeSQLiteSidecars(databasePath)
	return copyDatabaseFile(backupPath, databasePath)
}

func removeSQLiteSidecars(databasePath string) {
	_ = os.Remove(databasePath + "-wal")
	_ = os.Remove(databasePath + "-shm")
}

func samePath(left, right string) bool {
	leftAbs, leftErr := filepath.Abs(left)
	rightAbs, rightErr := filepath.Abs(right)
	if leftErr != nil || rightErr != nil {
		return filepath.Clean(left) == filepath.Clean(right)
	}
	return strings.EqualFold(filepath.Clean(leftAbs), filepath.Clean(rightAbs))
}
