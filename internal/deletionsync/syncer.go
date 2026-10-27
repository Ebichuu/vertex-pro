package deletionsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/vertex-pro/vertex-pro/internal/deletionpolicy"
	"github.com/vertex-pro/vertex-pro/internal/qui/control"
)

// QuiClient is the subset of the qui control client the syncer uses. It has
// no forced-execution call: qui's own scheduler runs live automations.
type QuiClient interface {
	Automations(context.Context, string) ([]control.Automation, error)
	SaveAutomation(context.Context, string, control.Automation) (control.Automation, error)
	PreviewDelete(context.Context, string, control.Automation, int, int, string) (control.PreviewResult, error)
}

// Error codes recorded on bindings.
const (
	CodeTampered        = "QUI_DELETION_POLICY_TAMPERED"
	CodeMissing         = "QUI_DELETION_POLICY_MISSING"
	CodeUnboundName     = "QUI_DELETION_POLICY_NAME_TAKEN"
	CodeReadback        = "QUI_DELETION_POLICY_READBACK_MISMATCH"
	CodeDuplicateName   = "QUI_DELETION_POLICY_DUPLICATE_NAME"
	CodeCompileFailed   = "DELETION_POLICY_COMPILE_FAILED"
	CodeDisableUnproven = "QUI_DELETION_POLICY_DISABLE_UNCONFIRMED"
)

// Syncer keeps one instance's qui automations equal to the compiled
// policies scoped to it.
type Syncer struct {
	Store    Store
	Client   QuiClient
	Instance string
}

// Outcome reports what reconciliation did for one policy role.
type Outcome struct {
	PolicyID string
	Role     deletionpolicy.Role
	Action   string // unchanged | saved | disabled | retired | error
	Code     string
	Live     bool
}

type bindingKey struct {
	policy string
	role   deletionpolicy.Role
}

// Reconcile converges qui to the desired automations. Any automation whose
// qui copy differs from what Vertex last wrote, has vanished, or whose name
// Vertex did not bind is rewritten disabled and in dry-run, and its policy
// is demoted: an operator must preview and activate it again.
func (syncer Syncer) Reconcile(ctx context.Context) ([]Outcome, error) {
	policies, err := syncer.Store.Policies(ctx, true)
	if err != nil {
		return nil, err
	}
	safetyRecord, err := syncer.Store.Safety(ctx)
	if err != nil {
		return nil, err
	}
	safety, err := safetyRecord.Spec.Safety()
	if err != nil {
		return nil, err
	}
	bindings, err := syncer.Store.Bindings(ctx, syncer.Instance)
	if err != nil {
		return nil, err
	}
	bound := map[bindingKey]Binding{}
	for _, binding := range bindings {
		bound[bindingKey{binding.PolicyID, binding.Role}] = binding
	}

	type desire struct {
		record     PolicyRecord
		automation control.Automation
	}
	desired := map[bindingKey]desire{}
	var outcomes []Outcome
	var failures []error
	for _, record := range policies {
		if record.Archived || !slices.Contains(record.Instances, syncer.Instance) {
			continue
		}
		policy, err := record.Policy()
		var compiled []deletionpolicy.Compiled
		if err == nil {
			compiled, err = deletionpolicy.Compile(policy, safety)
		}
		if err != nil {
			// A policy that no longer compiles must not keep running.
			_ = syncer.Store.Demote(ctx, record.ID)
			outcomes = append(outcomes, Outcome{PolicyID: record.ID, Action: "error", Code: CodeCompileFailed})
			failures = append(failures, fmt.Errorf("compile %s: %w", record.ID, err))
			continue
		}
		for _, item := range compiled {
			desired[bindingKey{record.ID, item.Role}] = desire{record: record, automation: item.Automation}
		}
	}

	remote, err := syncer.Client.Automations(ctx, syncer.Instance)
	if err != nil {
		return outcomes, err
	}
	keys := make([]bindingKey, 0, len(desired))
	for key := range desired {
		keys = append(keys, key)
	}
	slices.SortFunc(keys, compareKeys)
	for _, key := range keys {
		want := desired[key]
		outcome, err := syncer.ensure(ctx, key, want.record, safetyRecord.Revision, want.automation, bound, &remote)
		outcomes = append(outcomes, outcome)
		if err != nil {
			failures = append(failures, err)
		}
	}

	retired := make([]bindingKey, 0)
	for key := range bound {
		if _, keep := desired[key]; !keep {
			retired = append(retired, key)
		}
	}
	slices.SortFunc(retired, compareKeys)
	for _, key := range retired {
		outcome, err := syncer.retire(ctx, bound[key], &remote)
		outcomes = append(outcomes, outcome)
		if err != nil {
			failures = append(failures, err)
		}
	}
	return outcomes, errors.Join(failures...)
}

