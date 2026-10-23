package app

import (
	"context"
	"github.com/vertex-pro/vertex-pro/internal/dispatchstage"
	"github.com/vertex-pro/vertex-pro/internal/snapshot"
)

func (r *Runtime) refreshCapacity(ctx context.Context) error {
	if r.dispatchStage == nil {
		return nil
	}
	at := r.now().UTC()
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
	return r.dispatchStage.RefreshCapacity(ctx, dispatchstage.Evidence{Snapshot: current, Record: record, Preferences: preferences, CollectedAt: at})
}
