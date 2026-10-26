package db

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"modernc.org/sqlite"
)

var cancelFunctionID atomic.Uint64

// Cancel after SQLite enters a query, then prove the same connection can
// checkpoint and the file can be reopened for writes. Leaked driver Rows keep
// SQLite statements alive even when database/sql has released the connection.
func TestCanceledQueryReleasesSQLiteStatements(t *testing.T) {
	for attempt := range 20 {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		entered := make(chan struct{})
		name := fmt.Sprintf("vertex_cancel_%d", cancelFunctionID.Add(1))
		sqlite.MustRegisterScalarFunction(name, 0, func(*sqlite.FunctionContext, []driver.Value) (driver.Value, error) {
			close(entered)
			<-ctx.Done()
			return int64(42), nil
		})
		path := filepath.Join(t.TempDir(), "cancel.db")
		database, err := Open(context.Background(), Config{Path: path, MaxOpenConns: 1})
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		connection, err := database.Conn(context.Background())
		if err != nil {
			cancel()
			database.Close()
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() {
			rows, err := connection.QueryContext(ctx, "SELECT "+name+"()")
			if rows != nil {
				rows.Close()
			}
			done <- err
		}()
		select {
		case <-entered:
		case <-ctx.Done():
			cancel()
			<-done
			connection.Close()
			database.Close()
			t.Fatal("query did not enter SQLite function")
		}
		cancel()
		queryErr := <-done
		_, checkpointErr := connection.ExecContext(context.Background(), "PRAGMA wal_checkpoint(TRUNCATE)")
		connection.Close()
		closeErr := database.Close()
		// Cancellation can race with the final row becoming available. Either
		// result is valid, provided both paths release the statement.
		if (queryErr != nil && !errors.Is(queryErr, context.Canceled)) || checkpointErr != nil || closeErr != nil {
			t.Fatalf("attempt=%d query=%v checkpoint=%v close=%v", attempt, queryErr, checkpointErr, closeErr)
		}
		reopened, err := Open(context.Background(), Config{Path: path})
		if err != nil {
			t.Fatal(err)
		}
		_, writeErr := reopened.Exec("CREATE TABLE after_cancel(id INTEGER)")
		reopened.Close()
		if writeErr != nil {
			t.Fatal(writeErr)
		}
	}
}
