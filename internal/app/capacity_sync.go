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
	if r.dispatchStage == nil {
		return nil
	}
	preferences, err := r.proxyClient.Preferences(ctx)
	if err != nil {
		return err
	}
	store, err := snapshot.New(r.Database, r.snapshotRoot)
	if err != nil {
		return err
	}
	current, record, err := store.LoadLatest(ctx, r.instanceID)
	if err != nil {
		return err
	}
	evidence := dispatchstage.Evidence{Snapshot: current, Record: record, Preferences: preferences, CollectedAt: observedAt}
	r.selectionEvidence = &evidence
	return r.dispatchStage.RefreshCapacity(ctx, evidence)
}