func compareKeys(a, b bindingKey) int {
	if a.policy != b.policy {
		if a.policy < b.policy {
			return -1
		}
		return 1
	}
	switch {
	case a.role < b.role:
		return -1
	case a.role > b.role:
		return 1
	}
	return 0
}

func (syncer Syncer) ensure(ctx context.Context, key bindingKey, record PolicyRecord, safetyRevision int, want control.Automation, bound map[bindingKey]Binding, remote *[]control.Automation) (Outcome, error) {
	outcome := Outcome{PolicyID: key.policy, Role: key.role}
	binding, hasBinding := bound[key]
	var current *control.Automation
	if hasBinding {
		current = findByID(*remote, binding.QuiID)
	} else {
		named := findByName(*remote, want.Name)
		if len(named) > 1 {
			outcome.Action, outcome.Code = "error", CodeDuplicateName
			_ = syncer.Store.Demote(ctx, record.ID)
			return outcome, fmt.Errorf("%s: %d qui automations named %s", record.ID, len(named), want.Name)
		}
		if len(named) == 1 {
			current = &named[0]
		}
	}

	code := ""
	switch {
	case hasBinding && current == nil:
		code = CodeMissing
	case !hasBinding && current != nil:
		code = CodeUnboundName
	case hasBinding && current != nil:
		var recorded control.Automation
		if json.Unmarshal([]byte(binding.Payload), &recorded) != nil || !sameManaged(*current, recorded) {
			code = CodeTampered
		}
	}
	if code != "" {
		// Fail closed: never let an automation Vertex cannot vouch for delete.
		want.Enabled, want.DryRun = false, true
		if err := syncer.Store.Demote(ctx, record.ID); err != nil {
			return outcome, err
		}
	}

	if current != nil && code == "" && sameManaged(*current, want) {
		outcome.Action, outcome.Live = "unchanged", want.Enabled && !want.DryRun
		return outcome, syncer.Store.PutBinding(ctx, Binding{Instance: syncer.Instance, PolicyID: key.policy, Role: key.role, QuiID: current.ID, PolicyRevision: record.Revision, SafetyRevision: safetyRevision, Live: outcome.Live, Payload: payload(*current)})
	}
	if current != nil {
		want.ID, want.SortOrder = current.ID, current.SortOrder
	}
	saved, err := syncer.Client.SaveAutomation(ctx, syncer.Instance, want)
	if err != nil {
		outcome.Action = "error"
		return outcome, fmt.Errorf("save %s: %w", want.Name, err)
	}
	// A mutation response is not proof of what qui persisted: read it back.
	list, err := syncer.Client.Automations(ctx, syncer.Instance)
	if err != nil {
		outcome.Action = "error"
		return outcome, fmt.Errorf("read back %s: %w", want.Name, err)
	}
	*remote = list
	readback := findByID(list, saved.ID)
	if readback == nil || !sameManaged(*readback, want) {
		code = CodeReadback
		// Record what was intended; the next pass sees the difference as
		// tampering and rewrites the automation disabled.
		_ = syncer.Store.Demote(ctx, record.ID)
		outcome.Action, outcome.Code = "error", code
		quiID := saved.ID
		if quiID <= 0 && readback != nil {
			quiID = readback.ID
		}
		if quiID > 0 {
			_ = syncer.Store.PutBinding(ctx, Binding{Instance: syncer.Instance, PolicyID: key.policy, Role: key.role, QuiID: quiID, PolicyRevision: record.Revision, SafetyRevision: safetyRevision, Payload: payload(want), LastError: code})
		}
		return outcome, fmt.Errorf("%s: qui readback does not match", want.Name)
	}
	outcome.Live = readback.Enabled && !readback.DryRun
	outcome.Action, outcome.Code = "saved", code
	if code != "" {
		outcome.Action = "disabled"
	}
	return outcome, syncer.Store.PutBinding(ctx, Binding{Instance: syncer.Instance, PolicyID: key.policy, Role: key.role, QuiID: readback.ID, PolicyRevision: record.Revision, SafetyRevision: safetyRevision, Live: outcome.Live, Payload: payload(*readback), LastError: code})
}

