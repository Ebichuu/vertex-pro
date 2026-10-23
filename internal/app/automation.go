package app

import (
	"context"
	"fmt"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/dispatchstage"
	"github.com/vertex-pro/vertex-pro/internal/snapshot"
)

func (runtime *Runtime) startScheduler(config *SchedulerConfig) error {
	scheduler, err := newSchedulerCycle(runtime.Database, config, runtime.instanceID, runtime.rssFetcher, runtime.hardenedOutbound)
	if err != nil {
		return fmt.Errorf("initialize RSS scheduler: %w", err)
	}
	runtime.scheduler = scheduler
	// A scheduler may run without qui, to establish baselines and prepare jobs.
	ctx, cancel := context.WithCancel(context.Background())
	previousCancel := runtime.cancel
	runtime.cancel = func() {
		cancel()
		if previousCancel != nil {
			previousCancel()
		}
	}
	interval := config.PollInterval
	if interval <= 0 || interval > time.Minute {
		interval = time.Minute
	}
	runtime.workers.Add(1)
	go func() {
		defer runtime.workers.Done()
		_ = runtime.RunSchedulerCycle(ctx)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = runtime.RunSchedulerCycle(ctx)
			}
		}
	}()
	return nil
}

func (runtime *Runtime) collectDispatchEvidence(ctx context.Context) (dispatchstage.Evidence, func(), error) {
	runtime.syncMu.Lock()
	release := runtime.syncMu.Unlock
	collectedAt := runtime.now().UTC()
	if err := runtime.tickSyncLocked(ctx, runtime.now); err != nil {
		return dispatchstage.Evidence{}, release, err
	}
	preferences, err := runtime.proxyClient.Preferences(ctx)
	if err != nil {
		return dispatchstage.Evidence{}, release, err
	}
	store, err := snapshot.New(runtime.Database, runtime.snapshotRoot)
	if err != nil {
		return dispatchstage.Evidence{}, release, err
	}
	current, record, err := store.LoadLatest(ctx, runtime.instanceID)
	if err != nil {
		return dispatchstage.Evidence{}, release, err
	}
	return dispatchstage.Evidence{Snapshot: current, Record: record, Preferences: preferences, CollectedAt: collectedAt}, release, nil
}
