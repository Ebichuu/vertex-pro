package app

import (
	"context"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/dispatchstage"
	"github.com/vertex-pro/vertex-pro/internal/snapshot"
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
	store, err := snapshot.New(r.Database, r.snapshotRoot)
	if err != nil {
		return nil, err
	}
	current, record, err := store.LoadLatest(ctx, r.instanceID)
	if err != nil {
		return nil, err
	}
	evidence := dispatchstage.Evidence{Snapshot: current, Record: record, Preferences: preferences, CollectedAt: observedAt}
	return &evidence, nil
}