// retire disables an automation whose policy role is no longer desired on
// this instance. It never deletes the automation, so history stays readable.
func (syncer Syncer) retire(ctx context.Context, binding Binding, remote *[]control.Automation) (Outcome, error) {
	outcome := Outcome{PolicyID: binding.PolicyID, Role: binding.Role, Action: "retired"}
	current := findByID(*remote, binding.QuiID)
	if current == nil || (!current.Enabled && current.DryRun) {
		if current != nil {
			binding.Payload = payload(*current)
		}
		binding.Live, binding.LastError = false, ""
		return outcome, syncer.Store.PutBinding(ctx, binding)
	}
	disabled := *current
	disabled.Enabled, disabled.DryRun = false, true
	if _, err := syncer.Client.SaveAutomation(ctx, syncer.Instance, disabled); err != nil {
		outcome.Action = "error"
		return outcome, fmt.Errorf("disable %s: %w", current.Name, err)
	}
	list, err := syncer.Client.Automations(ctx, syncer.Instance)
	if err != nil {
		outcome.Action = "error"
		return outcome, err
	}
	*remote = list
	readback := findByID(list, binding.QuiID)
	if readback == nil || readback.Enabled || !readback.DryRun {
		outcome.Action, outcome.Code = "error", CodeDisableUnproven
		_ = syncer.Store.MarkBindingError(ctx, binding.Instance, binding.PolicyID, binding.Role, CodeDisableUnproven)
		return outcome, fmt.Errorf("disabling %s was not confirmed", current.Name)
	}
	binding.Live, binding.Payload, binding.LastError = false, payload(*readback), ""
	return outcome, syncer.Store.PutBinding(ctx, binding)
}

// sameManaged compares everything Vertex sets on an automation. qui-owned
// fields (sort order, timestamps, normalized tracker domains) are ignored.
func sameManaged(a, b control.Automation) bool {
	return a.Name == b.Name && a.TrackerPattern == b.TrackerPattern && a.Enabled == b.Enabled && a.DryRun == b.DryRun &&
		a.Notify == b.Notify && a.IntervalSeconds == b.IntervalSeconds &&
		canonical(a.Conditions) == canonical(b.Conditions) && canonical(a.FreeSpaceSource) == canonical(b.FreeSpaceSource) && canonical(a.SortingConfig) == canonical(b.SortingConfig)
}

// canonical renders a qui JSON object with sorted keys; empty means null.
func canonical(value map[string]any) string {
	if len(value) == 0 {
		return "null"
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "invalid"
	}
	return string(encoded)
}

func payload(automation control.Automation) string {
	encoded, _ := json.Marshal(automation)
	return string(encoded)
}

func findByID(list []control.Automation, id int) *control.Automation {
	for index := range list {
		if list[index].ID == id && id > 0 {
			return &list[index]
		}
	}
	return nil
}

func findByName(list []control.Automation, name string) []control.Automation {
	var result []control.Automation
	for _, automation := range list {
		if automation.Name == name {
			result = append(result, automation)
		}
	}
	return result
}

// PreviewRole is qui's preview of one compiled role.
type PreviewRole struct {
	Role         deletionpolicy.Role
	TotalMatches int
	Examples     []control.PreviewTorrent
}

// Preview asks qui what each role of the policy's current revision would
// delete on this instance, and records the evidence activation requires.
// It never saves or runs an automation.
func (syncer Syncer) Preview(ctx context.Context, record PolicyRecord, safetyRecord SafetyRecord, limit int) ([]PreviewRole, error) {
	if record.Archived || !slices.Contains(record.Instances, syncer.Instance) {
		return nil, fmt.Errorf("policy %s does not target instance %s", record.ID, syncer.Instance)
	}
	policy, err := record.Spec.Policy(record.ID, record.Name, record.Revision, false)
	if err != nil {
		return nil, err
	}
	safety, err := safetyRecord.Spec.Safety()
	if err != nil {
		return nil, err
	}
	compiled, err := deletionpolicy.Compile(policy, safety)
	if err != nil {
		return nil, err
	}
	result := make([]PreviewRole, 0, len(compiled))
	total := 0
	for _, item := range compiled {
		preview, err := syncer.Client.PreviewDelete(ctx, syncer.Instance, item.Automation, limit, 0, control.PreviewViewNeeded)
		if err != nil {
			return nil, fmt.Errorf("preview %s: %w", item.Role, err)
		}
		total += preview.TotalMatches
		result = append(result, PreviewRole{Role: item.Role, TotalMatches: preview.TotalMatches, Examples: preview.Examples})
	}
	if err := syncer.Store.RecordPreview(ctx, record.ID, record.Revision, safetyRecord.Revision, syncer.Instance, total); err != nil {
		return nil, err
	}
	return result, nil
}
