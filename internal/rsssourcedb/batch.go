package rsssourcedb

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/rsssource"
)

// Multi-row statements stay well below SQLite's bound-parameter limit.
const batchRows = 100

type txQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

type knownSource struct {
	fingerprint string
	legacy      bool
}

type consumerRow struct {
	key, json, state string
}

// knownSources reads the dedupe state of a whole poll in one query per batch.
func knownSources(ctx context.Context, tx txQuerier, site string, sources []rsssource.Source) (map[string]knownSource, error) {
	known := make(map[string]knownSource, len(sources))
	for start := 0; start < len(sources); start += batchRows {
		chunk := sources[start:min(start+batchRows, len(sources))]
		args := make([]any, 0, len(chunk)+1)
		args = append(args, site)
		for _, source := range chunk {
			args = append(args, source.SourceKey)
		}
		rows, err := tx.QueryContext(ctx, `SELECT source_key,fingerprint,legacy_seen FROM rss_sources WHERE site_id=? AND source_key IN (`+placeholders(len(chunk))+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var key string
			var source knownSource
			if err := rows.Scan(&key, &source.fingerprint, &source.legacy); err != nil {
				rows.Close()
				return nil, err
			}
			known[key] = source
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return known, nil
}

func insertSources(ctx context.Context, tx txQuerier, site string, sources []rsssource.Source, now time.Time) error {
	at := stamp(now)
	for start := 0; start < len(sources); start += batchRows {
		chunk := sources[start:min(start+batchRows, len(sources))]
		args := make([]any, 0, len(chunk)*9)
		for _, source := range chunk {
			args = append(args, site, source.SourceKey, source.GUID, source.URL, source.Title, source.SizeBytes, source.Fingerprint, at, at)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO rss_sources(site_id,source_key,guid,url,title,size_bytes,fingerprint,first_seen_at,updated_at,legacy_seen) VALUES `+rowPlaceholders(len(chunk), 9, ",0"), args...); err != nil {
			return err
		}
	}
	return nil
}

func insertConsumers(ctx context.Context, tx txQuerier, site, feed string, consumers []consumerRow, now time.Time) error {
	at, deadline := stamp(now), stamp(now.Add(rsssource.ConsumerLifetime))
	for start := 0; start < len(consumers); start += batchRows {
		chunk := consumers[start:min(start+batchRows, len(consumers))]
		args := make([]any, 0, len(chunk)*8)
		for _, consumer := range chunk {
			args = append(args, site, feed, consumer.key, consumer.json, consumer.state, at, deadline, at)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO rss_source_consumers(site_id,feed_id,source_key,source_json,state,next_attempt_at,deadline_at,discovered_at) VALUES `+rowPlaceholders(len(chunk), 8, "")+` ON CONFLICT(site_id,feed_id,source_key) DO NOTHING`, args...); err != nil {
			return err
		}
	}
	return nil
}

// RefreshLastMatched recounts matched consumers discovered since the feed's
// last poll. discovered_at uses the fixed-width consumer stamp, so comparing it
// against the poll time in the same format is chronological and uses
// ix_consumer_matched_discovered; last_polled_at is stored as RFC3339Nano.
func RefreshLastMatched(ctx context.Context, tx txQuerier, feedID string) error {
	var polled string
	err := tx.QueryRowContext(ctx, `SELECT last_polled_at FROM feed_runtime_state WHERE feed_id=?`, feedID).Scan(&polled)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	at, err := time.Parse(time.RFC3339Nano, polled)
	if err != nil {
		// Unparseable stamps keep the former (unindexed) semantics.
		_, err = tx.ExecContext(ctx, `UPDATE feed_runtime_state SET last_matched=(SELECT count(*) FROM rss_source_consumers c WHERE c.feed_id=feed_runtime_state.feed_id AND c.matched=1 AND julianday(c.discovered_at)>=julianday(feed_runtime_state.last_polled_at)) WHERE feed_id=?`, feedID)
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE feed_runtime_state SET last_matched=(SELECT count(*) FROM rss_source_consumers c WHERE c.feed_id=? AND c.matched=1 AND c.discovered_at>=?) WHERE feed_id=?`, feedID, stamp(at), feedID)
	return err
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// rowPlaceholders renders n "(?,...,?<suffix>)" groups of width columns.
func rowPlaceholders(n, width int, suffix string) string {
	row := "(" + placeholders(width) + suffix + ")"
	return strings.TrimSuffix(strings.Repeat(row+",", n), ",")
}
