// Package schedulercycle composes one production RSS poll cycle: fetch the
// feed, persist feed-specific consumers, recover pending artifact processing,
// and hand each item to the match pipeline. Dispatch itself stays in
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
	RecoverOnly      bool // Consume due durable work without polling the RSS feed.
	ETag             string
	LastModified     string
}

// Cycle carries the collaborators for repeated poll cycles.
type Cycle struct {
	fetcher     rssfetch.Fetcher
	downloader  *torrentdownload.Downloader
	consumers   rsssource.ConsumerStore
	pipeline    matchpipeline.Deps
	maxFeedSize int64
	now         func() time.Time
	artifacts   artifactCache
	progress    *rateProgressReporter
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
	consumers, err := dedupe.Consumers()
	if err != nil {
		return nil, err
	}
	now := pipeline.Now
	if now == nil {
		now = time.Now
	}
	cycle := &Cycle{fetcher: fetcher, downloader: downloader, consumers: consumers, pipeline: pipeline, maxFeedSize: maxFeedSize, now: now}
	if observer, ok := consumers.(rsssource.RateWaitStore); ok {
		cycle.progress = newRateProgressReporter(observer)
	}
	return cycle, nil
}

// Close cancels progress persistence and waits for its worker. Stop the callers
// of Run and ConsumeOne first, and close their database only after this returns.
// It is safe to call Close repeatedly or before any progress was reported.
func (cycle *Cycle) Close() {
	if cycle != nil && cycle.progress != nil {
		cycle.progress.close()
	}
}

// Result reports one cycle over one feed.
type Result struct {
	PolledAt     time.Time
	FeedURL      string
	Entries      int
	New          int
	Updated      int
	Duplicates   int
	Matched      int
	Skipped      int
	Failed       int
	Invalid      int
	Failures     []string
	Outcomes     []matchpipeline.Outcome
	ETag         string
	LastModified string
	NotModified  bool
}

// Run polls one feed end to end. A per-item failure does not abort the cycle;
// it is recorded and the cycle continues with the remaining entries.
func (cycle *Cycle) Run(ctx context.Context, feed FeedRef) (Result, error) {
	if feed.SiteID == "" || feed.FeedID == "" || feed.URL == "" {
		return Result{}, fmt.Errorf("site id, feed id and feed url are required")
	}
	result := Result{}
	var fetchErr error
	if !feed.RecoverOnly {
		result, fetchErr = cycle.Discover(ctx, feed)
	}
	if feed.BaselineOnly {
		return result, fetchErr
	}
	// Pending work does not depend on receiving a changed feed body. In
	// particular 304s and temporary RSS errors must not suppress recovery.
	for processed := 0; processed < 100; processed++ {
		work, err := cycle.consumers.Claim(ctx, feed.SiteID, feed.FeedID, cycle.now())
		if err != nil {
			return result, err
		}
		if work == nil {
			break
		}
		attemptCtx, cancel := cycle.attemptContext(ctx, *work)
		outcome, err := cycle.process(attemptCtx, feed, *work)
		cancel()
		code := ""
		if err != nil {
			code = "RSS_ITEM_PROCESSING_FAILED"
			result.Failed++
			result.Failures = append(result.Failures, code)
		} else {
			if outcome.Matched {
				result.Matched++
			} else {
				result.Skipped++
			}
			result.Outcomes = append(result.Outcomes, outcome)
		}
		work.Matched = outcome.Matched
		if err := cycle.consumers.Finish(ctx, *work, code, cycle.now()); err != nil {
			return result, err
		}
	}
	return result, fetchErr
}

func (cycle *Cycle) discover(ctx context.Context, feed FeedRef) (Result, error) {
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
		entries = append(entries, rsssource.Entry{GUID: entry.ID, URL: link, Title: entry.Title, SizeBytes: entry.SizeBytes, PublishedAt: entry.Published})
	}
	dedupeResult, err := cycle.consumers.Discover(ctx, feed.SiteID, feed.FeedID, entries, feed.BaselineOnly, cycle.now())
	if err != nil {
		return Result{}, fmt.Errorf("dedupe sources: %w", err)
	}
	result := Result{
		FeedURL:    feed.URL,
		Entries:    len(entries),
		New:        len(dedupeResult.New),
		Updated:    len(dedupeResult.Updated),
		Duplicates: dedupeResult.Duplicates,
		Failed:     len(dedupeResult.Invalid), Invalid: len(dedupeResult.Invalid),
		ETag: response.ETag, LastModified: response.LastModified,
	}
	for range dedupeResult.Invalid {
		result.Failures = append(result.Failures, "RSS_ENTRY_INVALID")
	}
	if feed.BaselineOnly {
		result.Skipped = len(entries) - result.Invalid
	}
	return result, nil
}

