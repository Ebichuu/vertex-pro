// Package schedulercycle composes one production RSS poll cycle: fetch the
// feed, deduplicate its sources, download and identify the artifacts of new
// entries, and hand each item to the match pipeline. Dispatch itself stays in
// dispatchorchestrator; this package only produces matched, identified items
// and reports what happened.
package schedulercycle

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/matchpipeline"
	"github.com/vertex-pro/vertex-pro/internal/rssfetch"
	"github.com/vertex-pro/vertex-pro/internal/rsspreview"
	"github.com/vertex-pro/vertex-pro/internal/rsssource"
	"github.com/vertex-pro/vertex-pro/internal/torrentdownload"
	"github.com/vertex-pro/vertex-pro/internal/torrentidentity"
	"github.com/vertex-pro/vertex-pro/internal/torrentparse"
)

// FeedRef is one feed to poll.
type FeedRef struct {
	CredentialRef    string
	CredentialOrigin string
	SiteID           string
	FeedID           string
	URL              string
	BaselineOnly     bool
	ETag             string
	LastModified     string
}

// Cycle carries the collaborators for repeated poll cycles.
type Cycle struct {
	fetcher     rssfetch.Fetcher
	downloader  *torrentdownload.Downloader
	dedupe      *rsssource.Dedupe
	pipeline    matchpipeline.Deps
	maxFeedSize int64
}

// New assembles a cycle. feedLimit bounds feed bodies; download limits are
// owned by the downloader.
func New(fetcher rssfetch.Fetcher, downloader *torrentdownload.Downloader, dedupe *rsssource.Dedupe, pipeline matchpipeline.Deps, maxFeedSize int64) (*Cycle, error) {
	if maxFeedSize <= 0 {
		maxFeedSize = 1 << 20
	}
	if dedupe == nil {
		return nil, fmt.Errorf("source dedupe is required")
	}
	return &Cycle{fetcher: fetcher, downloader: downloader, dedupe: dedupe, pipeline: pipeline, maxFeedSize: maxFeedSize}, nil
}

// Result reports one cycle over one feed.
type Result struct {
	FeedURL      string
	Entries      int
	New          int
	Updated      int
	Duplicates   int
	Matched      int
	Skipped      int
	Failed       int
	Failures     []string
	Outcomes     []matchpipeline.Outcome
	ETag         string
	LastModified string
	NotModified  bool
}

// Run polls one feed end to end. A per-item failure does not abort the cycle;
// it is recorded and the cycle continues with the remaining entries.
func (cycle *Cycle) Run(ctx context.Context, feed FeedRef) (Result, error) {
	if feed.SiteID == "" || feed.URL == "" {
		return Result{}, fmt.Errorf("site id and feed url are required")
	}
	response, err := cycle.fetcher.Fetch(ctx, rssfetch.Request{CredentialRef: feed.CredentialRef, CredentialOrigin: feed.CredentialOrigin, URL: feed.URL, ETag: feed.ETag, LastModified: feed.LastModified})
	if err != nil {
		return Result{}, fmt.Errorf("fetch feed: %w", err)
	}
	if response.StatusCode == http.StatusNotModified {
		return Result{NotModified: true, ETag: feed.ETag, LastModified: feed.LastModified}, nil
	}
	if int64(len(response.Body)) > cycle.maxFeedSize {
		return Result{}, fmt.Errorf("feed body exceeds %d bytes", cycle.maxFeedSize)
	}
	preview, err := rsspreview.Parse(feed.URL, response.Body, 100)
	if err != nil {
		return Result{}, fmt.Errorf("parse feed: %w", err)
	}
	entries := make([]rsssource.Entry, 0, len(preview.Entries))
	for _, entry := range preview.Entries {
		link := entry.DownloadURL
		if link == "" {
			link = entry.Link
		}
		entries = append(entries, rsssource.Entry{GUID: entry.ID, URL: link, Title: entry.Title, SizeBytes: entry.SizeBytes})
	}
	dedupeResult, err := cycle.dedupe.Apply(ctx, feed.SiteID, entries)
	if err != nil {
		return Result{}, fmt.Errorf("dedupe sources: %w", err)
	}
	result := Result{
		FeedURL:    feed.URL,
		Entries:    len(entries),
		New:        len(dedupeResult.New),
		Updated:    len(dedupeResult.Updated),
		Duplicates: dedupeResult.Duplicates,
		ETag:       response.ETag, LastModified: response.LastModified,
	}
	if feed.BaselineOnly {
		result.Skipped = len(dedupeResult.New)
		return result, nil
	}
	for _, source := range dedupeResult.New {
		item, err := cycle.identify(ctx, source, feed)
		if err != nil {
			result.Failed++
			result.Failures = append(result.Failures, fmt.Sprintf("%s: %v", source.SourceKey, err))
			continue
		}
		item.FeedID = feed.FeedID
		for _, entry := range preview.Entries {
			if entry.ID == source.GUID {
				item.PublishedAt = parsePublished(entry.Published)
				break
			}
		}
		outcome, err := matchpipeline.Process(ctx, cycle.pipeline, item)
		if err != nil {
			result.Failed++
			result.Failures = append(result.Failures, fmt.Sprintf("%s: %v", source.SourceKey, err))
			continue
		}
		if outcome.Matched {
			result.Matched++
		} else {
			result.Skipped++
		}
		result.Outcomes = append(result.Outcomes, outcome)
	}
	return result, nil
}

func (cycle *Cycle) identify(ctx context.Context, source rsssource.Source, feed FeedRef) (matchpipeline.Item, error) {
	artifact, err := cycle.downloader.Download(ctx, torrentdownload.Request{URL: source.URL, CredentialRef: feed.CredentialRef, CredentialOrigin: feed.CredentialOrigin})
	if err != nil {
		return matchpipeline.Item{}, fmt.Errorf("download artifact: %w", err)
	}
	parsed, err := torrentparse.Parse(artifact.Body, torrentparse.DefaultLimits())
	if err != nil {
		return matchpipeline.Item{}, fmt.Errorf("parse artifact: %w", err)
	}
	identity, err := torrentidentity.FromTorrent(parsed)
	if err != nil {
		return matchpipeline.Item{}, fmt.Errorf("compute identity: %w", err)
	}
	identityKey := identity.V1
	if identityKey == "" {
		return matchpipeline.Item{}, fmt.Errorf("artifact lacks v1 identity")
	}
	size := parsed.TotalSize
	source.SizeBytes = &size
	return matchpipeline.Item{Source: source, IdentityKey: identityKey}, nil
}

func parsePublished(raw string) time.Time {
	for _, layout := range []string{time.RFC3339, time.RFC1123Z, time.RFC1123, time.RFC822Z} {
		if value, err := time.Parse(layout, raw); err == nil {
			return value
		}
	}
	return time.Time{}
}
