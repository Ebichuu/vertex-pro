// Package deletionsync persists deletion policies, keeps their compiled qui
// automations in sync, records previews and gates activation on them, and
// backfills Vertex ownership tags on torrents Vertex provably added.
package deletionsync

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
	"github.com/vertex-pro/vertex-pro/internal/deletionpolicy"
)

var (
	ErrNotFound = errors.New("deletion policy not found")
	ErrConflict = errors.New("deletion policy revision conflict")
	// ErrNotPreviewed blocks activation without fresh preview evidence.
	ErrNotPreviewed = errors.New("deletion policy has no current preview on every instance")
)

// PreviewValidity bounds how old preview evidence may be at activation.
const PreviewValidity = 24 * time.Hour

var instancePattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

type Store struct {
	DB  *sql.DB
	Now func() time.Time
}

type PolicyRecord struct {
	ID        string
	Name      string
	Revision  int
	Spec      deletionpolicy.PolicySpec
	Instances []string
	Live      bool
	Archived  bool
	UpdatedAt time.Time
}

type SafetyRecord struct {
	Revision int
	Spec     deletionpolicy.SafetySpec
}

// Policy returns the compilable policy of this record.
func (record PolicyRecord) Policy() (deletionpolicy.Policy, error) {
	return record.Spec.Policy(record.ID, record.Name, record.Revision, record.Live && !record.Archived)
}

func (store Store) stamp() string { return store.Now().UTC().Format(time.RFC3339Nano) }

func validateInstances(instances []string) ([]string, error) {
	if len(instances) == 0 {
		return nil, fmt.Errorf("%w: at least one instance is required", deletionpolicy.ErrInvalid)
	}
	result := slices.Clone(instances)
	slices.Sort(result)
	result = slices.Compact(result)
	for _, instance := range result {
		if !instancePattern.MatchString(instance) {
			return nil, fmt.Errorf("%w: invalid instance %q", deletionpolicy.ErrInvalid, instance)
		}
	}
	return result, nil
}

// validate proves the spec compiles under the current safety settings, so
// a stored policy can always be synced.
func validate(id, name string, revision int, spec deletionpolicy.PolicySpec, safety deletionpolicy.SafetySpec) error {
	policy, err := spec.Policy(id, strings.TrimSpace(name), revision, false)
	if err != nil {
		return err
	}
	resolved, err := safety.Safety()
	if err != nil {
		return err
	}
	_, err = deletionpolicy.Compile(policy, resolved)
	return err
}

// CreatePolicy stores a new policy. New policies are never live.
func (store Store) CreatePolicy(ctx context.Context, id, name string, spec deletionpolicy.PolicySpec, instances []string) (PolicyRecord, error) {
	instances, err := validateInstances(instances)
	if err != nil {
		return PolicyRecord{}, err
	}
	safety, err := store.Safety(ctx)
	if err != nil {
		return PolicyRecord{}, err
	}
	if err := validate(id, name, 1, spec, safety.Spec); err != nil {
		return PolicyRecord{}, err
	}
	specJSON, _ := json.Marshal(spec)
	instancesJSON, _ := json.Marshal(instances)
	now := store.stamp()
	if _, err := store.DB.ExecContext(ctx, `INSERT INTO deletion_policies(policy_id,name,revision,spec_json,instance_ids_json,live,archived,created_at,updated_at) VALUES(?,?,1,?,?,0,0,?,?)`, id, strings.TrimSpace(name), string(specJSON), string(instancesJSON), now, now); err != nil {
		if strings.Contains(strings.ToUpper(err.Error()), "UNIQUE") {
			return PolicyRecord{}, ErrConflict
		}
		return PolicyRecord{}, err
	}
	return store.Policy(ctx, id)
}

