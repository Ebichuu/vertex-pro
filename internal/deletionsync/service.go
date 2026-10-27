package deletionsync

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/vertex-pro/vertex-pro/internal/syncmerge"
)

// ErrInstanceUnavailable means an instance in a policy's scope has no qui
// connection in this process.
var ErrInstanceUnavailable = errors.New("instance is not connected")

// Instance is one qui-connected instance.
type Instance struct {
	Client   QuiClient
	Tags     TagWriter
	Snapshot func() (syncmerge.Snapshot, bool)
	Activity ActivityReader
}

// Service is the deletion application service used by the API and the
// background reconciler. It never deletes a torrent itself: qui does, and
// only for automations an operator activated after a preview.
type Service struct {
	Store     Store
	Instances map[string]Instance
}

func (service Service) syncer(instance string) (Syncer, error) {
	connected, ok := service.Instances[instance]
	if !ok || connected.Client == nil {
		return Syncer{}, fmt.Errorf("%w: %s", ErrInstanceUnavailable, instance)
	}
	return Syncer{Store: service.Store, Client: connected.Client, Instance: instance}, nil
}

// Reconcile converges one instance.
func (service Service) Reconcile(ctx context.Context, instance string) ([]Outcome, error) {
	syncer, err := service.syncer(instance)
	if err != nil {
		return nil, err
	}
	return syncer.Reconcile(ctx)
}

// ReconcileAll converges every connected instance, sorted for determinism.
func (service Service) ReconcileAll(ctx context.Context) error {
	instances := make([]string, 0, len(service.Instances))
	for instance := range service.Instances {
		instances = append(instances, instance)
	}
	slices.Sort(instances)
	var failures []error
	for _, instance := range instances {
		if _, err := service.Reconcile(ctx, instance); err != nil {
			failures = append(failures, fmt.Errorf("instance %s: %w", instance, err))
		}
	}
	return errors.Join(failures...)
}

// Preview previews the policy's current revision on every instance in its
// scope and records the evidence Activate requires.
func (service Service) Preview(ctx context.Context, id string, limit int) (map[string][]PreviewRole, error) {
	record, err := service.Store.Policy(ctx, id)
	if err != nil {
		return nil, err
	}
	safety, err := service.Store.Safety(ctx)
	if err != nil {
		return nil, err
	}
	result := make(map[string][]PreviewRole, len(record.Instances))
	for _, instance := range record.Instances {
		syncer, err := service.syncer(instance)
		if err != nil {
			return nil, err
		}
		roles, err := syncer.Preview(ctx, record, safety, limit)
		if err != nil {
			return nil, fmt.Errorf("instance %s: %w", instance, err)
		}
		result[instance] = roles
	}
	return result, nil
}

// Activate makes a previewed revision live and pushes it to qui. A failed
// push leaves the policy live in the database; the next reconcile retries,
// and the binding records the error meanwhile.
func (service Service) Activate(ctx context.Context, id string, revision int) error {
	if err := service.Store.Activate(ctx, id, revision); err != nil {
		return err
	}
	return service.reconcileScope(ctx, id)
}

// Deactivate returns a policy to dry-run and pushes that to qui.
func (service Service) Deactivate(ctx context.Context, id string) error {
	if err := service.Store.Deactivate(ctx, id); err != nil {
		return err
	}
	return service.reconcileScope(ctx, id)
}

func (service Service) reconcileScope(ctx context.Context, id string) error {
	record, err := service.Store.Policy(ctx, id)
	if err != nil {
		return err
	}
	var failures []error
	for _, instance := range record.Instances {
		if _, err := service.Reconcile(ctx, instance); err != nil {
			failures = append(failures, fmt.Errorf("instance %s: %w", instance, err))
		}
	}
	return errors.Join(failures...)
}

// ImportAudit imports one instance's qui deletion activity.
func (service Service) ImportAudit(ctx context.Context, instance string) (ImportResult, error) {
	connected, ok := service.Instances[instance]
	if !ok || connected.Activity == nil {
		return ImportResult{}, fmt.Errorf("%w: %s", ErrInstanceUnavailable, instance)
	}
	return AuditImporter{Store: service.Store, Client: connected.Activity, Instance: instance}.Import(ctx)
}

// ImportAuditAll imports every connected instance; gaps is the number of
// instances whose import may have missed older activity.
func (service Service) ImportAuditAll(ctx context.Context) (gaps int, err error) {
	instances := make([]string, 0, len(service.Instances))
	for instance := range service.Instances {
		instances = append(instances, instance)
	}
	slices.Sort(instances)
	var failures []error
	for _, instance := range instances {
		result, err := service.ImportAudit(ctx, instance)
		if err != nil {
			failures = append(failures, fmt.Errorf("instance %s: %w", instance, err))
			continue
		}
		if result.Gap {
			gaps++
		}
	}
	return gaps, errors.Join(failures...)
}

func (service Service) backfill(instance string) (Backfill, error) {
	connected, ok := service.Instances[instance]
	if !ok || connected.Tags == nil || connected.Snapshot == nil {
		return Backfill{}, fmt.Errorf("%w: %s", ErrInstanceUnavailable, instance)
	}
	return Backfill{DB: service.Store.DB, Instance: instance, Tags: connected.Tags, Snapshot: connected.Snapshot}, nil
}

// BackfillPlan lists torrents on instance that need ownership tags.
func (service Service) BackfillPlan(ctx context.Context, instance string) ([]BackfillItem, error) {
	backfill, err := service.backfill(instance)
	if err != nil {
		return nil, err
	}
	return backfill.Plan(ctx)
}

// BackfillApply re-plans and applies, so it only acts on the current state.
func (service Service) BackfillApply(ctx context.Context, instance string) (int, error) {
	backfill, err := service.backfill(instance)
	if err != nil {
		return 0, err
	}
	plan, err := backfill.Plan(ctx)
	if err != nil {
		return 0, err
	}
	return backfill.Apply(ctx, plan)
}
