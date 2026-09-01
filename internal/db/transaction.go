package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ImmediateTx is a SQLite transaction opened with BEGIN IMMEDIATE.
type ImmediateTx struct {
	connection *sql.Conn
	finished   bool
}

// RunImmediate executes callback in a serialized SQLite write transaction.
func RunImmediate(ctx context.Context, database *sql.DB, callback func(context.Context, *ImmediateTx) error) error {
	if database == nil {
		return errors.New("database is required")
	}
	if callback == nil {
		return errors.New("transaction callback is required")
	}
	connection, err := database.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire transaction connection: %w", err)
	}
	defer connection.Close()
	if _, err := connection.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("begin immediate transaction: %w", err)
	}
	transaction := &ImmediateTx{connection: connection}
	defer func() {
		if !transaction.finished {
			_, _ = connection.ExecContext(context.Background(), "ROLLBACK")
		}
	}()
	if err := callback(ctx, transaction); err != nil {
		return fmt.Errorf("transaction callback: %w", err)
	}
	if _, err := connection.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("commit immediate transaction: %w", err)
	}
	transaction.finished = true
	return nil
}

// ExecContext executes a statement inside the immediate transaction.
func (transaction *ImmediateTx) ExecContext(ctx context.Context, query string, arguments ...any) (sql.Result, error) {
	if err := transaction.ensureOpen(); err != nil {
		return nil, err
	}
	return transaction.connection.ExecContext(ctx, query, arguments...)
}

// QueryContext executes a query inside the immediate transaction.
func (transaction *ImmediateTx) QueryContext(ctx context.Context, query string, arguments ...any) (*sql.Rows, error) {
	if err := transaction.ensureOpen(); err != nil {
		return nil, err
	}
	return transaction.connection.QueryContext(ctx, query, arguments...)
}

// QueryRowContext executes a single-row query inside the immediate transaction.
func (transaction *ImmediateTx) QueryRowContext(ctx context.Context, query string, arguments ...any) *sql.Row {
	if transaction.ensureOpen() != nil {
		return &sql.Row{}
	}
	return transaction.connection.QueryRowContext(ctx, query, arguments...)
}

func (transaction *ImmediateTx) ensureOpen() error {
	if transaction == nil || transaction.connection == nil {
		return errors.New("transaction is nil")
	}
	if transaction.finished {
		return errors.New("transaction is already finished")
	}
	return nil
}