// UpdatePolicy replaces a policy body. The new revision is never live.
func (store Store) UpdatePolicy(ctx context.Context, id string, expectedRevision int, name string, spec deletionpolicy.PolicySpec, instances []string) (PolicyRecord, error) {
	instances, err := validateInstances(instances)
	if err != nil {
		return PolicyRecord{}, err
	}
	safety, err := store.Safety(ctx)
	if err != nil {
		return PolicyRecord{}, err
	}
	if err := validate(id, name, expectedRevision+1, spec, safety.Spec); err != nil {
		return PolicyRecord{}, err
	}
	specJSON, _ := json.Marshal(spec)
	instancesJSON, _ := json.Marshal(instances)
	result, err := store.DB.ExecContext(ctx, `UPDATE deletion_policies SET name=?,spec_json=?,instance_ids_json=?,revision=revision+1,live=0,updated_at=? WHERE policy_id=? AND revision=? AND archived=0`, strings.TrimSpace(name), string(specJSON), string(instancesJSON), store.stamp(), id, expectedRevision)
	if err != nil {
		return PolicyRecord{}, err
	}
	if err := store.requireOne(ctx, result, id); err != nil {
		return PolicyRecord{}, err
	}
	return store.Policy(ctx, id)
}

// ArchivePolicy retires a policy; its automations are disabled on sync.
func (store Store) ArchivePolicy(ctx context.Context, id string, expectedRevision int) error {
	result, err := store.DB.ExecContext(ctx, `UPDATE deletion_policies SET archived=1,live=0,revision=revision+1,updated_at=? WHERE policy_id=? AND revision=? AND archived=0`, store.stamp(), id, expectedRevision)
	if err != nil {
		return err
	}
	return store.requireOne(ctx, result, id)
}

// Deactivate returns a policy to dry-run without changing its revision.
func (store Store) Deactivate(ctx context.Context, id string) error {
	result, err := store.DB.ExecContext(ctx, `UPDATE deletion_policies SET live=0,updated_at=? WHERE policy_id=? AND archived=0`, store.stamp(), id)
	if err != nil {
		return err
	}
	return store.requireOne(ctx, result, id)
}

// Activate makes exactly expectedRevision live, but only when it was
// previewed under the current safety revision on every target instance
// within PreviewValidity.
func (store Store) Activate(ctx context.Context, id string, expectedRevision int) error {
	return db.RunImmediate(ctx, store.DB, func(ctx context.Context, tx *db.ImmediateTx) error {
		var revision int
		var instancesJSON string
		var archived bool
		err := tx.QueryRowContext(ctx, `SELECT revision,instance_ids_json,archived FROM deletion_policies WHERE policy_id=?`, id).Scan(&revision, &instancesJSON, &archived)
		if errors.Is(err, sql.ErrNoRows) || err == nil && archived {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if revision != expectedRevision {
			return ErrConflict
		}
		var safetyRevision int
		if err := tx.QueryRowContext(ctx, `SELECT revision FROM deletion_safety WHERE id=1`).Scan(&safetyRevision); err != nil {
			return err
		}
		var instances []string
		if err := json.Unmarshal([]byte(instancesJSON), &instances); err != nil {
			return err
		}
		for _, instance := range instances {
			var previewed string
			err := tx.QueryRowContext(ctx, `SELECT previewed_at FROM deletion_previews WHERE policy_id=? AND policy_revision=? AND safety_revision=? AND instance_id=?`, id, revision, safetyRevision, instance).Scan(&previewed)
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%w: instance %s", ErrNotPreviewed, instance)
			}
			if err != nil {
				return err
			}
			if at, err := time.Parse(time.RFC3339Nano, previewed); err != nil || at.Before(store.Now().Add(-PreviewValidity)) {
				return fmt.Errorf("%w: instance %s preview is older than %s", ErrNotPreviewed, instance, PreviewValidity)
			}
		}
		_, err = tx.ExecContext(ctx, `UPDATE deletion_policies SET live=1,updated_at=? WHERE policy_id=? AND revision=?`, store.stamp(), id, revision)
		return err
	})
}

