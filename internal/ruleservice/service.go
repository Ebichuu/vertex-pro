package ruleservice

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
)

var (
	ErrNotFound     = errors.New("rule not found")
	ErrConflict     = errors.New("rule revision conflict")
	ErrSiteNotFound = errors.New("site not found")
)

var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

type Clock interface {
	Now() time.Time
}

type ClockFunc func() time.Time

func (clock ClockFunc) Now() time.Time { return clock() }

type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now().UTC() }

type Service struct {
	database *sql.DB
	clock    Clock
}

type Rule struct {
	ID         string
	SiteID     string
	Name       string
	Enabled    bool
	Priority   int
	Revision   int
	State      string
	Definition json.RawMessage
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type CreateInput struct {
	ID         string
	SiteID     string
	Name       string
	Enabled    bool
	Priority   int
	Definition json.RawMessage
}

type UpdateInput struct {
	Name       *string
	Enabled    *bool
	Priority   *int
	Definition json.RawMessage
}

func New(database *sql.DB, clock Clock) (Service, error) {
	if database == nil {
		return Service{}, errors.New("database is required")
	}
	if clock == nil {
		clock = RealClock{}
	}
	return Service{database: database, clock: clock}, nil
}

func (service Service) Create(ctx context.Context, input CreateInput) (Rule, error) {
	if err := validateID(input.ID, "rule ID"); err != nil && input.ID != "" {
		return Rule{}, err
	}
	name := strings.TrimSpace(input.Name)
	if err := validateName(name); err != nil {
		return Rule{}, err
	}
	if input.SiteID != "" {
		if err := validateID(input.SiteID, "site ID"); err != nil {
			return Rule{}, err
		}
	}
	if input.Priority < 0 {
		return Rule{}, errors.New("invalid rule priority")
	}
	definition, err := normalizeDefinition(input.Definition)
	if err != nil {
		return Rule{}, err
	}
	id := input.ID
	if id == "" {
		id = deterministicID(input.SiteID + "\x00" + name)
	}
	now := service.clock.Now().UTC()
	nowText := now.Format(time.RFC3339Nano)
	result, err := service.database.ExecContext(ctx, `INSERT INTO rules(rule_id,site_id,name,enabled,priority,revision,state,definition_json,created_at,updated_at) SELECT ?,?,?,?,?,?,?,?,?,? WHERE ? IS NULL OR EXISTS(SELECT 1 FROM sites WHERE site_id=? AND archived=0)`, id, nullableID(input.SiteID), name, input.Enabled, input.Priority, 1, "PENDING", string(definition), nowText, nowText, nullableID(input.SiteID), input.SiteID)
	if err != nil {
		return Rule{}, fmt.Errorf("create rule: %w", err)
	}
	if count, err := result.RowsAffected(); err != nil {
		return Rule{}, err
	} else if count != 1 {
		return Rule{}, ErrSiteNotFound
	}
	return Rule{ID: id, SiteID: input.SiteID, Name: name, Enabled: input.Enabled, Priority: input.Priority, Revision: 1, State: "PENDING", Definition: definition, CreatedAt: now, UpdatedAt: now}, nil
}

func (service Service) List(ctx context.Context, siteID string) ([]Rule, error) {
	if siteID != "" {
		if err := validateID(siteID, "site ID"); err != nil {
			return nil, err
		}
	}
	query := `SELECT rule_id,COALESCE(site_id,''),name,enabled,priority,revision,state,definition_json,created_at,updated_at FROM rules WHERE archived=0`
	args := []any{}
	if siteID != "" {
		query += ` AND (site_id = ? OR site_id IS NULL)`
		args = append(args, siteID)
	}
	query += ` ORDER BY priority DESC, revision DESC, rule_id ASC`
	rows, err := service.database.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list rules: %w", err)
	}
	defer rows.Close()
	result := make([]Rule, 0)
	for rows.Next() {
		rule, err := scanRule(rows)
		if err != nil {
			return nil, fmt.Errorf("scan rule: %w", err)
		}
		result = append(result, rule)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate rules: %w", err)
	}
	return result, nil
}