// Discover persists sources only. Preparation runs on independent bounded workers.
func (cycle *Cycle) Discover(ctx context.Context, feed FeedRef) (Result, error) {
	at := cycle.now()
	result, err := cycle.discover(ctx, feed)
	result.PolledAt = at
	return result, err
}

func (cycle *Cycle) ConsumeOne(ctx context.Context, feed FeedRef) (matchpipeline.Outcome, bool, error) {
	work, err := cycle.consumers.Claim(ctx, feed.SiteID, feed.FeedID, cycle.now())
	if err != nil || work == nil {
		return matchpipeline.Outcome{}, false, err
	}
	attemptCtx, cancel := cycle.attemptContext(ctx, *work)
	outcome, processErr := cycle.process(attemptCtx, feed, *work)
	cancel()
	code := ""
	if processErr != nil {
		code = "RSS_ITEM_PROCESSING_FAILED"
	}
	work.Matched = outcome.Matched
	if err := cycle.consumers.Finish(ctx, *work, code, cycle.now()); err != nil {
		return outcome, true, err
	}
	return outcome, true, processErr
}

// Configure the clock before starting workers; durable claims and the runtime's
// due-work query must use exactly the same time base.
func (cycle *Cycle) SetClock(now func() time.Time) { cycle.now = now; cycle.pipeline.Now = now }

func (cycle *Cycle) attemptContext(ctx context.Context, work rsssource.Consumer) (context.Context, context.CancelFunc) {
	// Bound total work by its durable lease; the transport separately starts
	// the network deadline after a rate permit has been granted.
	attemptCtx, cancel := context.WithTimeout(ctx, min(rsssource.ConsumerLease-5*time.Second, work.Deadline.Sub(cycle.now())))
	if cycle.progress != nil {
		progress := cycle.progress.attempt(work)
		attemptCtx = rssfetch.WithWaitObserver(attemptCtx, func(waiting bool, elapsed time.Duration) {
			progress.report(waiting, elapsed, cycle.now())
		})
		return attemptCtx, func() {
			cancel()
			progress.finish(cycle.now())
		}
	}
	return attemptCtx, cancel
}

func (cycle *Cycle) process(ctx context.Context, feed FeedRef, work rsssource.Consumer) (matchpipeline.Outcome, error) {
	preview := matchpipeline.Item{Source: work.Source, FeedID: feed.FeedID, PublishedAt: parsePublished(work.Source.PublishedAt)}
	if outcome, rejected, err := matchpipeline.Prefilter(ctx, cycle.pipeline, preview); err != nil || rejected {
		return outcome, err
	}
	item, err := cycle.identify(ctx, work.Source, feed)
	if err != nil {
		return matchpipeline.Outcome{}, err
	}
	if !cycle.now().Before(work.Deadline) {
		return matchpipeline.Outcome{}, fmt.Errorf("source processing deadline reached")
	}
	item.FeedID = feed.FeedID
	item.PublishedAt = parsePublished(work.Source.PublishedAt)
	return matchpipeline.Process(ctx, cycle.pipeline, item)
}

func (cycle *Cycle) identify(ctx context.Context, source rsssource.Source, feed FeedRef) (matchpipeline.Item, error) {
	item, err := cycle.artifacts.get(ctx, source, feed, func() (matchpipeline.Item, error) { return cycle.downloadIdentity(ctx, source, feed) })
	if err != nil {
		return matchpipeline.Item{}, err
	}
	// Shared bytes/identity must not replace this consumer's title or publication.
	size := *item.Source.SizeBytes
	item.Source = source
	item.Source.SizeBytes = &size
	return item, nil
}

func (cycle *Cycle) downloadIdentity(ctx context.Context, source rsssource.Source, feed FeedRef) (matchpipeline.Item, error) {
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
	return matchpipeline.Item{Source: source, IdentityKey: identityKey, TorrentBody: artifact.Body}, nil
}

func parsePublished(raw string) time.Time {
	for _, layout := range []string{time.RFC3339, time.RFC1123Z, time.RFC1123, time.RFC822Z} {
		if value, err := time.Parse(layout, raw); err == nil {
			return value
		}
	}
	return time.Time{}
}
