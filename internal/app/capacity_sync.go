package app

import (
	"context"
	"errors"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/dispatchstage"
	"github.com/vertex-pro/vertex-pro/internal/snapshot"
	"github.com/vertex-pro/vertex-pro/internal/syncmerge"
)

// Callers hold syncMu, or run before workers start. observedAt is the start
// of the successful sync, not the time the cached evidence is subsequently read.
func (r *Runtime) refreshCapacityAt(ctx context.Context, observedAt time.Time) error {
	evidence, err := r.readCapacityAt(ctx, observedAt)
	if err != nil || evidence == nil {
		return err
	}
	r.storeObservation(evidence)
	return r.dispatchStage.RefreshCapacity(ctx, *evidence)
}

func (r *Runtime) readCapacityAt(ctx context.Context, observedAt time.Time) (*dispatchstage.Evidence, error) {
	if r.dispatchStage == nil {
		return nil, nil
	}
	preferences, err := r.proxyClient.Preferences(ctx)
	if err != nil {
		return nil, err
	}
	return r.capacityFromPreferences(ctx, observedAt, preferences)
}

func (r *Runtime) capacityFromPreferences(ctx context.Context, observedAt time.Time, preferences map[string]any) (*dispatchstage.Evidence, error) {
	if r.dispatchStage == nil {
		return nil, nil
	}
	current, record, err := r.currentSnapshot()
	if err != nil {
		return nil, err
	}
	evidence := dispatchstage.Evidence{Snapshot: current, Record: record, Preferences: preferences, CollectedAt: observedAt}
	return &evidence, nil
}

// currentSnapshot returns the watcher's last committed snapshot. Revisions are
// memory-only; readers still compare Record against sync_instance_state.
func (r *Runtime) currentSnapshot() (syncmerge.Snapshot, snapshot.Record, error) {
	if r.watcher == nil {
		return syncmerge.Snapshot{}, snapshot.Record{}, errors.New("qui sync is not enabled")
	}
	current, record, ok := r.watcher.Current()
	if !ok {
		return syncmerge.Snapshot{}, snapshot.Record{}, errors.New("complete snapshot unavailable")
	}
	return current, record, nil
}

// currentSnapshotFor serves API readers across the root and child runtimes.
func (r *Runtime) currentSnapshotFor(instanceID string) (syncmerge.Snapshot, snapshot.Record, bool) {
	for _, w := range append([]*Runtime{r}, r.children...) {
		if w.instanceID == instanceID {
			current, record, err := w.currentSnapshot()
			return current, record, err == nil
		}
	}
	return syncmerge.Snapshot{}, snapshot.Record{}, false
}
