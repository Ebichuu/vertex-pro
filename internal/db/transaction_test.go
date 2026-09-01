package db

import (
	"context"
	"errors"
	"testing"
)

func TestRunImmediateCommitsAndRollsBack(t *testing.T) {
	database := openMigratedDatabase(t)
	ctx := context.Background()
	if _, err := database.ExecContext(ctx, `CREATE TABLE tx_probe (id INTEGER PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		t.Fatalf("create probe table: %v", err)
	}
	if err := RunImmediate(ctx, database, func(ctx context.Context, transaction *ImmediateTx) error {
		_, err := transaction.ExecContext(ctx, `INSERT INTO tx_probe(id, value) VALUES (1, 'committed')`)
		return err
	}); err != nil {
		t.Fatalf("commit transaction: %v", err)
	}
	var value string
	if err := database.QueryRowContext(ctx, `SELECT value FROM tx_probe WHERE id = 1`).Scan(&value); err != nil {
		t.Fatalf("read committed value: %v", err)
	}
	if value != "committed" {
		t.Fatalf("committed value = %q, want committed", value)
	}
	wantErr := errors.New("rollback sentinel")
	if err := RunImmediate(ctx, database, func(ctx context.Context, transaction *ImmediateTx) error {
		if _, err := transaction.ExecContext(ctx, `INSERT INTO tx_probe(id, value) VALUES (2, 'rolled-back')`); err != nil {
			return err
		}
		return wantErr
	}); !errors.Is(err, wantErr) {
		t.Fatalf("rollback error = %v, want sentinel", err)
	}
	var count int
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM tx_probe WHERE id = 2`).Scan(&count); err != nil {
		t.Fatalf("read rolled-back row: %v", err)
	}
	if count != 0 {
		t.Fatalf("rolled-back row count = %d, want 0", count)
	}
}

func TestRunImmediateRejectsInvalidInputsAndFinishedUse(t *testing.T) {
	ctx := context.Background()
	if err := RunImmediate(ctx, nil, func(context.Context, *ImmediateTx) error { return nil }); err == nil {
		t.Fatal("nil database error = nil, want error")
	}
	database := openMigratedDatabase(t)
	if err := RunImmediate(ctx, database, nil); err == nil {
		t.Fatal("nil callback error = nil, want error")
	}
	if err := RunImmediate(ctx, database, func(ctx context.Context, transaction *ImmediateTx) error {
		if _, err := transaction.ExecContext(ctx, `CREATE TABLE tx_finished (id INTEGER PRIMARY KEY)`); err != nil {
			return err
		}
		return nil
	}); err != nil {
		t.Fatalf("create finished probe: %v", err)
	}
}
