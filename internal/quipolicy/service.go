package quipolicy

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/qui/control"
	"github.com/vertex-pro/vertex-pro/internal/ruleaggregate"
)

var ErrPolicyInvalid = errors.New("managed policy is not valid")

type Client interface {
	Automations(context.Context, string) ([]control.Automation, error)
	SaveAutomation(context.Context, string, control.Automation) (control.Automation, error)
}
type Service struct {
	DB       *sql.DB
	Client   Client
	Instance string
	Now      func() time.Time
}

func (s Service) Ensure(ctx context.Context, m ruleaggregate.Match) (string, error) {
	if m.SeedTimeSeconds <= 0 {
		return "", nil
	}
	if m.DeleteMode != "TORRENT_ONLY" && m.DeleteMode != "WITH_FILES" {
		return "", fmt.Errorf("%w: invalid delete mode", ErrPolicyInvalid)
	}
	if m.RuleID == "" || m.RuleRevision <= 0 || s.DB == nil || s.Client == nil || s.Now == nil {
		return "", fmt.Errorf("%w: invalid qui policy identity or dependencies", ErrPolicyInvalid)
	}
	var active int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM rules r LEFT JOIN sites site ON site.site_id=r.site_id WHERE r.rule_id=? AND r.revision=? AND r.enabled=1 AND r.archived=0 AND (site.site_id IS NULL OR (site.enabled=1 AND site.archived=0))`, m.RuleID, m.RuleRevision).Scan(&active); err != nil {
		return "", err
	}
	if active != 1 {
		return "", fmt.Errorf("%w: rule is disabled or unavailable", ErrPolicyInvalid)
	}
	var token string
	if err := s.DB.QueryRowContext(ctx, `SELECT token FROM installation_identity WHERE id=1`).Scan(&token); err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(token + "/" + m.RuleID + "/" + strconv.Itoa(m.RuleRevision)))
	tag := fmt.Sprintf("vertex-policy-%x", digest[:16])
	mode := "delete"
	if m.DeleteMode == "WITH_FILES" {
		mode = "deleteWithFiles"
	}
	desired := control.Automation{Name: tag, TrackerPattern: "*", Enabled: true, IntervalSeconds: 60, Conditions: map[string]any{"schemaVersion": "1", "delete": map[string]any{"enabled": true, "mode": mode, "condition": map[string]any{"operator": "AND", "conditions": []any{map[string]any{"field": "TAGS", "operator": "EQUAL", "value": tag}, map[string]any{"field": "SEEDING_TIME", "operator": "GREATER_THAN", "value": strconv.FormatInt(m.SeedTimeSeconds, 10)}}}}}}
	var boundID, boundEnabled int
	var boundTag, boundJSON string
	bindErr := s.DB.QueryRowContext(ctx, `SELECT qui_id,tag,enabled,payload_json FROM qui_policy_bindings WHERE instance_id=? AND rule_id=? AND rule_revision=?`, s.Instance, m.RuleID, m.RuleRevision).Scan(&boundID, &boundTag, &boundEnabled, &boundJSON)
	if bindErr != nil && !errors.Is(bindErr, sql.ErrNoRows) {
		return "", bindErr
	}
	if bindErr == nil && boundTag != tag {
		return "", fmt.Errorf("%w: managed qui policy binding changed", ErrPolicyInvalid)
	}
	var bound control.Automation
	if bindErr == nil {
		if err := json.Unmarshal([]byte(boundJSON), &bound); err != nil {
			return "", fmt.Errorf("%w: managed qui policy binding is invalid", ErrPolicyInvalid)
		}
	}
	list, err := s.Client.Automations(ctx, s.Instance)
	if err != nil {
		return "", err
	}
	count := 0
	boundFound := false
	alreadyEnabled := false
	for _, existing := range list {
		if boundID > 0 && existing.ID == boundID {
			boundFound = true
			if existing.Name != tag {
				return "", fmt.Errorf("%w: managed qui policy renamed externally", ErrPolicyInvalid)
			}
		}
		if existing.Name == tag {
			count++
			if boundID > 0 && existing.ID != boundID {
				return "", fmt.Errorf("%w: managed qui policy identity changed externally", ErrPolicyInvalid)
			}
			if boundID > 0 && !sameManagedSettings(existing, bound) {
				return "", fmt.Errorf("%w: managed qui policy changed externally", ErrPolicyInvalid)
			}
			if !sameManagedSettings(existing, desired) {
				return "", fmt.Errorf("%w: managed qui policy changed externally", ErrPolicyInvalid)
			}
			if !existing.Enabled {
				if bindErr != nil || boundEnabled != 0 {
					return "", fmt.Errorf("%w: managed qui policy disabled externally", ErrPolicyInvalid)
				}
			}
			desired.ID = existing.ID
			desired.SortOrder = existing.SortOrder
			alreadyEnabled = existing.Enabled
		}
	}
	if boundID > 0 && !boundFound {
		return "", fmt.Errorf("%w: managed qui policy missing externally", ErrPolicyInvalid)
	}
	if count > 1 {
		return "", fmt.Errorf("%w: duplicate managed qui policy", ErrPolicyInvalid)
	}
	if !alreadyEnabled {
		if _, err := s.Client.SaveAutomation(ctx, s.Instance, desired); err != nil {
			return "", err
		}
		// A mutation response is not proof that the intended rule persisted.
		list, err = s.Client.Automations(ctx, s.Instance)
		if err != nil {
			return "", err
		}
	}
	var saved *control.Automation
	for i := range list {
		if list[i].Name == tag {
			if saved != nil {
				return "", fmt.Errorf("%w: duplicate managed qui policy", ErrPolicyInvalid)
			}
			saved = &list[i]
		}
	}
	if saved == nil || saved.ID <= 0 || saved.ID != desired.ID && desired.ID > 0 || !saved.Enabled || !sameManagedSettings(*saved, desired) {
		return "", fmt.Errorf("%w: qui policy readback mismatch", ErrPolicyInvalid)
	}
	raw, _ := json.Marshal(saved)
	_, err = s.DB.ExecContext(ctx, `INSERT INTO qui_policy_bindings(instance_id,rule_id,rule_revision,qui_id,tag,payload_json,enabled,updated_at) VALUES(?,?,?,?,?,?,1,?) ON CONFLICT(instance_id,rule_id,rule_revision) DO UPDATE SET qui_id=excluded.qui_id,payload_json=excluded.payload_json,enabled=1,last_error_code='',updated_at=excluded.updated_at`, s.Instance, m.RuleID, m.RuleRevision, saved.ID, tag, string(raw), s.Now().UTC().Format(time.RFC3339Nano))
	return tag, err
}
func sameManagedSettings(a, b control.Automation) bool {
	left, _ := json.Marshal(a.Conditions)
	right, _ := json.Marshal(b.Conditions)
	freeLeft, _ := json.Marshal(a.FreeSpaceSource)
	freeRight, _ := json.Marshal(b.FreeSpaceSource)
	sortLeft, _ := json.Marshal(a.SortingConfig)
	sortRight, _ := json.Marshal(b.SortingConfig)
	return a.Name == b.Name && a.TrackerPattern == b.TrackerPattern && a.IntervalSeconds == b.IntervalSeconds && a.DryRun == b.DryRun && a.Notify == b.Notify && (b.SortOrder == 0 || a.SortOrder == b.SortOrder) && string(left) == string(right) && string(freeLeft) == string(freeRight) && string(sortLeft) == string(sortRight)
}

// Disabling or archiving a Vertex rule disables its managed qui policies.
// Existing policies are never re-enabled merely by a monitoring tick.
func (s Service) SyncDisabled(ctx context.Context) error {
	rows, err := s.DB.QueryContext(ctx, `SELECT b.rule_id,b.rule_revision,b.payload_json FROM qui_policy_bindings b JOIN rules r ON r.rule_id=b.rule_id LEFT JOIN sites site ON site.site_id=r.site_id WHERE b.instance_id=? AND b.enabled=1 AND (r.enabled=0 OR r.archived=1 OR site.enabled=0 OR site.archived=1)`, s.Instance)
	if err != nil {
		return err
	}
	type item struct {
		id  string
		rev int
		raw string
	}
	var list []item
	for rows.Next() {
		var v item
		if err := rows.Scan(&v.id, &v.rev, &v.raw); err != nil {
			rows.Close()
			return err
		}
		list = append(list, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, v := range list {
		fail := func(code string, cause error) error {
			_, _ = s.DB.ExecContext(ctx, `UPDATE qui_policy_bindings SET last_error_code=? WHERE instance_id=? AND rule_id=? AND rule_revision=?`, code, s.Instance, v.id, v.rev)
			return cause
		}
		var a control.Automation
		if err := json.Unmarshal([]byte(v.raw), &a); err != nil {
			return fail("QUI_POLICY_BINDING_INVALID", err)
		}
		remote, err := s.Client.Automations(ctx, s.Instance)
		if err != nil {
			return fail("QUI_POLICY_DISABLE_FAILED", err)
		}
		var current *control.Automation
		for i := range remote {
			if remote[i].ID == a.ID {
				if current != nil {
					return fail("QUI_POLICY_CONFLICT", fmt.Errorf("duplicate qui automation ID"))
				}
				current = &remote[i]
			}
		}
		if current == nil || !sameManagedSettings(*current, a) {
			return fail("QUI_POLICY_CONFLICT", fmt.Errorf("%w: managed qui policy changed externally", ErrPolicyInvalid))
		}
		if current.Enabled {
			a.Enabled = false
			a.SortOrder = current.SortOrder
			if _, err := s.Client.SaveAutomation(ctx, s.Instance, a); err != nil {
				return fail("QUI_POLICY_DISABLE_FAILED", err)
			}
			remote, err = s.Client.Automations(ctx, s.Instance)
			if err != nil {
				return fail("QUI_POLICY_DISABLE_FAILED", err)
			}
			current = nil
			for i := range remote {
				if remote[i].ID == a.ID {
					if current != nil {
						return fail("QUI_POLICY_CONFLICT", fmt.Errorf("duplicate qui automation ID"))
					}
					current = &remote[i]
				}
			}
			if current == nil || current.Enabled || !sameManagedSettings(*current, a) {
				return fail("QUI_POLICY_DISABLE_FAILED", fmt.Errorf("qui policy disable not confirmed"))
			}
		}
		currentJSON, err := json.Marshal(current)
		if err != nil {
			return err
		}
		if _, err := s.DB.ExecContext(ctx, `UPDATE qui_policy_bindings SET enabled=0,payload_json=?,last_error_code='',updated_at=? WHERE instance_id=? AND rule_id=? AND rule_revision=?`, string(currentJSON), s.Now().UTC().Format(time.RFC3339Nano), s.Instance, v.id, v.rev); err != nil {
			return err
		}
	}
	return nil
}
