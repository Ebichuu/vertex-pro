package app

import (
	"context"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
	"github.com/vertex-pro/vertex-pro/internal/rsssourcedb"
	"github.com/vertex-pro/vertex-pro/internal/schedulercycle"
)

func (runtime *Runtime) recordFeedPoll(ctx context.Context, feed schedulercycle.FeedRef, result schedulercycle.Result, pollErr error) error {
	now := runtime.now().UTC().Format(time.RFC3339Nano)
	if !result.PolledAt.IsZero() {
		now = result.PolledAt.UTC().Format(time.RFC3339Nano)
	}
	code := ""
	if pollErr != nil {
		code = "RSS_FETCH_OR_PARSE_FAILED"
	} else if result.Invalid > 0 {
		code = "RSS_ENTRY_INVALID"
	} else if result.Failed > 0 {
		code = "RSS_ITEM_PROCESSING_FAILED"
	}
	return db.RunImmediate(ctx, runtime.Database, func(ctx context.Context, tx *db.ImmediateTx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO feed_runtime_state(feed_id,initialized,last_polled_at,last_success_at,last_error_code,last_entries,last_matched) VALUES(?,?,?,?,?,?,?) ON CONFLICT(feed_id) DO UPDATE SET initialized=CASE WHEN excluded.initialized=1 THEN 1 ELSE initialized END,last_polled_at=excluded.last_polled_at,last_success_at=COALESCE(excluded.last_success_at,last_success_at),last_error_code=excluded.last_error_code,last_entries=excluded.last_entries,last_matched=excluded.last_matched`, feed.FeedID, pollErr == nil, now, successAt(pollErr, now), code, result.Entries, result.Matched); err != nil {
			return err
		}
		if pollErr == nil {
			if len(result.Outcomes) == 0 && !result.PolledAt.IsZero() {
				if err := rsssourcedb.RefreshLastMatched(ctx, tx, feed.FeedID); err != nil {
					return err
				}
			}
			_, err := tx.ExecContext(ctx, `UPDATE feeds SET etag=?,last_modified=? WHERE feed_id=?`, result.ETag, result.LastModified, feed.FeedID)
			return err
		}
		return nil
	})
}

func successAt(err error, now string) any {
	if err != nil {
		return nil
	}
	return now
}