// RecordPreview stores preview evidence for one policy/safety revision.
func (store Store) RecordPreview(ctx context.Context, id string, policyRevision, safetyRevision int, instance string, totalMatches int) error {
	_, err := store.DB.ExecContext(ctx, `INSERT INTO deletion_previews(policy_id,policy_revision,safety_revision,instance_id,total_matches,previewed_at) VALUES(?,?,?,?,?,?)
		ON CONFLICT(policy_id,policy_revision,safety_revision,instance_id) DO UPDATE SET total_matches=excluded.total_matches,previewed_at=excluded.previewed_at`, id, policyRevision, safetyRevision, instance, totalMatches, store.stamp())
	return err
}

// Demote returns a policy to dry-run after its qui automation was found
// changed or missing; an operator must preview and activate it again.
func (store Store) Demote(ctx context.Context, id string) error {
	_, err := store.DB.ExecContext(ctx, `UPDATE deletion_policies SET live=0,updated_at=? WHERE policy_id=? AND live=1`, store.stamp(), id)
	return err
}

func (store Store) requireOne(ctx context.Context, result sql.Result, id string) error {
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 1 {
		return nil
	}
	var exists int
	if err := store.DB.QueryRowContext(ctx, `SELECT count(*) FROM deletion_policies WHERE policy_id=? AND archived=0`, id).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return ErrNotFound
	}
	return ErrConflict
}

const policyColumns = `policy_id,name,revision,spec_json,instance_ids_json,live,archived,updated_at`

func scanPolicy(scanner interface{ Scan(...any) error }) (PolicyRecord, error) {
	var record PolicyRecord
	var specJSON, instancesJSON, updated string
	if err := scanner.Scan(&record.ID, &record.Name, &record.Revision, &specJSON, &instancesJSON, &record.Live, &record.Archived, &updated); err != nil {
		return PolicyRecord{}, err
	}
	spec, err := deletionpolicy.DecodePolicySpec([]byte(specJSON))
	if err != nil {
		return PolicyRecord{}, err
	}
	record.Spec = spec
	if err := json.Unmarshal([]byte(instancesJSON), &record.Instances); err != nil {
		return PolicyRecord{}, err
	}
	record.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	return record, nil
}

