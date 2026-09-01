package ruleversion

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/rules"
)

func TestStoreCreatesImmutableVersionsAndMovesCurrentPointer(t *testing.T) {
	store := New(fixedClock{value: time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)})
	first, versionOne, err := store.Create(context.Background(), RuleInput{SiteID: "site-1", Name: "race", Enabled: true}, validVersionInput("title-one"))
	if err != nil {
		t.Fatal(err)
	}
	if first.CurrentVersionID != versionOne.ID || versionOne.Revision != 1 {
		t.Fatalf("rule=%+v version=%+v", first, versionOne)
	}
	versionTwoInput := validVersionInput("title-two")
	versionTwo, err := store.CreateVersion(context.Background(), first.ID, versionTwoInput)
	if err != nil {
		t.Fatal(err)
	}
	if versionTwo.Revision != 2 || versionTwo.ID == versionOne.ID {
		t.Fatalf("version=%+v", versionTwo)
	}
	current, err := store.GetRule(context.Background(), first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.CurrentVersionID != versionTwo.ID {
		t.Fatalf("current=%+v", current)
	}
	historical, err := store.GetVersion(context.Background(), versionOne.ID)
	if err != nil {
		t.Fatal(err)
	}
	if historical.Revision != 1 || *historical.ConditionTree.Root.Value.String != "title-one" {
		t.Fatalf("historical=%+v", historical)
	}
	versionTwo.ActionSetJSON[0] = 'x'
	reloaded, err := store.GetVersion(context.Background(), versionTwo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.ActionSetJSON[0] == 'x' {
		t.Fatal("stored action JSON was mutable")
	}
}

func TestStoreSerializesConcurrentVersionCreation(t *testing.T) {
	store := New(nil)
	rule, _, err := store.Create(context.Background(), RuleInput{SiteID: "site-1", Name: "race"}, validVersionInput("one"))
	if err != nil {
		t.Fatal(err)
	}
	const count = 16
	var wait sync.WaitGroup
	errorsCh := make(chan error, count)
	for index := 0; index < count; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			input := validVersionInput("value")
			input.ActionSetJSON = []byte(`{"index":` + string(rune('0'+index%10)) + `}`)
			_, createErr := store.CreateVersion(context.Background(), rule.ID, input)
			errorsCh <- createErr
		}(index)
	}
	wait.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	versions, err := store.ListVersions(context.Background(), rule.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != count+1 {
		t.Fatalf("versions=%d", len(versions))
	}
	current, err := store.GetRule(context.Background(), rule.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.CurrentVersionID != versions[len(versions)-1].ID || versions[len(versions)-1].Revision != count+1 {
		t.Fatalf("current=%+v last=%+v", current, versions[len(versions)-1])
	}
}

func TestStoreRejectsInvalidTargetsAndCanceledContext(t *testing.T) {
	store := New(nil)
	invalid := validVersionInput("value")
	invalid.TargetMode = "FIXED_INSTANCE"
	invalid.TargetGroupID = "group"
	if _, _, err := store.Create(context.Background(), RuleInput{SiteID: "site", Name: "bad"}, invalid); err == nil {
		t.Fatal("expected target validation error")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := store.Create(ctx, RuleInput{SiteID: "site", Name: "bad"}, validVersionInput("value")); !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}

func TestStoreRejectsDuplicateRuleNamePerSite(t *testing.T) {
	store := New(nil)
	if _, _, err := store.Create(context.Background(), RuleInput{SiteID: "site", Name: "same"}, validVersionInput("one")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Create(context.Background(), RuleInput{SiteID: "site", Name: "same"}, validVersionInput("two")); err == nil {
		t.Fatal("expected duplicate rule error")
	}
	if _, _, err := store.Create(context.Background(), RuleInput{SiteID: "other", Name: "same"}, validVersionInput("three")); err != nil {
		t.Fatal(err)
	}
}

func validVersionInput(title string) VersionInput {
	return VersionInput{ConditionTree: rules.ConditionTree{Version: rules.CurrentVersion, Root: rules.Node{Kind: rules.LeafKind, Field: "title", Operator: rules.EqualsOperator, Value: &rules.Value{Type: rules.StringValue, String: &title}}}, ActionSchemaVersion: 1, ActionSetJSON: []byte(`{"category":"race"}`), TargetMode: "FIXED_INSTANCE", TargetInstanceID: "qb-a", ParticipationWindow: time.Hour}
}

type fixedClock struct{ value time.Time }

func (clock fixedClock) Now() time.Time { return clock.value }
