package db

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestOpenConfiguresEveryConnection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vertex-pro.db")
	config := Config{Path: path, BusyTimeout: 3 * time.Second, MaxOpenConns: 2, MaxIdleConns: 1}
	database, err := Open(context.Background(), config)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer database.Close()

	if got := database.Stats().MaxOpenConnections; got != config.MaxOpenConns {
		t.Fatalf("max open connections = %d, want %d", got, config.MaxOpenConns)
	}
	connections := make([]*sql.Conn, 0, config.MaxOpenConns)
	for range config.MaxOpenConns {
		connection, err := database.Conn(context.Background())
		if err != nil {
			t.Fatalf("Conn() error = %v", err)
		}
		connections = append(connections, connection)
	}
	for _, connection := range connections {
		if err := assertConnectionValues(connection, config); err != nil {
			connection.Close()
			t.Fatal(err)
		}
		if err := connection.Close(); err != nil {
			t.Fatalf("connection close error = %v", err)
		}
	}
}

func TestOpenRejectsInvalidConfig(t *testing.T) {
	for name, config := range map[string]Config{
		"missing path":        {Synchronous: "NORMAL"},
		"invalid synchronous": {Path: ":memory:", Synchronous: "FAST"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Open(context.Background(), config); err == nil {
				t.Fatal("Open() error = nil, want error")
			}
		})
	}
}

func assertConnectionValues(connection *sql.Conn, config Config) error {
	var foreignKeys int
	if err := connection.QueryRowContext(context.Background(), "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		return err
	}
	if foreignKeys != 1 {
		return fmt.Errorf("foreign_keys = %d, want 1", foreignKeys)
	}
	var journalMode string
	if err := connection.QueryRowContext(context.Background(), "PRAGMA journal_mode").Scan(&journalMode); err != nil {
		return err
	}
	if journalMode != "wal" {
		return fmt.Errorf("journal_mode = %q, want wal", journalMode)
	}
	var busyTimeout int
	if err := connection.QueryRowContext(context.Background(), "PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		return err
	}
	wantBusyTimeout := int(config.BusyTimeout / time.Millisecond)
	if busyTimeout != wantBusyTimeout {
		return fmt.Errorf("busy_timeout = %d, want %d", busyTimeout, wantBusyTimeout)
	}
	var synchronous int
	if err := connection.QueryRowContext(context.Background(), "PRAGMA synchronous").Scan(&synchronous); err != nil {
		return err
	}
	if synchronous != 1 {
		return fmt.Errorf("synchronous = %d, want 1", synchronous)
	}
	return nil
}