func (store Store) Policy(ctx context.Context, id string) (PolicyRecord, error) {
	record, err := scanPolicy(store.DB.QueryRowContext(ctx, `SELECT `+policyColumns+` FROM deletion_policies WHERE policy_id=? AND archived=0`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return PolicyRecord{}, ErrNotFound
	}
	return record, err
}

// Policies lists policies; includeArchived also returns retired ones, which
// the syncer needs in order to disable their automations.
func (store Store) Policies(ctx context.Context, includeArchived bool) ([]PolicyRecord, error) {
	query := `SELECT ` + policyColumns + ` FROM deletion_policies`
	if !includeArchived {
		query += ` WHERE archived=0`
	}
	rows, err := store.DB.QueryContext(ctx, query+` ORDER BY policy_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []PolicyRecord
	for rows.Next() {
		record, err := scanPolicy(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func (store Store) Safety(ctx context.Context) (SafetyRecord, error) {
	var record SafetyRecord
	var specJSON string
	if err := store.DB.QueryRowContext(ctx, `SELECT revision,spec_json FROM deletion_safety WHERE id=1`).Scan(&record.Revision, &specJSON); err != nil {
		return SafetyRecord{}, err
	}
	spec, err := deletionpolicy.DecodeSafetySpec([]byte(specJSON))
	if err != nil {
		return SafetyRecord{}, err
	}
	record.Spec = spec
	return record, nil
}

// UpdateSafety replaces the safety settings and returns every live policy to
// dry-run: what a policy may delete changed, so it must be previewed again.
// It refuses settings under which an existing policy would not compile.
func (store Store) UpdateSafety(ctx context.Context, expectedRevision int, spec deletionpolicy.SafetySpec) (SafetyRecord, error) {
	if _, err := spec.Safety(); err != nil {
		return SafetyRecord{}, err
	}
	policies, err := store.Policies(ctx, false)
	if err != nil {
		return SafetyRecord{}, err
	}
	for _, policy := range policies {
		if err := validate(policy.ID, policy.Name, policy.Revision, policy.Spec, spec); err != nil {
			return SafetyRecord{}, fmt.Errorf("policy %s: %w", policy.ID, err)
		}
	}
	specJSON, _ := json.Marshal(spec)
	err = db.RunImmediate(ctx, store.DB, func(ctx context.Context, tx *db.ImmediateTx) error {
		result, err := tx.ExecContext(ctx, `UPDATE deletion_safety SET revision=revision+1,spec_json=?,updated_at=? WHERE id=1 AND revision=?`, string(specJSON), store.stamp(), expectedRevision)
		if err != nil {
			return err
		}
		if count, err := result.RowsAffected(); err != nil || count != 1 {
			return errors.Join(err, ErrConflict)
		}
		_, err = tx.ExecContext(ctx, `UPDATE deletion_policies SET live=0,updated_at=? WHERE live=1`, store.stamp())
		return err
	})
	if err != nil {
		return SafetyRecord{}, err
	}
	return store.Safety(ctx)
}

// HasWork reports whether any policy or binding exists, so the background
// reconciler makes no qui calls on installations without deletion policies.
func (store Store) HasWork(ctx context.Context) (bool, error) {
	var count int
	err := store.DB.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM deletion_policies) + (SELECT count(*) FROM deletion_policy_bindings)`).Scan(&count)
	return count > 0, err
}

// Binding is the qui automation Vertex owns for one policy role.
type Binding struct {
	Instance       string
	PolicyID       string
	Role           deletionpolicy.Role
	QuiID          int
	PolicyRevision int
	SafetyRevision int
	Live           bool
	Payload        string
	LastError      string
}

func (store Store) Bindings(ctx context.Context, instance string) ([]Binding, error) {
	rows, err := store.DB.QueryContext(ctx, `SELECT instance_id,policy_id,role,qui_id,policy_revision,safety_revision,live,payload_json,last_error_code FROM deletion_policy_bindings WHERE instance_id=? ORDER BY policy_id,role`, instance)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Binding
	for rows.Next() {
		var binding Binding
		if err := rows.Scan(&binding.Instance, &binding.PolicyID, &binding.Role, &binding.QuiID, &binding.PolicyRevision, &binding.SafetyRevision, &binding.Live, &binding.Payload, &binding.LastError); err != nil {
			return nil, err
		}
		result = append(result, binding)
	}
	return result, rows.Err()
}

func (store Store) PutBinding(ctx context.Context, binding Binding) error {
	_, err := store.DB.ExecContext(ctx, `INSERT INTO deletion_policy_bindings(instance_id,policy_id,role,qui_id,policy_revision,safety_revision,live,payload_json,last_error_code,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(instance_id,policy_id,role) DO UPDATE SET qui_id=excluded.qui_id,policy_revision=excluded.policy_revision,safety_revision=excluded.safety_revision,live=excluded.live,payload_json=excluded.payload_json,last_error_code=excluded.last_error_code,updated_at=excluded.updated_at`,
		binding.Instance, binding.PolicyID, string(binding.Role), binding.QuiID, binding.PolicyRevision, binding.SafetyRevision, binding.Live, binding.Payload, binding.LastError, store.stamp())
	return err
}

func (store Store) MarkBindingError(ctx context.Context, instance, policyID string, role deletionpolicy.Role, code string) error {
	_, err := store.DB.ExecContext(ctx, `UPDATE deletion_policy_bindings SET last_error_code=?,updated_at=? WHERE instance_id=? AND policy_id=? AND role=?`, code, store.stamp(), instance, policyID, string(role))
	return err
}
