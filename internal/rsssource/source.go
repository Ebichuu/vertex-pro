package rsssource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
)

type Entry struct {
	PublishedAt string
	GUID        string
	URL         string
	Title       string
	SizeBytes   *int64
}

type Source struct {
	PublishedAt string
	SiteID      string
	SourceKey   string
	GUID        string
	URL         string
	Title       string
	SizeBytes   *int64
	Fingerprint string
}

type Result struct {
	SiteID     string
	New        []Source
	Updated    []Source
	Duplicates int
}

type Store interface {
	Load(context.Context, string) ([]Source, error)
	Save(context.Context, Source) error
}

type Dedupe struct {
	store Store
	mu    sync.Mutex
}

func New(store Store) (*Dedupe, error) {
	if store == nil {
		return nil, errors.New("source store is required")
	}
	return &Dedupe{store: store}, nil
}

func (dedupe *Dedupe) Apply(ctx context.Context, siteID string, entries []Entry) (Result, error) {
	if dedupe == nil || dedupe.store == nil {
		return Result{}, errors.New("source dedupe is not initialized")
	}
	siteID = strings.TrimSpace(siteID)
	if siteID == "" {
		return Result{}, errors.New("site ID is required")
	}
	dedupe.mu.Lock()
	defer dedupe.mu.Unlock()

	existing, err := dedupe.store.Load(ctx, siteID)
	if err != nil {
		return Result{}, fmt.Errorf("load sources: %w", err)
	}
	byKey := make(map[string]Source, len(existing))
	for _, source := range existing {
		if source.SiteID != "" && source.SiteID != siteID {
			return Result{}, errors.New("source site ID mismatch")
		}
		source.SiteID = siteID
		if source.SourceKey == "" {
			return Result{}, errors.New("persisted source key is required")
		}
		byKey[source.SourceKey] = source
	}
	result := Result{SiteID: siteID}
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		source, err := normalize(siteID, entry)
		if err != nil {
			return Result{}, err
		}
		if _, exists := seen[source.SourceKey]; exists {
			result.Duplicates++
			continue
		}
		seen[source.SourceKey] = struct{}{}
		previous, exists := byKey[source.SourceKey]
		if !exists {
			if err := dedupe.store.Save(ctx, source); err != nil {
				return Result{}, fmt.Errorf("save source: %w", err)
			}
			byKey[source.SourceKey] = source
			result.New = append(result.New, source)
			continue
		}
		if sameFields(previous, source) {
			result.Duplicates++
			continue
		}
		if err := dedupe.store.Save(ctx, source); err != nil {
			return Result{}, fmt.Errorf("update source: %w", err)
		}
		byKey[source.SourceKey] = source
		result.Updated = append(result.Updated, source)
	}
	sort.Slice(result.New, func(i, j int) bool { return result.New[i].SourceKey < result.New[j].SourceKey })
	sort.Slice(result.Updated, func(i, j int) bool { return result.Updated[i].SourceKey < result.Updated[j].SourceKey })
	return result, nil
}

func normalize(siteID string, entry Entry) (Source, error) {
	guid := strings.TrimSpace(entry.GUID)
	feedURL, err := normalizeURL(entry.URL)
	if err != nil {
		return Source{}, err
	}
	title := strings.Join(strings.Fields(entry.Title), " ")
	if guid == "" && feedURL == "" && title == "" {
		return Source{}, errors.New("source requires guid, URL, or title")
	}
	key := sourceKey(guid, feedURL, title, entry.SizeBytes)
	return Source{PublishedAt: entry.PublishedAt, SiteID: siteID, SourceKey: key, GUID: guid, URL: feedURL, Title: title, SizeBytes: cloneSize(entry.SizeBytes), Fingerprint: fingerprint(guid, feedURL, title, entry.SizeBytes)}, nil
}

func sourceKey(guid, feedURL, title string, size *int64) string {
	if guid != "" {
		return "guid:" + guid
	}
	if feedURL != "" {
		return "url:" + feedURL
	}
	return "meta:" + fingerprint("", "", title, size)
}

func fingerprint(guid, feedURL, title string, size *int64) string {
	sizeValue := ""
	if size != nil {
		sizeValue = strconv.FormatInt(*size, 10)
	}
	payload := strings.Join([]string{guid, feedURL, title, sizeValue}, "\x00")
	digest := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(digest[:])
}

func normalizeURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		return "", errors.New("invalid source URL")
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	return parsed.String(), nil
}

func sameFields(left, right Source) bool {
	return left.GUID == right.GUID && left.URL == right.URL && left.Title == right.Title && equalSize(left.SizeBytes, right.SizeBytes) && left.Fingerprint == right.Fingerprint
}

func equalSize(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func cloneSize(value *int64) *int64 {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

type MemoryStore struct {
	mu      sync.Mutex
	sources map[string]map[string]Source
}

func (store *MemoryStore) Load(_ context.Context, siteID string) ([]Source, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	result := make([]Source, 0)
	for _, source := range store.sources[siteID] {
		result = append(result, cloneSource(source))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].SourceKey < result[j].SourceKey })
	return result, nil
}

func (store *MemoryStore) Save(_ context.Context, source Source) error {
	if strings.TrimSpace(source.SiteID) == "" || strings.TrimSpace(source.SourceKey) == "" {
		return errors.New("source identity is required")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.sources == nil {
		store.sources = make(map[string]map[string]Source)
	}
	if store.sources[source.SiteID] == nil {
		store.sources[source.SiteID] = make(map[string]Source)
	}
	store.sources[source.SiteID][source.SourceKey] = cloneSource(source)
	return nil
}

func cloneSource(source Source) Source {
	source.SizeBytes = cloneSize(source.SizeBytes)
	return source
}
