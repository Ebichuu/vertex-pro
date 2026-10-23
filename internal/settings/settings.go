// Package settings persists validated application settings with optimistic locking.
package settings

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	_ "time/tzdata" // IANA zones must also work in the distroless image.
)

var (
	ErrInvalid  = errors.New("invalid settings")
	ErrConflict = errors.New("settings revision changed")
)

// Values contains only editable, non-secret configuration.
type Values struct {
	Timezone                 string `json:"timezone"`
	RSSPollIntervalMinutes   int    `json:"rss_poll_interval_minutes"`
	RateLimitPerMinute       int    `json:"rate_limit_per_minute"`
	MaxConcurrentRSSFetches  int    `json:"max_concurrent_rss_fetches"`
	SessionTTLHours          int    `json:"session_ttl_hours"`
	StatsRawRetentionDays    int    `json:"stats_raw_retention_days"`
	StatsHourlyRetentionDays int    `json:"stats_hourly_retention_days"`
}

type Settings struct {
	Values
	Revision  int    `json:"revision"`
	UpdatedAt string `json:"updated_at"`
}

type Service struct {
	database *sql.DB
	now      func() time.Time
}

func New(database *sql.DB, now func() time.Time) (*Service, error) {
	if database == nil {
		return nil, errors.New("database is required")
	}
	if now == nil {
		now = time.Now
	}
	return &Service{database: database, now: now}, nil
}

const columns = `timezone, rss_poll_interval_minutes, rate_limit_per_minute,
	max_concurrent_rss_fetches, session_ttl_hours, stats_raw_retention_days,
	stats_hourly_retention_days, revision, updated_at`

func scan(row *sql.Row) (Settings, error) {
	var value Settings
	err := row.Scan(&value.Timezone, &value.RSSPollIntervalMinutes, &value.RateLimitPerMinute,
		&value.MaxConcurrentRSSFetches, &value.SessionTTLHours, &value.StatsRawRetentionDays,
		&value.StatsHourlyRetentionDays, &value.Revision, &value.UpdatedAt)
	return value, err
}

func (service *Service) Load(ctx context.Context) (Settings, error) {
	value, err := scan(service.database.QueryRowContext(ctx, `SELECT `+columns+` FROM app_settings WHERE id = 1`))
	if err != nil {
		return Settings{}, fmt.Errorf("load settings: %w", err)
	}
	if err := Validate(value.Values); err != nil {
		return Settings{}, fmt.Errorf("persisted settings: %w", err)
	}
	return value, nil
}

func Validate(value Values) error {
	zone := strings.TrimSpace(value.Timezone)
	if zone == "" || zone == "Local" || len(zone) > 100 {
		return fmt.Errorf("%w: timezone must be an IANA name", ErrInvalid)
	}
	if _, err := time.LoadLocation(zone); err != nil {
		return fmt.Errorf("%w: timezone must be an IANA name", ErrInvalid)
	}
	for _, bound := range []struct {
		name       string
		value, max int
	}{
		{"rss_poll_interval_minutes", value.RSSPollIntervalMinutes, 1440},
		{"rate_limit_per_minute", value.RateLimitPerMinute, 600},
		{"max_concurrent_rss_fetches", value.MaxConcurrentRSSFetches, 32},
		{"session_ttl_hours", value.SessionTTLHours, 720},
		{"stats_raw_retention_days", value.StatsRawRetentionDays, 3650},
		{"stats_hourly_retention_days", value.StatsHourlyRetentionDays, 3650},
	} {
		if bound.value < 1 || bound.value > bound.max {
			return fmt.Errorf("%w: %s must be between 1 and %d", ErrInvalid, bound.name, bound.max)
		}
	}
	if value.StatsHourlyRetentionDays < value.StatsRawRetentionDays {
		return fmt.Errorf("%w: hourly retention cannot be shorter than raw retention", ErrInvalid)
	}
	return nil
}

func (service *Service) Update(ctx context.Context, expectedRevision int, value Values) (Settings, error) {
	if expectedRevision < 1 {
		return Settings{}, fmt.Errorf("%w: revision must be positive", ErrInvalid)
	}
	if err := Validate(value); err != nil {
		return Settings{}, err
	}
	value.Timezone = strings.TrimSpace(value.Timezone)
	// RETURNING binds the response to this update, even if another writer saves next.
	updated, err := scan(service.database.QueryRowContext(ctx, `UPDATE app_settings SET
		timezone = ?, rss_poll_interval_minutes = ?, rate_limit_per_minute = ?,
		max_concurrent_rss_fetches = ?, session_ttl_hours = ?, stats_raw_retention_days = ?,
		stats_hourly_retention_days = ?, revision = revision + 1, updated_at = ?
		WHERE id = 1 AND revision = ? RETURNING `+columns,
		value.Timezone, value.RSSPollIntervalMinutes, value.RateLimitPerMinute,
		value.MaxConcurrentRSSFetches, value.SessionTTLHours, value.StatsRawRetentionDays,
		value.StatsHourlyRetentionDays, service.now().UTC().Format(time.RFC3339Nano), expectedRevision))
	if errors.Is(err, sql.ErrNoRows) {
		return Settings{}, ErrConflict
	}
	if err != nil {
		return Settings{}, fmt.Errorf("update settings: %w", err)
	}
	return updated, nil
}
