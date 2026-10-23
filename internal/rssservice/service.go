package rssservice

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

type Service struct {
	database *sql.DB
	now      func() time.Time
}

var (
	ErrFeedNotFound         = errors.New("feed not found")
	ErrFeedRevisionConflict = errors.New("feed revision conflict")
)

type Site struct {
	ID        string
	Name      string
	BaseURL   string
	Enabled   bool
	CreatedAt string
	UpdatedAt string
}
type Feed struct {
	ID           string
	SiteID       string
	Name         string
	FeedURL      string
	Enabled      bool
	PollMinutes  int
	ETag         sql.NullString
	LastModified sql.NullString
	CreatedAt    string
	UpdatedAt    string
}
type CreateSiteInput struct {
	ID      string
	Name    string
	BaseURL string
	Enabled bool
}
type CreateFeedInput struct {
	ID          string
	SiteID      string
	Name        string
	FeedURL     string
	Enabled     bool
	PollMinutes int
}

func New(database *sql.DB) (Service, error) {
	return NewWithClock(database, time.Now)
}

func NewWithClock(database *sql.DB, now func() time.Time) (Service, error) {
	if database == nil {
		return Service{}, errors.New("database is required")
	}
	if now == nil {
		now = time.Now
	}
	return Service{database: database, now: now}, nil
}
func (service Service) CreateSite(ctx context.Context, input CreateSiteInput) (Site, error) {
	if err := validateID(input.ID, "site ID"); err != nil {
		return Site{}, err
	}
	if err := validateName(input.Name, "site name"); err != nil {
		return Site{}, err
	}
	base, err := validateURL(input.BaseURL, "base URL")
	if err != nil {
		return Site{}, err
	}
	now := service.now().UTC().Format(time.RFC3339Nano)
	_, err = service.database.ExecContext(ctx, `INSERT INTO sites(site_id,name,base_url,enabled,created_at,updated_at) VALUES(?,?,?,?,?,?)`, input.ID, input.Name, base, input.Enabled, now, now)
	if err != nil {
		return Site{}, fmt.Errorf("create site: %w", err)
	}
	return Site{ID: input.ID, Name: input.Name, BaseURL: base, Enabled: input.Enabled, CreatedAt: now, UpdatedAt: now}, nil
}
func (service Service) ListSites(ctx context.Context) ([]Site, error) {
	rows, err := service.database.QueryContext(ctx, `SELECT site_id,name,base_url,enabled,created_at,updated_at FROM sites ORDER BY site_id`)
	if err != nil {
		return nil, fmt.Errorf("list sites: %w", err)
	}
	defer rows.Close()
	var result []Site
	for rows.Next() {
		var v Site
		var enabled int
		if err := rows.Scan(&v.ID, &v.Name, &v.BaseURL, &enabled, &v.CreatedAt, &v.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan site: %w", err)
		}
		v.Enabled = enabled == 1
		result = append(result, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sites: %w", err)
	}
	return result, nil
}
func (service Service) CreateFeed(ctx context.Context, input CreateFeedInput) (Feed, error) {
	if err := validateID(input.ID, "feed ID"); err != nil {
		return Feed{}, err
	}
	if err := validateID(input.SiteID, "site ID"); err != nil {
		return Feed{}, err
	}
	if err := validateName(input.Name, "feed name"); err != nil {
		return Feed{}, err
	}
	feedURL, err := validateFeedURL(input.FeedURL)
	if err != nil {
		return Feed{}, err
	}
	if input.PollMinutes == 0 {
		input.PollMinutes = 15
	}
	if input.PollMinutes < 1 || input.PollMinutes > 1440 {
		return Feed{}, errors.New("invalid poll minutes")
	}
	now := service.now().UTC().Format(time.RFC3339Nano)
	_, err = service.database.ExecContext(ctx, `INSERT INTO feeds(feed_id,site_id,name,feed_url,enabled,poll_minutes,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)`, input.ID, input.SiteID, input.Name, feedURL, input.Enabled, input.PollMinutes, now, now)
	if err != nil {
		return Feed{}, fmt.Errorf("create feed: %w", err)
	}
	return Feed{ID: input.ID, SiteID: input.SiteID, Name: input.Name, FeedURL: feedURL, Enabled: input.Enabled, PollMinutes: input.PollMinutes, CreatedAt: now, UpdatedAt: now}, nil
}

type UpdateFeedInput struct {
	ID                string
	ExpectedUpdatedAt string
	Name              *string
	FeedURL           *string
	Enabled           *bool
	PollMinutes       *int
}

func (service Service) GetFeed(ctx context.Context, id string) (Feed, error) {
	if err := validateID(id, "feed ID"); err != nil {
		return Feed{}, err
	}
	var feed Feed
	var enabled int
	err := service.database.QueryRowContext(ctx, `SELECT feed_id,site_id,name,feed_url,enabled,poll_minutes,etag,last_modified,created_at,updated_at FROM feeds WHERE feed_id = ?`, id).Scan(
		&feed.ID, &feed.SiteID, &feed.Name, &feed.FeedURL, &enabled, &feed.PollMinutes, &feed.ETag, &feed.LastModified, &feed.CreatedAt, &feed.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Feed{}, ErrFeedNotFound
	}
	if err != nil {
		return Feed{}, fmt.Errorf("get feed: %w", err)
	}
	feed.Enabled = enabled == 1
	return feed, nil
}

func (service Service) UpdateFeed(ctx context.Context, input UpdateFeedInput) (Feed, error) {
	if err := validateID(input.ID, "feed ID"); err != nil {
		return Feed{}, err
	}
	if strings.TrimSpace(input.ExpectedUpdatedAt) == "" {
		return Feed{}, errors.New("feed revision is required")
	}
	feed, err := service.GetFeed(ctx, input.ID)
	if err != nil {
		return Feed{}, err
	}
	changed := false
	if input.Name != nil {
		if err := validateName(*input.Name, "feed name"); err != nil {
			return Feed{}, err
		}
		feed.Name = strings.TrimSpace(*input.Name)
		changed = true
	}
	if input.FeedURL != nil {
		feedURL, err := validateFeedURL(*input.FeedURL)
		if err != nil {
			return Feed{}, err
		}
		feed.FeedURL = feedURL
		changed = true
	}
	if input.Enabled != nil {
		feed.Enabled = *input.Enabled
		changed = true
	}
	if input.PollMinutes != nil {
		if *input.PollMinutes < 1 || *input.PollMinutes > 1440 {
			return Feed{}, errors.New("invalid poll minutes")
		}
		feed.PollMinutes = *input.PollMinutes
		changed = true
	}
	if !changed {
		return Feed{}, errors.New("no feed fields to update")
	}
	now := service.now().UTC().Format(time.RFC3339Nano)
	result, err := service.database.ExecContext(ctx, `UPDATE feeds SET name=?,feed_url=?,enabled=?,poll_minutes=?,updated_at=? WHERE feed_id=? AND updated_at=?`, feed.Name, feed.FeedURL, feed.Enabled, feed.PollMinutes, now, feed.ID, input.ExpectedUpdatedAt)
	if err != nil {
		return Feed{}, fmt.Errorf("update feed: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return Feed{}, fmt.Errorf("check feed update: %w", err)
	}
	if rows != 1 {
		return Feed{}, ErrFeedRevisionConflict
	}
	return service.GetFeed(ctx, feed.ID)
}

func (service Service) ListFeeds(ctx context.Context, siteID string) ([]Feed, error) {
	if siteID != "" {
		if err := validateID(siteID, "site ID"); err != nil {
			return nil, err
		}
	}
	query := `SELECT feed_id,site_id,name,feed_url,enabled,poll_minutes,etag,last_modified,created_at,updated_at FROM feeds`
	args := []any{}
	if siteID != "" {
		query += ` WHERE site_id = ?`
		args = append(args, siteID)
	}
	query += ` ORDER BY feed_id`
	rows, err := service.database.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list feeds: %w", err)
	}
	defer rows.Close()
	var result []Feed
	for rows.Next() {
		var v Feed
		var enabled int
		if err := rows.Scan(&v.ID, &v.SiteID, &v.Name, &v.FeedURL, &enabled, &v.PollMinutes, &v.ETag, &v.LastModified, &v.CreatedAt, &v.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan feed: %w", err)
		}
		v.Enabled = enabled == 1
		result = append(result, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate feeds: %w", err)
	}
	return result, nil
}
func validateID(value, label string) error {
	if !idPattern.MatchString(value) {
		return fmt.Errorf("invalid %s", label)
	}
	return nil
}
func validateName(value, label string) error {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 200 {
		return fmt.Errorf("invalid %s", label)
	}
	return nil
}
func validateURL(value, label string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("invalid %s", label)
	}
	return parsed.String(), nil
}

// Authenticated RSS subscriptions commonly put their passkey and filters in
// the query. Keep those opaque; base site URLs retain their stricter policy.
func validateFeedURL(value string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.Fragment != "" {
		return "", errors.New("invalid feed URL")
	}
	if _, err := url.ParseQuery(parsed.RawQuery); err != nil {
		return "", errors.New("invalid feed URL")
	}
	return parsed.String(), nil
}
