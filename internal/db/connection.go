package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const (
	defaultBusyTimeout = 5 * time.Second
	defaultMaxOpen     = 4
	defaultMaxIdle     = 4
)

type Config struct {
	Path         string
	BusyTimeout  time.Duration
	MaxOpenConns int
	MaxIdleConns int
	Synchronous  string
}

func (config Config) withDefaults() Config {
	if config.BusyTimeout <= 0 {
		config.BusyTimeout = defaultBusyTimeout
	}
	if config.MaxOpenConns <= 0 {
		config.MaxOpenConns = defaultMaxOpen
	}
	if config.MaxIdleConns <= 0 {
		config.MaxIdleConns = defaultMaxIdle
	}
	if config.MaxIdleConns > config.MaxOpenConns {
		config.MaxIdleConns = config.MaxOpenConns
	}
	if config.Synchronous == "" {
		config.Synchronous = "NORMAL"
	}
	return config
}

func Open(ctx context.Context, input Config) (*sql.DB, error) {
	config := input.withDefaults()
	if strings.TrimSpace(config.Path) == "" {
		return nil, errors.New("database path is required")
	}
	if config.Synchronous != "FULL" && config.Synchronous != "NORMAL" && config.Synchronous != "OFF" {
		return nil, fmt.Errorf("unsupported synchronous level %q", config.Synchronous)
	}

	database, err := sql.Open("sqlite", dataSourceName(config))
	if err != nil {
		return nil, fmt.Errorf("open sqlite database: %w", err)
	}
	database.SetMaxOpenConns(config.MaxOpenConns)
	database.SetMaxIdleConns(config.MaxIdleConns)
	if err := database.PingContext(ctx); err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("ping sqlite database: %w", err)
	}
	if err := AssertConnectionPragmas(ctx, database, config); err != nil {
		_ = database.Close()
		return nil, err
	}
	return database, nil
}

func AssertConnectionPragmas(ctx context.Context, database *sql.DB, config Config) error {
	connection, err := database.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire sqlite connection: %w", err)
	}
	defer connection.Close()

	var foreignKeys int
	if err := connection.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		return fmt.Errorf("read foreign_keys pragma: %w", err)
	}
	if foreignKeys != 1 {
		return fmt.Errorf("foreign_keys pragma is %d, want 1", foreignKeys)
	}

	var busyTimeout int
	if err := connection.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		return fmt.Errorf("read busy_timeout pragma: %w", err)
	}
	wantBusyTimeout := int(config.BusyTimeout / time.Millisecond)
	if busyTimeout != wantBusyTimeout {
		return fmt.Errorf("busy_timeout pragma is %d, want %d", busyTimeout, wantBusyTimeout)
	}

	var journalMode string
	if err := connection.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode); err != nil {
		return fmt.Errorf("read journal_mode pragma: %w", err)
	}
	if !strings.EqualFold(journalMode, "wal") {
		return fmt.Errorf("journal_mode pragma is %q, want WAL", journalMode)
	}

	var synchronous int
	if err := connection.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&synchronous); err != nil {
		return fmt.Errorf("read synchronous pragma: %w", err)
	}
	wantSynchronous := map[string]int{"OFF": 0, "NORMAL": 1, "FULL": 2}[config.Synchronous]
	if synchronous != wantSynchronous {
		return fmt.Errorf("synchronous pragma is %d, want %d", synchronous, wantSynchronous)
	}
	return nil
}

func dataSourceName(config Config) string {
	path := config.Path
	if path != ":memory:" {
		path = filepath.ToSlash(path)
	}
	query := url.Values{}
	query.Add("_pragma", "journal_mode(WAL)")
	query.Add("_pragma", "busy_timeout("+strconv.FormatInt(int64(config.BusyTimeout/time.Millisecond), 10)+")")
	query.Add("_pragma", "foreign_keys(1)")
	query.Add("_pragma", "synchronous("+config.Synchronous+")")
	return "file:" + path + "?" + query.Encode()
}
