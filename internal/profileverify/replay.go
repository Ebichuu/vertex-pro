package profileverify

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
)

// ListRuns returns every persisted profile verification run, ordered by
// campaign and finished time. It is the export half of the campaign replay
// path: the production assembly consumes these rows through Record, which is
// idempotent for identical runs and refuses conflicting ones.
func ListRuns(ctx context.Context, database *sql.DB) ([]Run, error) {
	if database == nil {
		return nil, fmt.Errorf("database is required")
	}
	ids := make([]string, 0, 16)
	rows, err := database.QueryContext(ctx, `SELECT run_id FROM profile_verification_runs ORDER BY campaign_id, finished_at, run_id`)
	if err != nil {
		return nil, fmt.Errorf("list profile verification runs: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var runID string
		if err := rows.Scan(&runID); err != nil {
			return nil, err
		}
		ids = append(ids, runID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Strings(ids)

	runs := make([]Run, 0, len(ids))
	for _, runID := range ids {
		run, found, err := loadPersistedRunByID(ctx, database, runID)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, fmt.Errorf("persisted run %s disappeared during export", runID)
		}
		runs = append(runs, run)
	}
	return runs, nil
}
