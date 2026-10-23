package ruleaggregate

import (
	"errors"
	"sort"
	"strings"
	"time"
)

const AlgorithmVersion = "rule-aggregation-v1"

type Match struct {
	RuleID              string
	RuleName            string
	MaxJobs             int
	SeedTimeSeconds     int64
	DeleteMode          string
	RuleRevision        int
	Priority            int
	TargetMode          string
	TargetInstanceID    string
	TargetGroupID       string
	Category            string
	SavePath            string
	AddParams           map[string]string
	Tags                []string
	AllowedProfiles     []string
	StorageLabels       []string
	ParticipationWindow time.Duration
}

type Snapshot struct {
	RuleID       string
	RuleName     string
	RuleRevision int
	Priority     int
	IsWinner     bool
}

type Result struct {
	AlgorithmVersion    string
	Matches             []Snapshot
	Winner              Match
	Tags                []string
	AllowedProfiles     []string
	StorageLabels       []string
	ParticipationWindow time.Duration
	Rejected            bool
	Reason              string
}

func Aggregate(matches []Match, reservedTags []string) (Result, error) {
	if len(matches) == 0 {
		return Result{}, errors.New("at least one rule match is required")
	}
	ordered := make([]Match, len(matches))
	for index, match := range matches {
		if err := validateMatch(match); err != nil {
			return Result{}, err
		}
		ordered[index] = cloneMatch(match)
	}
	sort.SliceStable(ordered, func(left, right int) bool {
		if ordered[left].Priority != ordered[right].Priority {
			return ordered[left].Priority > ordered[right].Priority
		}
		if ordered[left].RuleRevision != ordered[right].RuleRevision {
			return ordered[left].RuleRevision > ordered[right].RuleRevision
		}
		return ordered[left].RuleID < ordered[right].RuleID
	})
	seen := make(map[string]struct{}, len(ordered))
	for _, match := range ordered {
		key := match.RuleID + "\x00" + string(rune(match.RuleRevision))
		if _, exists := seen[key]; exists {
			return Result{}, errors.New("duplicate rule version match")
		}
		seen[key] = struct{}{}
	}
	winner := ordered[0]
	profiles, ok := intersectConstraints(ordered, func(match Match) []string { return match.AllowedProfiles })
	if !ok {
		return Result{AlgorithmVersion: AlgorithmVersion, Rejected: true, Reason: "PROFILE_CONSTRAINT_INTERSECTION_EMPTY"}, nil
	}
	storageLabels, ok := intersectConstraints(ordered, func(match Match) []string { return match.StorageLabels })
	if !ok {
		return Result{AlgorithmVersion: AlgorithmVersion, Rejected: true, Reason: "STORAGE_CONSTRAINT_INTERSECTION_EMPTY"}, nil
	}
	reserved := make(map[string]struct{}, len(reservedTags))
	for _, tag := range reservedTags {
		reserved[tag] = struct{}{}
	}
	tagSet := make(map[string]struct{})
	for _, match := range ordered {
		for _, tag := range match.Tags {
			if tag == "" {
				continue
			}
			if _, isReserved := reserved[tag]; !isReserved {
				tagSet[tag] = struct{}{}
			}
		}
	}
	tags := make([]string, 0, len(tagSet))
	for tag := range tagSet {
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	result := Result{AlgorithmVersion: AlgorithmVersion, Winner: winner, Tags: tags, AllowedProfiles: profiles, StorageLabels: storageLabels, ParticipationWindow: winner.ParticipationWindow}
	result.Matches = make([]Snapshot, len(ordered))
	for index, match := range ordered {
		result.Matches[index] = Snapshot{RuleID: match.RuleID, RuleName: match.RuleName, RuleRevision: match.RuleRevision, Priority: match.Priority, IsWinner: index == 0}
	}
	return result, nil
}

func validateMatch(match Match) error {
	if strings.TrimSpace(match.RuleID) == "" || match.RuleRevision <= 0 {
		return errors.New("rule match identity is invalid")
	}
	if match.TargetMode != "FIXED_INSTANCE" && match.TargetMode != "INSTANCE_GROUP" {
		return errors.New("rule match target mode is invalid")
	}
	if match.TargetMode == "FIXED_INSTANCE" && (match.TargetInstanceID == "" || match.TargetGroupID != "") {
		return errors.New("fixed target snapshot is invalid")
	}
	if match.TargetMode == "INSTANCE_GROUP" && (match.TargetGroupID == "" || match.TargetInstanceID != "") {
		return errors.New("group target snapshot is invalid")
	}
	if match.ParticipationWindow <= 0 {
		return errors.New("participation window is invalid")
	}
	return nil
}

func intersectConstraints(matches []Match, selectValues func(Match) []string) ([]string, bool) {
	var current map[string]struct{}
	for _, match := range matches {
		values := selectValues(match)
		if len(values) == 0 {
			continue
		}
		candidate := make(map[string]struct{}, len(values))
		for _, value := range values {
			if value != "" {
				candidate[value] = struct{}{}
			}
		}
		if current == nil {
			current = candidate
			continue
		}
		for value := range current {
			if _, exists := candidate[value]; !exists {
				delete(current, value)
			}
		}
		if len(current) == 0 {
			return nil, false
		}
	}
	if current == nil {
		return nil, true
	}
	values := make([]string, 0, len(current))
	for value := range current {
		values = append(values, value)
	}
	sort.Strings(values)
	return values, true
}

func cloneMatch(match Match) Match {
	clone := match
	clone.Tags = append([]string(nil), match.Tags...)
	clone.AllowedProfiles = append([]string(nil), match.AllowedProfiles...)
	clone.StorageLabels = append([]string(nil), match.StorageLabels...)
	clone.AddParams = make(map[string]string, len(match.AddParams))
	for key, value := range match.AddParams {
		clone.AddParams[key] = value
	}
	return clone
}
