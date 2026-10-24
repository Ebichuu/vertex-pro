package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/quipolicy"
	"github.com/vertex-pro/vertex-pro/internal/ruleaggregate"
)

type policyEntry struct {
	tag string
	at  time.Time
}
type policyCache struct {
	mu      sync.Mutex
	warmMu  sync.Mutex
	service quipolicy.Service
	entries map[string]policyEntry
}

func newPolicyCache(service quipolicy.Service) *policyCache {
	return &policyCache{service: service, entries: map[string]policyEntry{}}
}
func policyKey(m ruleaggregate.Match) string {
	return fmt.Sprintf("%s/%d/%d/%s", m.RuleID, m.RuleRevision, m.SeedTimeSeconds, m.DeleteMode)
}
func (c *policyCache) ready(_ context.Context, m ruleaggregate.Match) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[policyKey(m)]
	age := c.service.Now().Sub(e.at)
	if !ok || age < 0 || age > 30*time.Second {
		return "", errors.New("POLICY_NOT_READY")
	}
	return e.tag, nil
}
func (c *policyCache) warm(ctx context.Context, m ruleaggregate.Match) (string, error) {
	c.warmMu.Lock()
	defer c.warmMu.Unlock()
	if tag, err := c.ready(ctx, m); err == nil {
		return tag, nil
	}
	tag, err := c.service.Ensure(ctx, m)
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, e := range c.entries {
		if c.service.Now().Sub(e.at) > 30*time.Second {
			delete(c.entries, k)
		}
	}
	if err == nil {
		c.entries[policyKey(m)] = policyEntry{tag: tag, at: c.service.Now()}
	} else {
		delete(c.entries, policyKey(m))
	}
	return tag, err
}
