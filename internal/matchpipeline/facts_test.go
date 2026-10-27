package matchpipeline

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/racejob"
	"github.com/vertex-pro/vertex-pro/internal/racejobstore"
	"github.com/vertex-pro/vertex-pro/internal/ruleaggregate"
	"github.com/vertex-pro/vertex-pro/internal/ruleservice"
)

func TestReleaseGroup(t *testing.T) {
	for title, want := range map[string]string{
		"The.Movie.2026.1080p.BluRay.x264-WiKi":                "WiKi",
		"The Movie 2026 2160p WEB-DL H265 DDP5.1-CHDWEB [电影名]": "CHDWEB",
		"Show.S01E02.1080p.WEB-DL.H264-HHWEB@HHCLUB":           "HHWEB@HHCLUB",
		"Show S01E02 1080p WEB-DL":                             "",
		"Some.Release.BD-Rip":                                  "",
		"[Nekomoe kissaten] Anime - 05 [1080p]":                "Nekomoe kissaten",
		"Plain Title Without Group":                            "",
		"Movie.2026.1080p.WEB-DL.AAC-Group (Extended) 【中字】":    "Group",
	} {
		if got := ReleaseGroup(title); got != want {
			t.Errorf("ReleaseGroup(%q) = %q, want %q", title, got, want)
		}
	}
}

func TestBuildFactsFillsRSSDerivedFields(t *testing.T) {
	candidate := item()
	candidate.Source.Title = "Movie.2026.1080p.WEB-DL-GRP"
	candidate.Category, candidate.Publisher = "Movies", "Alice"
	candidate.PublishedAt = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	candidate.DiscoveredAt = candidate.PublishedAt.Add(90*time.Second + 400*time.Millisecond)
	facts := buildFacts(candidate)
	if *facts["category"].Value.String != "Movies" || *facts["publisher"].Value.String != "Alice" || *facts["group"].Value.String != "GRP" || *facts["discovery_delay"].Value.Number != 90 {
		t.Fatalf("facts = %+v", facts)
	}
	candidate.DiscoveredAt = candidate.PublishedAt.Add(-time.Minute)
	if delay := *buildFacts(candidate)["discovery_delay"].Value.Number; delay != 0 {
		t.Fatalf("skewed delay = %v", delay)
	}
	candidate.DiscoveredAt = time.Time{}
	if _, ok := buildFacts(candidate)["discovery_delay"]; ok {
		t.Fatal("delay without discovery time")
	}
}

func categoryTree(category string) json.RawMessage {
	payload, _ := json.Marshal(map[string]any{"condition_tree": map[string]any{"version": 1, "root": map[string]any{
		"kind": "leaf", "field": "category", "operator": "equals", "value": map[string]any{"type": "string", "string": category},
	}}})
	return payload
}

// Before category was filled, this rule was always UNKNOWN and never matched.
func TestCategoryRuleMatchesAndPrefiltersWithoutDownload(t *testing.T) {
	database := pipelineDatabase(t)
	seedSite(t, database, "site-1")
	service, err := ruleservice.New(database, nil)
	if err != nil {
		t.Fatal(err)
	}
	seedRule(t, service, "site-1", "movies", 1, categoryTree("Movies"))
	store, err := racejobstore.New(database)
	if err != nil {
		t.Fatal(err)
	}
	creator, err := racejob.New(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	deps := Deps{Rules: &service, JobCreator: creator, MatchFactory: func(ruleservice.Rule) (ruleaggregate.Match, error) {
		return ruleaggregate.Match{TargetMode: "FIXED_INSTANCE", TargetInstanceID: "instance-1", ParticipationWindow: time.Hour}, nil
	}}
	tv := item()
	tv.Category = "TV"
	if _, rejected, err := Prefilter(context.Background(), deps, tv); err != nil || !rejected {
		t.Fatalf("TV prefilter rejected=%v err=%v", rejected, err)
	}
	movie := item()
	movie.Category = "Movies"
	if _, rejected, err := Prefilter(context.Background(), deps, movie); err != nil || rejected {
		t.Fatalf("movie prefilter rejected=%v err=%v", rejected, err)
	}
	outcome, err := Process(context.Background(), deps, movie)
	if err != nil || !outcome.Matched || !outcome.JobCreated {
		t.Fatalf("movie outcome=%+v err=%v", outcome, err)
	}
}
