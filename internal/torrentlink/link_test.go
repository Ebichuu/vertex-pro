package torrentlink

import (
	"context"
	"sync"
	"testing"

	"github.com/vertex-pro/vertex-pro/internal/torrentidentity"
)

func TestLinkerReusesIdentityAcrossSites(t *testing.T) {
	linker, err := New(&MemoryStore{})
	if err != nil {
		t.Fatal(err)
	}
	identity := torrentidentity.Identity{Version: torrentidentity.VersionV1, V1: "1111111111111111111111111111111111111111"}
	first, err := linker.Link(context.Background(), "site-a", "source-a", identity)
	if err != nil {
		t.Fatal(err)
	}
	second, err := linker.Link(context.Background(), "site-b", "source-b", identity)
	if err != nil {
		t.Fatal(err)
	}
	if !first.CreatedIdentity || first.DuplicateKind != "NEW" || !second.CreatedSource || second.DuplicateKind != "CROSS_SITE_DUPLICATE" || first.Identity.ID != second.Identity.ID {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
}

func TestLinkerSameSourceIsIdempotentAndRejectsRebind(t *testing.T) {
	linker, err := New(&MemoryStore{})
	if err != nil {
		t.Fatal(err)
	}
	one := torrentidentity.Identity{Version: torrentidentity.VersionV1, V1: "2222222222222222222222222222222222222222"}
	two := torrentidentity.Identity{Version: torrentidentity.VersionV1, V1: "3333333333333333333333333333333333333333"}
	first, err := linker.Link(context.Background(), "site-a", "source-a", one)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := linker.Link(context.Background(), "site-a", "source-a", one)
	if err != nil || replay.DuplicateKind != "SAME_SOURCE" || replay.Identity.ID != first.Identity.ID {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	if _, err := linker.Link(context.Background(), "site-a", "source-a", two); err == nil {
		t.Fatal("expected rebind rejection")
	}
}

func TestLinkerHybridAliasesShareOnePhysicalIdentity(t *testing.T) {
	store := &MemoryStore{}
	linker, err := New(store)
	if err != nil {
		t.Fatal(err)
	}
	identity := torrentidentity.Identity{Version: torrentidentity.VersionHybrid, V1: "4444444444444444444444444444444444444444", V2: "5555555555555555555555555555555555555555555555555555555555555555"}
	result, err := linker.Link(context.Background(), "site-a", "source-a", identity)
	if err != nil {
		t.Fatal(err)
	}
	v1Only := torrentidentity.Identity{Version: torrentidentity.VersionV1, V1: identity.V1}
	replay, err := linker.Link(context.Background(), "site-b", "source-b", v1Only)
	if err != nil {
		t.Fatal(err)
	}
	if replay.Identity.ID != result.Identity.ID || replay.DuplicateKind != "CROSS_SITE_DUPLICATE" {
		t.Fatalf("result=%+v replay=%+v", result, replay)
	}
}

func TestLinkerConcurrentDuplicateLinksCreateOneIdentity(t *testing.T) {
	linker, err := New(&MemoryStore{})
	if err != nil {
		t.Fatal(err)
	}
	identity := torrentidentity.Identity{Version: torrentidentity.VersionV1, V1: "6666666666666666666666666666666666666666"}
	var wait sync.WaitGroup
	results := make([]Result, 8)
	errs := make([]error, 8)
	for index := range results {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			results[index], errs[index] = linker.Link(context.Background(), "site-a", "source-"+string(rune('a'+index)), identity)
		}(index)
	}
	wait.Wait()
	for index := range results {
		if errs[index] != nil {
			t.Fatal(errs[index])
		}
		if results[index].Identity.ID != results[0].Identity.ID {
			t.Fatalf("index=%d result=%+v first=%+v", index, results[index], results[0])
		}
	}
}

func TestLinkerRejectsInvalidIdentity(t *testing.T) {
	linker, err := New(&MemoryStore{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := linker.Link(context.Background(), "site-a", "source-a", torrentidentity.Identity{Version: torrentidentity.VersionV1, V1: "short"}); err == nil {
		t.Fatal("expected digest error")
	}
	if _, err := linker.Link(context.Background(), "site-a", "source-a", torrentidentity.Identity{}); err == nil {
		t.Fatal("expected empty identity error")
	}
}
