package rsssourcedb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
	"github.com/vertex-pro/vertex-pro/internal/rsssource"
)

// Fixed precision makes SQLite lexical timestamp comparisons chronological.
func stamp(at time.Time) string { return at.UTC().Format("2006-01-02T15:04:05.000000000Z") }

func (store *Store) Discover(ctx context.Context, site, feed string, entries []rsssource.Entry, baseline bool, now time.Time) (rsssource.Result, error) {
	result := rsssource.Result{SiteID: site}
	sources := make([]rsssource.Source, 0, len(entries))
	for index, entry := range entries {
		source, err := rsssource.Normalize(site, entry)
		if err != nil {
			result.Invalid = append(result.Invalid, rsssource.InvalidEntry{Index: index, Code: "RSS_ENTRY_INVALID"})
			continue
		}
		sources = append(sources, source)
	}
	err := db.RunImmediate(ctx, store.database, func(ctx context.Context, tx *db.ImmediateTx) error {
		var feedSite string
		if err := tx.QueryRowContext(ctx, `SELECT site_id FROM feeds WHERE feed_id=?`, feed).Scan(&feedSite); err != nil {
			return err
		}
		if feedSite != site {
			return fmt.Errorf("feed site mismatch")
		}
		for _, issue := range result.Invalid {
			data, err := json.Marshal(map[string]any{"feed_id": feed, "entry_index": issue.Index})
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO system_events(event_type,severity,code,safe_message,safe_data_json,occurred_at,expires_at) VALUES('system.alert','WARNING',?,'RSS entry rejected; valid entries continue',?,?,?)`, issue.Code, string(data), stamp(now), stamp(now.Add(7*24*time.Hour))); err != nil {
				return err
			}
		}
		seen := map[string]bool{}
		for _, source := range sources {
			if seen[source.SourceKey] {
				result.Duplicates++
				continue
			}
			seen[source.SourceKey] = true
			var fingerprint string
			var legacy int
			err := tx.QueryRowContext(ctx, `SELECT fingerprint,legacy_seen FROM rss_sources WHERE site_id=? AND source_key=?`, site, source.SourceKey).Scan(&fingerprint, &legacy)
			if errors.Is(err, sql.ErrNoRows) {
				_, err = tx.ExecContext(ctx, `INSERT INTO rss_sources(site_id,source_key,guid,url,title,size_bytes,fingerprint,first_seen_at,updated_at,legacy_seen) VALUES(?,?,?,?,?,?,?,?,?,0)`, site, source.SourceKey, source.GUID, source.URL, source.Title, source.SizeBytes, source.Fingerprint, stamp(now), stamp(now))
				result.New = append(result.New, source)
			} else if err == nil && fingerprint != source.Fingerprint {
				_, err = tx.ExecContext(ctx, `UPDATE rss_sources SET guid=?,url=?,title=?,size_bytes=?,fingerprint=?,updated_at=? WHERE site_id=? AND source_key=?`, source.GUID, source.URL, source.Title, source.SizeBytes, source.Fingerprint, stamp(now), site, source.SourceKey)
				result.Updated = append(result.Updated, source)
			} else if err == nil {
				result.Duplicates++
			}
			if err != nil {
				return err
			}
			state := "PENDING"
			if baseline || legacy == 1 {
				state = "SKIPPED"
			}
			raw, err := json.Marshal(source)
			if err != nil {
				return err
			}
			// Keep each consumer's original input, including its URL/title/publication.
			// Another overlapping feed may update the shared source, but not this work.
			if _, err := tx.ExecContext(ctx, `INSERT INTO rss_source_consumers(site_id,feed_id,source_key,source_json,state,next_attempt_at,deadline_at,discovered_at) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(site_id,feed_id,source_key) DO NOTHING`, site, feed, source.SourceKey, string(raw), state, stamp(now), stamp(now.Add(rsssource.ConsumerLifetime)), stamp(now)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return rsssource.Result{}, err
	}
	return result, nil
}

func (store *Store) Claim(ctx context.Context, site, feed string, now time.Time) (*rsssource.Consumer, error) {
	var result *rsssource.Consumer
	err := db.RunImmediate(ctx, store.database, func(ctx context.Context, tx *db.ImmediateTx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE rss_source_consumers SET state='FAILED',last_error_code=CASE WHEN deadline_at<=? THEN 'PROCESSING_DEADLINE' ELSE 'PROCESSING_ATTEMPTS_EXHAUSTED' END WHERE site_id=? AND feed_id=? AND state='PENDING' AND (deadline_at<=? OR (attempts>=? AND next_attempt_at<=?))`, stamp(now), site, feed, stamp(now), rsssource.MaxConsumerAttempts, stamp(now)); err != nil {
			return err
		}
		var raw, key, deadline string
		var attempts int
		err := tx.QueryRowContext(ctx, `SELECT source_key,source_json,attempts,deadline_at FROM rss_source_consumers WHERE site_id=? AND feed_id=? AND state='PENDING' AND attempts<? AND next_attempt_at<=? AND deadline_at>? ORDER BY next_attempt_at,source_key LIMIT 1`, site, feed, rsssource.MaxConsumerAttempts, stamp(now), stamp(now)).Scan(&key, &raw, &attempts, &deadline)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		var source rsssource.Source
		if err := json.Unmarshal([]byte(raw), &source); err != nil {
			return err
		}
		at, err := time.Parse(time.RFC3339Nano, deadline)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE rss_source_consumers SET attempts=attempts+1,next_attempt_at=?,rate_wait_started_at=NULL,preparation_started_at=? WHERE site_id=? AND feed_id=? AND source_key=?`, stamp(now.Add(rsssource.ConsumerLease)), stamp(now), site, feed, key); err != nil {
			return err
		}
		result = &rsssource.Consumer{Source: source, FeedID: feed, Attempts: attempts + 1, Deadline: at}
		return nil
	})
	return result, err
}

func (store *Store) Finish(ctx context.Context, work rsssource.Consumer, code string, now time.Time) error {
	state := "DONE"
	if code != "" {
		state = "PENDING"
		if work.Attempts >= rsssource.MaxConsumerAttempts || !now.Before(work.Deadline) {
			state = "FAILED"
		}
	}
	return db.RunImmediate(ctx, store.database, func(ctx context.Context, tx *db.ImmediateTx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE rss_source_consumers SET state=?,last_error_code=?,next_attempt_at=?,matched=?,rate_wait_started_at=NULL,preparation_started_at=NULL WHERE site_id=? AND feed_id=? AND source_key=? AND state='PENDING' AND attempts=?`, state, code, stamp(now.Add(rsssource.ConsumerRetry(work.Attempts))), work.Matched && code == "", work.Source.SiteID, work.FeedID, work.Source.SourceKey, work.Attempts); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE feed_runtime_state SET last_matched=(SELECT count(*) FROM rss_source_consumers c WHERE c.feed_id=feed_runtime_state.feed_id AND c.matched=1 AND julianday(c.discovered_at)>=julianday(feed_runtime_state.last_polled_at)) WHERE feed_id=?`, work.FeedID)
		return err
	})
}

func (store *Store) RecordRateWait(ctx context.Context, work rsssource.Consumer, waiting bool, elapsed time.Duration, now time.Time) error {
	var started any
	if waiting {
		started = stamp(now)
	}
	_, err := store.database.ExecContext(ctx, `UPDATE rss_source_consumers SET rate_wait_started_at=?,rate_wait_ms=rate_wait_ms+? WHERE site_id=? AND feed_id=? AND source_key=? AND state='PENDING' AND attempts=?`, started, max(0, elapsed.Milliseconds()), work.Source.SiteID, work.FeedID, work.Source.SourceKey, work.Attempts)
	return err
}
