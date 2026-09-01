package ruleversion

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/rules"
)

type Clock interface{ Now() time.Time }
type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now().UTC() }

type Rule struct {
	ID               string
	SiteID           string
	Name             string
	Enabled          bool
	Priority         int
	CurrentVersionID string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type Version struct {
	ID                     string
	RuleID                 string
	Revision               int
	ConditionSchemaVersion int
	ConditionTree          rules.ConditionTree
	ActionSchemaVersion    int
	ActionSetJSON          json.RawMessage
	TargetMode             string
	TargetInstanceID       string
	TargetGroupID          string
	ParticipationWindow    time.Duration
	CreatedAt              time.Time
}

type RuleInput struct {
	SiteID   string
	Name     string
	Enabled  bool
	Priority int
}

type VersionInput struct {
	ConditionTree       rules.ConditionTree
	ActionSchemaVersion int
	ActionSetJSON       json.RawMessage
	TargetMode          string
	TargetInstanceID    string
	TargetGroupID       string
	ParticipationWindow time.Duration
}

type Store struct {
	mu       sync.RWMutex
	clock    Clock
	rules    map[string]Rule
	versions map[string][]Version
	byName   map[string]string
}

func New(clock Clock) *Store {
	if clock == nil {
		clock = RealClock{}
	}
	return &Store{clock: clock, rules: make(map[string]Rule), versions: make(map[string][]Version), byName: make(map[string]string)}
}

func (store *Store) Create(ctx context.Context, input RuleInput, versionInput VersionInput) (Rule, Version, error) {
	if err := contextError(ctx); err != nil {
		return Rule{}, Version{}, err
	}
	if store == nil {
		return Rule{}, Version{}, errors.New("rule version store is nil")
	}
	if input.Name == "" {
		return Rule{}, Version{}, errors.New("rule name is required")
	}
	if err := validateVersionInput(versionInput); err != nil {
		return Rule{}, Version{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	key := input.SiteID + "\x00" + input.Name
	if _, exists := store.byName[key]; exists {
		return Rule{}, Version{}, errors.New("rule name already exists for site")
	}
	now := store.clock.Now().UTC()
	ruleID := deterministicID(input.SiteID + "\x00" + input.Name)
	rule := Rule{ID: ruleID, SiteID: input.SiteID, Name: input.Name, Enabled: input.Enabled, Priority: input.Priority, CreatedAt: now, UpdatedAt: now}
	version := buildVersion(ruleID, 1, versionInput, now)
	rule.CurrentVersionID = version.ID
	store.rules[ruleID] = rule
	store.versions[ruleID] = []Version{version}
	store.byName[key] = ruleID
	return cloneRule(rule), cloneVersion(version), nil
}

func (store *Store) CreateVersion(ctx context.Context, ruleID string, input VersionInput) (Version, error) {
	if err := contextError(ctx); err != nil {
		return Version{}, err
	}
	if store == nil {
		return Version{}, errors.New("rule version store is nil")
	}
	if ruleID == "" {
		return Version{}, errors.New("rule ID is required")
	}
	if err := validateVersionInput(input); err != nil {
		return Version{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	rule, exists := store.rules[ruleID]
	if !exists {
		return Version{}, errors.New("rule not found")
	}
	versions := store.versions[ruleID]
	revision := len(versions) + 1
	now := store.clock.Now().UTC()
	version := buildVersion(ruleID, revision, input, now)
	versions = append(versions, version)
	store.versions[ruleID] = versions
	rule.CurrentVersionID = version.ID
	rule.UpdatedAt = now
	store.rules[ruleID] = rule
	return cloneVersion(version), nil
}

func (store *Store) GetRule(ctx context.Context, ruleID string) (Rule, error) {
	if err := contextError(ctx); err != nil {
		return Rule{}, err
	}
	if store == nil {
		return Rule{}, errors.New("rule version store is nil")
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	rule, ok := store.rules[ruleID]
	if !ok {
		return Rule{}, errors.New("rule not found")
	}
	return cloneRule(rule), nil
}

func (store *Store) GetVersion(ctx context.Context, versionID string) (Version, error) {
	if err := contextError(ctx); err != nil {
		return Version{}, err
	}
	if store == nil {
		return Version{}, errors.New("rule version store is nil")
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	for _, versions := range store.versions {
		for _, version := range versions {
			if version.ID == versionID {
				return cloneVersion(version), nil
			}
		}
	}
	return Version{}, errors.New("rule version not found")
}

func (store *Store) ListVersions(ctx context.Context, ruleID string) ([]Version, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if store == nil {
		return nil, errors.New("rule version store is nil")
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	versions, ok := store.versions[ruleID]
	if !ok {
		return nil, errors.New("rule not found")
	}
	result := make([]Version, len(versions))
	for index, version := range versions {
		result[index] = cloneVersion(version)
	}
	return result, nil
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return errors.New("context is nil")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("context cancelled: %w", err)
	}
	return nil
}

func validateVersionInput(input VersionInput) error {
	if err := input.ConditionTree.Validate(); err != nil {
		return fmt.Errorf("condition tree: %w", err)
	}
	if err := rules.ValidateCatalog(input.ConditionTree); err != nil {
		return fmt.Errorf("condition catalog: %w", err)
	}
	if input.ActionSchemaVersion <= 0 {
		return errors.New("action schema version is required")
	}
	if len(input.ActionSetJSON) == 0 || !json.Valid(input.ActionSetJSON) {
		return errors.New("action set JSON is invalid")
	}
	if input.TargetMode != "FIXED_INSTANCE" && input.TargetMode != "INSTANCE_GROUP" {
		return errors.New("target mode is invalid")
	}
	if (input.TargetMode == "FIXED_INSTANCE") == (input.TargetMode == "INSTANCE_GROUP") {
		return errors.New("target mode is invalid")
	}
	if input.TargetMode == "FIXED_INSTANCE" && (input.TargetInstanceID == "" || input.TargetGroupID != "") {
		return errors.New("fixed target is invalid")
	}
	if input.TargetMode == "INSTANCE_GROUP" && (input.TargetGroupID == "" || input.TargetInstanceID != "") {
		return errors.New("group target is invalid")
	}
	if input.ParticipationWindow <= 0 {
		return errors.New("participation window is required")
	}
	return nil
}

func buildVersion(ruleID string, revision int, input VersionInput, now time.Time) Version {
	return Version{ID: fmt.Sprintf("%s-v-%d", ruleID, revision), RuleID: ruleID, Revision: revision, ConditionSchemaVersion: input.ConditionTree.Version, ConditionTree: cloneTree(input.ConditionTree), ActionSchemaVersion: input.ActionSchemaVersion, ActionSetJSON: append(json.RawMessage(nil), input.ActionSetJSON...), TargetMode: input.TargetMode, TargetInstanceID: input.TargetInstanceID, TargetGroupID: input.TargetGroupID, ParticipationWindow: input.ParticipationWindow, CreatedAt: now}
}

func deterministicID(value string) string {
	digest := sha256.Sum256([]byte(value))
	return "rule-" + hex.EncodeToString(digest[:])
}
func cloneRule(rule Rule) Rule { return rule }
func cloneVersion(version Version) Version {
	version.ConditionTree = cloneTree(version.ConditionTree)
	version.ActionSetJSON = append(json.RawMessage(nil), version.ActionSetJSON...)
	return version
}
func cloneTree(tree rules.ConditionTree) rules.ConditionTree {
	tree.Root = cloneNode(tree.Root)
	return tree
}

func cloneNode(node rules.Node) rules.Node {
	clone := node
	if node.Children != nil {
		clone.Children = make([]rules.Node, len(node.Children))
		for index, child := range node.Children {
			clone.Children[index] = cloneNode(child)
		}
	}
	if node.Value != nil {
		value := *node.Value
		value.Strings = append([]string(nil), node.Value.Strings...)
		value.Numbers = append([]float64(nil), node.Value.Numbers...)
		value.Times = append([]string(nil), node.Value.Times...)
		if node.Value.String != nil {
			copied := *node.Value.String
			value.String = &copied
		}
		if node.Value.Number != nil {
			copied := *node.Value.Number
			value.Number = &copied
		}
		if node.Value.Boolean != nil {
			copied := *node.Value.Boolean
			value.Boolean = &copied
		}
		if node.Value.Time != nil {
			copied := *node.Value.Time
			value.Time = &copied
		}
		clone.Value = &value
	}
	return clone
}
