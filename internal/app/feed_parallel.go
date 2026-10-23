package app

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"sync"

	"github.com/vertex-pro/vertex-pro/internal/schedulercycle"
)

// Related feeds share a serial lane, including separate site records pointing
// at the same host and one site with multiple hosts. The existing transport
// limiter and per-feed cadence remain authoritative; this only overlaps work
// for independent upstreams within one scheduler cycle.
func runFeedGroups(ctx context.Context, feeds []schedulercycle.FeedRef, limit int, run func(context.Context, schedulercycle.FeedRef) error) error {
	if len(feeds) == 0 {
		return ctx.Err()
	}
	if limit < 1 {
		limit = 1
	}
	parent := make([]int, len(feeds))
	hosts := make([]string, len(feeds))
	for i, feed := range feeds {
		parent[i] = i
		if parsed, err := url.Parse(feed.URL); err == nil {
			hosts[i] = strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
		}
	}
	var root func(int) int
	root = func(i int) int {
		if parent[i] != i {
			parent[i] = root(parent[i])
		}
		return parent[i]
	}
	for i := range feeds {
		for j := 0; j < i; j++ {
			if feeds[i].SiteID == feeds[j].SiteID || hosts[i] == hosts[j] {
				parent[root(i)] = root(j)
			}
		}
	}
	groups := [][]int{}
	indexes := map[int]int{}
	for i := range feeds {
		key := root(i)
		index, exists := indexes[key]
		if !exists {
			index = len(groups)
			indexes[key] = index
			groups = append(groups, nil)
		}
		groups[index] = append(groups[index], i)
	}
	queue := make(chan []int, len(groups))
	for _, group := range groups {
		queue <- group
	}
	close(queue)
	errs := make([]error, len(feeds))
	var workers sync.WaitGroup
	for worker := 0; worker < min(limit, len(groups)); worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for group := range queue {
				for _, i := range group {
					if err := ctx.Err(); err != nil {
						errs[i] = err
						continue
					}
					errs[i] = run(ctx, feeds[i])
				}
			}
		}()
	}
	workers.Wait()
	return errors.Join(errs...)
}
