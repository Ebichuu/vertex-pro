package rsssource

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestDedupeCollapsesSameSiteDuplicateByGUID(t *testing.T) {
	dedupe, err := New(&MemoryStore{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := dedupe.Apply(context.Background(), "site-a", []Entry{{GUID: "g1", URL: "https://EXAMPLE.test/a", Title: "A"}, {GUID: "g1", URL: "https://example.test/a", Title: "A"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.New) != 1 || result.Duplicates != 1 {
		t.Fatalf("result=%+v", result)
	}
}

func TestDedupeUsesURLWhenGUIDMissingAndIsolatesSites(t *testing.T) {
	store := &MemoryStore{}
	dedupe, err := New(store)
	if err != nil {
		t.Fatal(err)
	}
	entry := Entry{URL: "https://example.test/a", Title: "A"}
	first, err := dedupe.Apply(context.Background(), "site-a", []Entry{entry, entry})
	if err != nil {
		t.Fatal(err)
	}
	second, err := dedupe.Apply(context.Background(), "site-b", []Entry{entry})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.New) != 1 || first.Duplicates != 1 || len(second.New) != 1 {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
}

func TestDedupeUpdatesFieldsWithoutCreatingDuplicate(t *testing.T) {
	store := &MemoryStore{}
	dedupe, err := New(store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dedupe.Apply(context.Background(), "site-a", []Entry{{GUID: "g1", Title: "Old", SizeBytes: int64ptr(10)}}); err != nil {
		t.Fatal(err)
	}
	result, err := dedupe.Apply(context.Background(), "site-a", []Entry{{GUID: "g1", Title: "New", SizeBytes: int64ptr(20)}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.New) != 0 || len(result.Updated) != 1 || result.Updated[0].Title != "New" {
		t.Fatalf("result=%+v", result)
	}
	loaded, err := store.Load(context.Background(), "site-a")
	if err != nil || len(loaded) != 1 || loaded[0].Title != "New" {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
}

func TestDedupeUsesTitleAndSizeWhenGUIDAndURLMissing(t *testing.T) {
	dedupe, err := New(&MemoryStore{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := dedupe.Apply(context.Background(), "site-a", []Entry{{Title: "Same", SizeBytes: int64ptr(1)}, {Title: "Same", SizeBytes: int64ptr(1)}, {Title: "Same", SizeBytes: int64ptr(2)}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.New) != 2 || result.Duplicates != 1 {
		t.Fatalf("result=%+v", result)
	}
}

func TestDedupeRejectsInvalidInputsAndStoreErrors(t *testing.T) {
	if _, err := New(nil); err == nil {
		t.Fatal("expected store error")
	}
	dedupe, err := New(&failingStore{loadErr: errors.New("load failed")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dedupe.Apply(context.Background(), "", nil); err == nil {
		t.Fatal("expected site ID error")
	}
	if _, err := dedupe.Apply(context.Background(), "site-a", nil); err == nil || !strings.Contains(err.Error(), "load sources") {
		t.Fatalf("err=%v", err)
	}
	dedupe, err = New(&failingStore{saveErr: errors.New("save failed")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dedupe.Apply(context.Background(), "site-a", []Entry{{GUID: "g1"}}); err == nil || !strings.Contains(err.Error(), "save source") {
		t.Fatalf("err=%v", err)
	}
	if _, err := dedupe.Apply(context.Background(), "site-a", []Entry{{URL: "file:///tmp/a"}}); err == nil {
		t.Fatal("expected URL error")
	}
}

func int64ptr(value int64) *int64 { return &value }

type failingStore struct{ loadErr, saveErr error }

func (store *failingStore) Load(context.Context, string) ([]Source, error) { return nil, store.loadErr }
func (store *failingStore) Save(context.Context, Source) error             { return store.saveErr }
