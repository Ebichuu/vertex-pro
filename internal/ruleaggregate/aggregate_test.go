package ruleaggregate

import (
	"reflect"
	"testing"
	"time"
)

func TestAggregateIsDeterministicAcrossInputOrder(t *testing.T) {
	matches := []Match{
		{RuleID: "rule-b", RuleRevision: 2, Priority: 100, TargetMode: "INSTANCE_GROUP", TargetGroupID: "group-b", Category: "other", Tags: []string{"free", "b"}, AllowedProfiles: []string{"profile-a", "profile-b"}, StorageLabels: []string{"ssd", "hdd"}, ParticipationWindow: time.Hour},
		{RuleID: "rule-a", RuleRevision: 1, Priority: 100, TargetMode: "FIXED_INSTANCE", TargetInstanceID: "qb-a", Category: "race", Tags: []string{"free", "a"}, AllowedProfiles: []string{"profile-a", "profile-c"}, StorageLabels: []string{"ssd"}, ParticipationWindow: 2 * time.Hour},
		{RuleID: "rule-c", RuleRevision: 3, Priority: 90, TargetMode: "FIXED_INSTANCE", TargetInstanceID: "qb-c", Category: "ignored", Tags: []string{"c"}, AllowedProfiles: []string{"profile-a"}, StorageLabels: []string{"ssd", "hdd"}, ParticipationWindow: 3 * time.Hour},
	}
	first, err := Aggregate(matches, []string{"vertex-pro"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Aggregate([]Match{matches[2], matches[0], matches[1]}, []string{"vertex-pro"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
	if first.Winner.RuleID != "rule-b" || first.Winner.RuleRevision != 2 {
		t.Fatalf("winner=%+v", first.Winner)
	}
	if !reflect.DeepEqual(first.Tags, []string{"a", "b", "c", "free"}) {
		t.Fatalf("tags=%v", first.Tags)
	}
	if !reflect.DeepEqual(first.AllowedProfiles, []string{"profile-a"}) || !reflect.DeepEqual(first.StorageLabels, []string{"ssd"}) {
		t.Fatalf("constraints=%v/%v", first.AllowedProfiles, first.StorageLabels)
	}
	if first.ParticipationWindow != time.Hour {
		t.Fatalf("window=%s", first.ParticipationWindow)
	}
	if len(first.Matches) != 3 || !first.Matches[0].IsWinner {
		t.Fatalf("snapshots=%+v", first.Matches)
	}
}

func TestAggregateRejectsEmptyConstraintIntersection(t *testing.T) {
	base := Match{TargetMode: "FIXED_INSTANCE", TargetInstanceID: "qb-a", RuleRevision: 1, ParticipationWindow: time.Hour}
	base.RuleID = "rule-a"
	base.AllowedProfiles = []string{"a"}
	other := base
	other.RuleID = "rule-b"
	other.AllowedProfiles = []string{"b"}
	result, err := Aggregate([]Match{base, other}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Rejected || result.Reason != "PROFILE_CONSTRAINT_INTERSECTION_EMPTY" {
		t.Fatalf("result=%+v", result)
	}
}

func TestAggregateRejectsDuplicateAndInvalidMatches(t *testing.T) {
	match := Match{RuleID: "rule-a", RuleRevision: 1, TargetMode: "FIXED_INSTANCE", TargetInstanceID: "qb-a", ParticipationWindow: time.Hour}
	if _, err := Aggregate([]Match{match, match}, nil); err == nil {
		t.Fatal("expected duplicate error")
	}
	match.TargetMode = "INSTANCE_GROUP"
	if _, err := Aggregate([]Match{match}, nil); err == nil {
		t.Fatal("expected invalid target error")
	}
	if _, err := Aggregate(nil, nil); err == nil {
		t.Fatal("expected empty input error")
	}
}

func TestAggregateDoesNotAllowReservedTagsToBeOverridden(t *testing.T) {
	match := Match{RuleID: "rule-a", RuleRevision: 1, TargetMode: "FIXED_INSTANCE", TargetInstanceID: "qb-a", ParticipationWindow: time.Hour, Tags: []string{"vertex-pro", "user"}}
	result, err := Aggregate([]Match{match}, []string{"vertex-pro"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Tags, []string{"user"}) {
		t.Fatalf("tags=%v", result.Tags)
	}
}
