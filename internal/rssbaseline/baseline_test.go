package rssbaseline

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestBaselineFirstScanOnlyCreatesBaseline(t *testing.T) {
	baseline, err := New(&MemoryStore{})
	if err != nil {
		t.Fatal(err)
	}
	entries := []Entry{{GUID: "a", URL: "https://example.test/a", Title: "A"}, {GUID: "b", URL: "https://example.test/b", Title: "B"}}
	result, err := baseline.Apply(context.Background(), "feed-a", entries)
	if err != nil {
		t.Fatal(err)
	}
	if !result.InitializedNow || result.BaselineCount != 2 || len(result.NewItems) != 0 {
		t.Fatalf("result=%+v", result)
	}
}

func TestBaselineReturnsOnlyNewItemsAfterInitialization(t *testing.T) {
	baseline, err := New(&MemoryStore{})
	if err != nil {
		t.Fatal(err)
	}
	first := []Entry{{GUID: "a", URL: "https://example.test/a", Title: "A"}}
	if _, err := baseline.Apply(context.Background(), "feed-a", first); err != nil {
		t.Fatal(err)
	}
	result, err := baseline.Apply(context.Background(), "feed-a", []Entry{{GUID: "a", URL: "https://example.test/a", Title: "A"}, {GUID: "b", URL: "https://example.test/b", Title: "B"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.InitializedNow || result.BaselineCount != 2 || len(result.NewItems) != 1 || result.NewItems[0].GUID != "b" {
		t.Fatalf("result=%+v", result)
	}
}

func TestBaselineIsDeterministicForEmptyAndDuplicateScans(t *testing.T) {
	baseline, err := New(&MemoryStore{})
	if err != nil {
		t.Fatal(err)
	}
	first, err := baseline.Apply(context.Background(), "feed-a", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !first.InitializedNow || first.BaselineCount != 0 || len(first.NewItems) != 0 {
		t.Fatalf("first=%+v", first)
	}
	entries := []Entry{{GUID: "a", URL: "https://example.test/a", Title: " A "}, {GUID: "a", URL: "https://example.test/a", Title: " A "}}
	second, err := baseline.Apply(context.Background(), "feed-a", entries)
	if err != nil {
		t.Fatal(err)
	}
	if second.BaselineCount != 1 || len(second.NewItems) != 1 || second.DuplicateCount != 1 {
		t.Fatalf("second=%+v", second)
	}
	third, err := baseline.Apply(context.Background(), "feed-a", entries)
	if err != nil {
		t.Fatal(err)
	}
	if len(third.NewItems) != 0 || third.BaselineCount != 1 || third.DuplicateCount != 1 {
		t.Fatalf("third=%+v", third)
	}
}

func TestBaselineRestoresPersistedStateAcrossRestart(t *testing.T) {
	store := &MemoryStore{}
	first, err := New(store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Apply(context.Background(), "feed-a", []Entry{{GUID: "a", URL: "https://example.test/a", Title: "A"}}); err != nil {
		t.Fatal(err)
	}
	restarted, err := New(store)
	if err != nil {
		t.Fatal(err)
	}
	result, err := restarted.Apply(context.Background(), "feed-a", []Entry{{GUID: "a", URL: "https://example.test/a", Title: "A"}, {GUID: "b", URL: "https://example.test/b", Title: "B"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.NewItems) != 1 || result.NewItems[0].GUID != "b" {
		t.Fatalf("result=%+v", result)
	}
}

func TestBaselineRejectsInvalidInputsAndStoreErrors(t *testing.T) {
	if _, err := New(nil); err == nil {
		t.Fatal("expected store error")
	}
	baseline, err := New(&failingStore{loadErr: errors.New("load failed")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := baseline.Apply(context.Background(), "", nil); err == nil {
		t.Fatal("expected feed ID error")
	}
	if _, err := baseline.Apply(context.Background(), "feed-a", nil); err == nil || !strings.Contains(err.Error(), "load baseline") {
		t.Fatalf("err=%v", err)
	}
	baseline, err = New(&failingStore{saveErr: errors.New("save failed")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := baseline.Apply(context.Background(), "feed-a", nil); err == nil || !strings.Contains(err.Error(), "save baseline") {
		t.Fatalf("err=%v", err)
	}
}

func TestMemoryStoreReturnsIndependentCopies(t *testing.T) {
	store := &MemoryStore{}
	if err := store.Save(context.Background(), FeedState{FeedID: "feed-a", Initialized: true, Items: []Item{{Key: "key", GUID: "a"}}}); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(context.Background(), "feed-a")
	if err != nil {
		t.Fatal(err)
	}
	loaded.Items[0].GUID = "changed"
	again, err := store.Load(context.Background(), "feed-a")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(again.Items[0].GUID, "a") {
		t.Fatalf("again=%+v", again)
	}
}

type failingStore struct {
	loadErr error
	saveErr error
}

func (store *failingStore) Load(context.Context, string) (FeedState, error) {
	return FeedState{}, store.loadErr
}
func (store *failingStore) Save(context.Context, FeedState) error { return store.saveErr }
