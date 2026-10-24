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
	scheduler.SetClock(runtime.now)
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
	runtime.startDiscoveryWorkers(ctx, interval)
	runtime.startPreparationWorkers(ctx)
	return nil
}

func (runtime *Runtime) collectDispatchEvidence(ctx context.Context) (dispatchstage.Evidence, func(), error) {
	runtime.syncMu.Lock()
	return runtime.collectDispatchEvidenceLocked(ctx)
}

func (runtime *Runtime) collectDispatchEvidenceLocked(ctx context.Context) (dispatchstage.Evidence, func(), error) {
	release := runtime.syncMu.Unlock
	runtime.storeObservation(nil)
	collectedAt := runtime.now().UTC()
	if err := runtime.requireEnabledInstance(ctx); err != nil {
		return dispatchstage.Evidence{}, release, err
	}
	if err := runtime.syncEvidenceLocked(ctx, runtime.now, false); err != nil {
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
	evidence := dispatchstage.Evidence{Snapshot: current, Record: record, Preferences: preferences, CollectedAt: collectedAt}
	runtime.storeObservation(&evidence)
	return evidence, release, nil
}

// Legacy synchronous callers retain the instance lock through projection.
// Production pipeline workers use collectCachedEvidence without this lock.
func (runtime *Runtime) collectSelectionEvidence(ctx context.Context) (dispatchstage.Evidence, func(), error) {
	runtime.syncMu.Lock()
	if e := runtime.loadObservation(); e != nil {
		now := runtime.now().UTC()
		if !e.CollectedAt.IsZero() && !now.Before(e.CollectedAt) && now.Sub(e.CollectedAt) <= 30*time.Second {
			var state, digest string
			var generation, revision uint64
			err := runtime.Database.QueryRowContext(ctx, `SELECT sync_state,sync_generation,snapshot_revision,snapshot_digest FROM sync_instance_state WHERE instance_id=?`, runtime.instanceID).Scan(&state, &generation, &revision, &digest)
			if err != nil {
				runtime.storeObservation(nil)
				return dispatchstage.Evidence{}, runtime.syncMu.Unlock, err
			}
			if state == "READY" && e.Record.Complete && e.Record.InstanceID == runtime.instanceID &&
				e.Record.Generation == generation && e.Record.Revision == revision && e.Record.Digest == digest {
				return *e, runtime.syncMu.Unlock, nil
			}
		}
	}
	return runtime.collectDispatchEvidenceLocked(ctx)
}

func (runtime *Runtime) requireEnabledInstance(ctx context.Context) error {
	if runtime.controlClient == nil {
		return fmt.Errorf("qui control client unavailable")
	}
	instances, err := runtime.controlClient.Instances(ctx)
	if err != nil {
		return err
	}
	for _, instance := range instances {
		if instance.ID == runtime.instanceID && instance.Enabled {
			return nil
		}
	}
	if err := updateSyncState(ctx, runtime.Database, runtime.instanceID, "OFFLINE", runtime.now()); err != nil {
		return err
	}
	return fmt.Errorf("configured qui instance is disabled or missing")
}
