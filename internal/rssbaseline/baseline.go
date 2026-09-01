package rssbaseline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
)

type Entry struct {
	GUID      string
	URL       string
	Title     string
	SizeBytes *int64
}

type Item struct {
	Key       string
	GUID      string
	URL       string
	Title     string
	SizeBytes *int64
}

type FeedState struct {
	FeedID      string
	Initialized bool
	Items       []Item
}

type Store interface {
	Load(context.Context, string) (FeedState, error)
	Save(context.Context, FeedState) error
}

type Result struct {
	FeedID         string
	InitializedNow bool
	BaselineCount  int
	NewItems       []Item
	DuplicateCount int
}

type Baseline struct {
	store Store
	mu    sync.Mutex
}

func New(store Store) (*Baseline, error) {
	if store == nil {
		return nil, errors.New("baseline store is required")
	}
	return &Baseline{store: store}, nil
}

func (baseline *Baseline) Apply(ctx context.Context, feedID string, entries []Entry) (Result, error) {
	feedID = strings.TrimSpace(feedID)
	if feedID == "" {
		return Result{}, errors.New("feed ID is required")
	}
	if baseline == nil || baseline.store == nil {
		return Result{}, errors.New("baseline is not initialized")
	}
	baseline.mu.Lock()
	defer baseline.mu.Unlock()

	state, err := baseline.store.Load(ctx, feedID)
	if err != nil {
		return Result{}, fmt.Errorf("load baseline: %w", err)
	}
	if state.FeedID != "" && state.FeedID != feedID {
		return Result{}, errors.New("baseline feed ID mismatch")
	}
	state.FeedID = feedID
	current, duplicates := canonicalize(entries)
	if !state.Initialized {
		state.Initialized = true
		state.Items = current
		if err := baseline.store.Save(ctx, state); err != nil {
			return Result{}, fmt.Errorf("save baseline: %w", err)
		}
		return Result{FeedID: feedID, InitializedNow: true, BaselineCount: len(current), DuplicateCount: duplicates}, nil
	}

	seen := make(map[string]struct{}, len(state.Items))
	for _, item := range state.Items {
		seen[item.Key] = struct{}{}
	}
	newItems := make([]Item, 0)
	for _, item := range current {
		if _, exists := seen[item.Key]; exists {
			continue
		}
		seen[item.Key] = struct{}{}
		newItems = append(newItems, item)
	}
	if len(newItems) > 0 {
		state.Items = append(state.Items, newItems...)
		sort.Slice(state.Items, func(i, j int) bool { return state.Items[i].Key < state.Items[j].Key })
	}
	if err := baseline.store.Save(ctx, state); err != nil {
		return Result{}, fmt.Errorf("save baseline: %w", err)
	}
	return Result{FeedID: feedID, BaselineCount: len(state.Items), NewItems: newItems, DuplicateCount: duplicates}, nil
}

func canonicalize(entries []Entry) ([]Item, int) {
	items := make([]Item, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	duplicates := 0
	for _, entry := range entries {
		item := Item{GUID: strings.TrimSpace(entry.GUID), URL: strings.TrimSpace(entry.URL), Title: strings.TrimSpace(entry.Title), SizeBytes: cloneSize(entry.SizeBytes)}
		item.Key = fingerprint(item)
		if _, exists := seen[item.Key]; exists {
			duplicates++
			continue
		}
		seen[item.Key] = struct{}{}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Key < items[j].Key })
	return items, duplicates
}

func fingerprint(item Item) string {
	size := ""
	if item.SizeBytes != nil {
		size = strconv.FormatInt(*item.SizeBytes, 10)
	}
	payload := strings.Join([]string{item.GUID, item.URL, item.Title, size}, "\x00")
	digest := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(digest[:])
}

func cloneSize(value *int64) *int64 {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

type MemoryStore struct {
	mu     sync.Mutex
	states map[string]FeedState
}

func (store *MemoryStore) Load(_ context.Context, feedID string) (FeedState, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.states == nil {
		store.states = make(map[string]FeedState)
	}
	return cloneState(store.states[feedID]), nil
}

func (store *MemoryStore) Save(_ context.Context, state FeedState) error {
	if strings.TrimSpace(state.FeedID) == "" {
		return errors.New("feed ID is required")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.states == nil {
		store.states = make(map[string]FeedState)
	}
	store.states[state.FeedID] = cloneState(state)
	return nil
}

func cloneState(state FeedState) FeedState {
	result := state
	result.Items = make([]Item, len(state.Items))
	for index, item := range state.Items {
		result.Items[index] = item
		result.Items[index].SizeBytes = cloneSize(item.SizeBytes)
	}
	return result
}