func (service Service) Get(ctx context.Context, ruleID string) (Rule, error) {
	if err := validateID(ruleID, "rule ID"); err != nil {
		return Rule{}, err
	}
	row := service.database.QueryRowContext(ctx, `SELECT rule_id,COALESCE(site_id,''),name,enabled,priority,revision,state,definition_json,created_at,updated_at FROM rules WHERE archived=0 AND rule_id = ?`, ruleID)
	rule, err := scanRule(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Rule{}, ErrNotFound
	}
	if err != nil {
		return Rule{}, fmt.Errorf("get rule: %w", err)
	}
	return rule, nil
}

func (service Service) Update(ctx context.Context, ruleID string, expectedRevision int, input UpdateInput) (Rule, error) {
	if err := validateID(ruleID, "rule ID"); err != nil {
		return Rule{}, err
	}
	if expectedRevision <= 0 {
		return Rule{}, errors.New("invalid expected revision")
	}
	current, err := service.Get(ctx, ruleID)
	if err != nil {
		return Rule{}, err
	}
	name := current.Name
	if input.Name != nil {
		name = strings.TrimSpace(*input.Name)
		if err := validateName(name); err != nil {
			return Rule{}, err
		}
	}
	enabled := current.Enabled
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	priority := current.Priority
	if input.Priority != nil {
		if *input.Priority < 0 {
			return Rule{}, errors.New("invalid rule priority")
		}
		priority = *input.Priority
	}
	definition := current.Definition
	if input.Definition != nil {
		definition, err = normalizeDefinition(input.Definition)
		if err != nil {
			return Rule{}, err
		}
	}
	now := service.clock.Now().UTC()
	result, err := service.database.ExecContext(ctx, `UPDATE rules SET name=?,enabled=?,priority=?,revision=revision+1,definition_json=?,updated_at=? WHERE archived=0 AND rule_id=? AND revision=?`, name, enabled, priority, string(definition), now.Format(time.RFC3339Nano), ruleID, expectedRevision)
	if err != nil {
		return Rule{}, fmt.Errorf("update rule: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return Rule{}, fmt.Errorf("read updated rule count: %w", err)
	}
	if affected == 0 {
		return Rule{}, ErrConflict
	}
	return service.Get(ctx, ruleID)
}

func scanRule(scanner interface{ Scan(...any) error }) (Rule, error) {
	var rule Rule
	var siteID, definition, createdAt, updatedAt string
	var enabled int
	if err := scanner.Scan(&rule.ID, &siteID, &rule.Name, &enabled, &rule.Priority, &rule.Revision, &rule.State, &definition, &createdAt, &updatedAt); err != nil {
		return Rule{}, err
	}
	created, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return Rule{}, fmt.Errorf("parse created_at: %w", err)
	}
	updated, err := time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return Rule{}, fmt.Errorf("parse updated_at: %w", err)
	}
	if !json.Valid([]byte(definition)) {
		return Rule{}, errors.New("stored rule definition is invalid JSON")
	}
	rule.SiteID = siteID
	rule.Enabled = enabled == 1
	rule.Definition = json.RawMessage(definition)
	rule.CreatedAt = created
	rule.UpdatedAt = updated
	return rule, nil
}

func normalizeDefinition(value json.RawMessage) (json.RawMessage, error) {
	if len(value) == 0 {
		return json.RawMessage(`{}`), nil
	}
	var object map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(value)))
	if err := decoder.Decode(&object); err != nil || object == nil {
		return nil, errors.New("rule definition must be a JSON object")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("rule definition must contain one JSON value")
		}
		return nil, errors.New("rule definition has trailing data")
	}
	if _, ok := object["action_set_json"]; ok {
		if _, err := MatchFor(Rule{ID: "validate", Revision: 1, Definition: value}, "validate"); err != nil {
			return nil, fmt.Errorf("invalid rule actions: %w", err)
		}
	}
	return append(json.RawMessage(nil), value...), nil
}

func validateID(value, label string) error {
	if !idPattern.MatchString(value) {
		return fmt.Errorf("invalid %s", label)
	}
	return nil
}

func validateName(value string) error {
	if value == "" || len(value) > 200 {
		return errors.New("invalid rule name")
	}
	return nil
}

func nullableID(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func deterministicID(value string) string {
	digest := sha256.Sum256([]byte(value))
	return "rule-" + hex.EncodeToString(digest[:])
}
