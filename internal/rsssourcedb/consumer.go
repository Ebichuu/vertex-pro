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
		unique := make([]rsssource.Source, 0, len(sources))
		seen := make(map[string]bool, len(sources))
		for _, source := range sources {
			if seen[source.SourceKey] {
				result.Duplicates++
				continue
			}
			seen[source.SourceKey] = true
			unique = append(unique, source)
		}
		existing, err := knownSources(ctx, tx, site, unique)
		if err != nil {
			return err
		}
		var inserts []rsssource.Source
		consumers := make([]consumerRow, 0, len(unique))
		for _, source := range unique {
			known, exists := existing[source.SourceKey]
			switch {
			case !exists:
				inserts = append(inserts, source)
				result.New = append(result.New, source)
			case known.fingerprint != source.Fingerprint:
				if _, err := tx.ExecContext(ctx, `UPDATE rss_sources SET guid=?,url=?,title=?,size_bytes=?,fingerprint=?,updated_at=? WHERE site_id=? AND source_key=?`, source.GUID, source.URL, source.Title, source.SizeBytes, source.Fingerprint, stamp(now), site, source.SourceKey); err != nil {
					return err
				}
				result.Updated = append(result.Updated, source)
			default:
				result.Duplicates++
			}
			state := "PENDING"
			if baseline || known.legacy {
				state = "SKIPPED"
			}
			raw, err := json.Marshal(source)
			if err != nil {
				return err
			}
			// Keep each consumer's original input, including its URL/title/publication.
			// Another overlapping feed may update the shared source, but not this work.
			consumers = append(consumers, consumerRow{key: source.SourceKey, json: string(raw), state: state})
		}
		if err := insertSources(ctx, tx, site, inserts, now); err != nil {
			return err
		}
		return insertConsumers(ctx, tx, site, feed, consumers, now)
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
		var raw, key, deadline, resume, discovered string
		var attempts int
		var generation int64
		var redirects int
		err := tx.QueryRowContext(ctx, `SELECT source_key,source_json,attempts,deadline_at,claim_generation,artifact_resume_url,artifact_redirects,discovered_at FROM rss_source_consumers WHERE site_id=? AND feed_id=? AND state='PENDING' AND attempts<? AND next_attempt_at<=? AND deadline_at>? AND (artifact_deferred_until IS NULL OR artifact_deferred_until<=?) ORDER BY next_attempt_at,source_key LIMIT 1`, site, feed, rsssource.MaxConsumerAttempts, stamp(now), stamp(now), stamp(time.Now())).Scan(&key, &raw, &attempts, &deadline, &generation, &resume, &redirects, &discovered)
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
		if _, err := tx.ExecContext(ctx, `UPDATE rss_source_consumers SET attempts=attempts+1,claim_generation=claim_generation+1,next_attempt_at=?,rate_wait_ms=rate_wait_ms+CASE WHEN artifact_deferred_until IS NOT NULL AND rate_wait_started_at IS NOT NULL THEN max(0,CAST((julianday(?)-julianday(rate_wait_started_at))*86400000 AS INTEGER)) ELSE 0 END,rate_wait_started_at=NULL,preparation_started_at=?,artifact_deferred_until=NULL WHERE site_id=? AND feed_id=? AND source_key=?`, stamp(now.Add(rsssource.ConsumerLease)), stamp(now), stamp(now), site, feed, key); err != nil {
			return err
		}
		result = &rsssource.Consumer{Source: source, FeedID: feed, Attempts: attempts + 1, Deadline: at, ClaimGeneration: generation + 1, ResumeURL: resume, Redirects: redirects}
		// Legacy rows have an empty discovered_at: leave the fact unknown.
		if at, err := time.Parse(time.RFC3339Nano, discovered); err == nil {
			result.DiscoveredAt = at
		}
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
		if _, err := tx.ExecContext(ctx, `UPDATE rss_source_consumers SET state=?,last_error_code=?,next_attempt_at=?,matched=?,rate_wait_started_at=NULL,preparation_started_at=NULL,artifact_deferred_until=NULL,artifact_resume_url='',artifact_redirects=0 WHERE site_id=? AND feed_id=? AND source_key=? AND state='PENDING' AND attempts=? AND claim_generation=?`, state, code, stamp(now.Add(rsssource.ConsumerRetry(work.Attempts))), work.Matched && code == "", work.Source.SiteID, work.FeedID, work.Source.SourceKey, work.Attempts, work.ClaimGeneration); err != nil {
			return err
		}
		return RefreshLastMatched(ctx, tx, work.FeedID)
	})
}

func (store *Store) RecordRateWait(ctx context.Context, work rsssource.Consumer, waiting bool, elapsed time.Duration, now time.Time) error {
	var started any
	if waiting {
		started = stamp(now)
	}
	// An asynchronous end may arrive after Finish. Preserve its elapsed time,
	// but never revive a completed attempt's active wait (including retries that
	// are PENDING without an active preparation). The generation still fences
	// every delayed observation from a subsequently claimed attempt.
	_, err := store.database.ExecContext(ctx, `UPDATE rss_source_consumers SET rate_wait_started_at=CASE WHEN state='PENDING' AND preparation_started_at IS NOT NULL THEN ? ELSE NULL END,rate_wait_ms=rate_wait_ms+? WHERE site_id=? AND feed_id=? AND source_key=? AND attempts=? AND claim_generation=?`, started, max(0, elapsed.Milliseconds()), work.Source.SiteID, work.FeedID, work.Source.SourceKey, work.Attempts, work.ClaimGeneration)
	return err
}

func (store *Store) Defer(ctx context.Context, work rsssource.Consumer, retryAt time.Time, resume string, redirects int, now time.Time) error {
	if retryAt.IsZero() || resume == "" || redirects < 0 || redirects >= 10 {
		return errors.New("invalid preparation deferral")
	}
	if !now.Before(work.Deadline) {
		return store.Finish(ctx, work, "PROCESSING_DEADLINE", now)
	}
	// The generation fences both completion and delayed telemetry. Returning the
	// retry budget is safe only for the exact active lease that received no HTTP
	// permission; a duplicate yield cannot decrement attempts a second time.
	_, err := store.database.ExecContext(ctx, `UPDATE rss_source_consumers SET attempts=attempts-1,next_attempt_at=?,artifact_deferred_until=?,artifact_resume_url=?,artifact_redirects=?,preparation_started_at=NULL,rate_wait_started_at=?,last_error_code='' WHERE site_id=? AND feed_id=? AND source_key=? AND state='PENDING' AND attempts=? AND claim_generation=? AND preparation_started_at IS NOT NULL`, stamp(now), stamp(retryAt), resume, redirects, stamp(now), work.Source.SiteID, work.FeedID, work.Source.SourceKey, work.Attempts, work.ClaimGeneration)
	return err
}
