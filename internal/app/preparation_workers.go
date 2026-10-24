package app

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/schedulercycle"
)

func feedBudgetKeys(f schedulercycle.FeedRef) []string {
	u, _ := url.Parse(f.URL)
	host := ""
	if u != nil {
		host = strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	}
	return []string{"site:" + f.SiteID, "host:" + host}
}

func (r *Runtime) startDiscoveryWorkers(ctx context.Context, interval time.Duration) {
	for i := 0; i < 4; i++ {
		r.workers.Add(1)
		go func() {
			defer r.workers.Done()
			ticker := time.NewTicker(min(interval, time.Second))
			defer ticker.Stop()
			for {
				if ctx.Err() != nil {
					return
				}
				// Selection and cadence checks share the claim lock; a worker cannot use
				// a feed list fetched before another worker committed its poll timestamp.
				r.preparationMu.Lock()
				feeds, err := r.databaseEnabledFeeds(ctx)
				var selected *schedulercycle.FeedRef
				if err == nil {
					for _, feed := range feeds {
						if feed.RecoverOnly {
							continue
						}
						keys := feedBudgetKeys(feed)
						available := true
						for _, key := range keys {
							available = available && !r.polling[key]
						}
						if available {
							f := feed
							selected = &f
							for _, key := range keys {
								r.polling[key] = true
							}
							break
						}
					}
				}
				r.preparationMu.Unlock()
				if selected != nil {
					result, pollErr := r.scheduler.Discover(ctx, *selected)
					saveErr := r.recordFeedPoll(ctx, *selected, result, pollErr)
					status := "ok"
					if pollErr != nil || saveErr != nil {
						status = "error"
					}
					r.Metrics.Inc("vertex_rss_feeds_total", map[string]string{"result": status})
					for outcome, count := range map[string]int{"new": result.New, "updated": result.Updated, "duplicate": result.Duplicates, "skipped": result.Skipped, "invalid": result.Invalid} {
						if count > 0 {
							r.Metrics.Add("vertex_rss_items_total", map[string]string{"outcome": outcome}, uint64(count))
						}
					}
					r.preparationMu.Lock()
					for _, key := range feedBudgetKeys(*selected) {
						delete(r.polling, key)
					}
					r.preparationMu.Unlock()
					if saveErr == nil {
						for i := 0; i < r.Settings.MaxConcurrentPreparations; i++ {
							select {
							case r.prepareWake <- struct{}{}:
							default:
							}
						}
					}
					if saveErr == nil {
						continue
					}
				}
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
			}
		}()
	}
}

func (r *Runtime) preparationFeeds(ctx context.Context) ([]schedulercycle.FeedRef, error) {
	stamp := r.now().UTC().Format("2006-01-02T15:04:05.000000000Z")
	rows, err := r.Database.QueryContext(ctx, `SELECT f.site_id,f.feed_id,f.feed_url,s.credential_ref,s.base_url FROM feeds f JOIN sites s ON s.site_id=f.site_id JOIN feed_runtime_state r ON r.feed_id=f.feed_id WHERE f.enabled=1 AND f.archived=0 AND s.enabled=1 AND s.archived=0 AND r.initialized=1 AND EXISTS(SELECT 1 FROM rss_source_consumers c WHERE c.site_id=f.site_id AND c.feed_id=f.feed_id AND c.state='PENDING' AND (c.next_attempt_at<=? OR c.deadline_at<=?)) ORDER BY f.feed_id`, stamp, stamp)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var feeds []schedulercycle.FeedRef
	for rows.Next() {
		var f schedulercycle.FeedRef
		if err := rows.Scan(&f.SiteID, &f.FeedID, &f.URL, &f.CredentialRef, &f.CredentialOrigin); err != nil {
			return nil, err
		}
		feeds = append(feeds, f)
	}
	return feeds, rows.Err()
}

func (r *Runtime) startPreparationWorkers(ctx context.Context) {
	for i := 0; i < r.Settings.MaxConcurrentPreparations; i++ {
		r.workers.Add(1)
		go func() {
			defer r.workers.Done()
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			for {
				if ctx.Err() != nil {
					return
				}
				r.preparationMu.Lock()
				feeds, err := r.preparationFeeds(ctx)
				var selected *schedulercycle.FeedRef
				if err == nil {
					for offset := 0; offset < len(feeds); offset++ {
						index := (r.prepareCursor + offset) % len(feeds)
						feed := feeds[index]
						available := true
						for _, key := range feedBudgetKeys(feed) {
							available = available && r.preparing[key] < r.Settings.MaxPreparationsPerHost
						}
						if available {
							f := feed
							selected = &f
							r.prepareCursor = (index + 1) % len(feeds)
							for _, key := range feedBudgetKeys(feed) {
								r.preparing[key]++
							}
							break
						}
					}
				}
				r.preparationMu.Unlock()
				if selected != nil {
					outcome, worked, err := r.scheduler.ConsumeOne(ctx, *selected)
					r.preparationMu.Lock()
					for _, key := range feedBudgetKeys(*selected) {
						r.preparing[key]--
					}
					r.preparationMu.Unlock()
					if worked {
						status := "skipped"
						if err != nil {
							status = "failed"
						} else if outcome.Matched {
							status = "matched"
							r.wakeDispatch()
						}
						r.Metrics.Inc("vertex_rss_items_total", map[string]string{"outcome": status})
						continue
					}
				}
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				case <-r.prepareWake:
				}
			}
		}()
	}
}
