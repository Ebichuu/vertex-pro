// Package rsssourcedb persists rsssource dedupe state in the rss_sources
// table, giving feed source deduplication the same restart-surviving
// semantics the race job store provides for matched items.
package rsssourcedb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/vertex-pro/vertex-pro/internal/rsssource"
)

// Store implements rsssource.Store on top of SQLite.
type Store struct {
	database *sql.DB
}

// New wraps a database. The schema is owned by the migration runner.
func New(database *sql.DB) (*Store, error) {
	if database == nil {
		return nil, errors.New("database is required")
	}
	return &Store{database: database}, nil
}

// Load returns every source recorded for a site.
func (store *Store) Load(ctx context.Context, siteID string) ([]rsssource.Source, error) {
	rows, err := store.database.QueryContext(ctx, `SELECT site_id, source_key, guid, url, title, size_bytes, fingerprint FROM rss_sources WHERE site_id=?`, siteID)
	if err != nil {
		return nil, fmt.Errorf("load rss sources: %w", err)
	}
	defer rows.Close()
	sources := make([]rsssource.Source, 0, 16)
	for rows.Next() {
		var source rsssource.Source
		var size sql.NullInt64
		if err := rows.Scan(&source.SiteID, &source.SourceKey, &source.GUID, &source.URL, &source.Title, &size, &source.Fingerprint); err != nil {
			return nil, err
		}
		if size.Valid {
			value := size.Int64
			source.SizeBytes = &value
		}
		sources = append(sources, source)
	}
	return sources, rows.Err()
}

// Save inserts a new source or updates the mutable fields of an existing one
// while preserving first_seen_at.
func (store *Store) Save(ctx context.Context, source rsssource.Source) error {
	result, err := store.database.ExecContext(ctx, `INSERT INTO rss_sources(site_id, source_key, guid, url, title, size_bytes, fingerprint, first_seen_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, strftime('%Y-%m-%dT%H:%M:%SZ','now'), strftime('%Y-%m-%dT%H:%M:%SZ','now'))
		ON CONFLICT(site_id, source_key) DO UPDATE SET guid=excluded.guid, url=excluded.url, title=excluded.title, size_bytes=excluded.size_bytes, fingerprint=excluded.fingerprint, updated_at=excluded.updated_at`,
		source.SiteID, source.SourceKey, source.GUID, source.URL, source.Title, source.SizeBytes, source.Fingerprint)
	if err != nil {
		return fmt.Errorf("save rss source: %w", err)
	}
	if rows, err := result.RowsAffected(); err == nil && rows == 0 {
		return fmt.Errorf("save rss source: no rows affected")
	}
	return nil
}
