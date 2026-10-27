package app

import (
	"context"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/deletionsync"
	"github.com/vertex-pro/vertex-pro/internal/syncmerge"
)

// deletionReconcileInterval paces the background convergence of qui
// automations with stored deletion policies.
const deletionReconcileInterval = time.Minute

// deletionService exposes deletion policies over every qui-connected
// instance of this process (the root runtime and its children).
func (r *Runtime) deletionService() (deletionsync.Service, bool) {
	if r == nil || r.Database == nil {
		return deletionsync.Service{}, false
	}
	instances := map[string]deletionsync.Instance{}
	for _, worker := range append([]*Runtime{r}, r.children...) {
		if worker.controlClient == nil || worker.instanceID == "" {
			continue
		}
		w := worker
		instance := deletionsync.Instance{Client: w.controlClient, Activity: w.controlClient, Snapshot: func() (syncmerge.Snapshot, bool) {
			snapshot, _, err := w.currentSnapshot()
			return snapshot, err == nil
		}}
		if w.proxyClient != nil {
			instance.Tags = w.proxyClient
		}
		instances[w.instanceID] = instance
	}
	return deletionsync.Service{Store: deletionsync.Store{DB: r.Database, Now: r.now}, Instances: instances}, true
}

// startDeletionReconciler re-converges qui every interval. It stays idle
// until a deletion policy exists and makes no qui call before that.
func (r *Runtime) startDeletionReconciler(interval time.Duration) {
	ctx, cancel := context.WithCancel(context.Background())
	r.deletionCancel = cancel
	r.workers.Add(1)
	go func() {
		defer r.workers.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			service, ok := r.deletionService()
			if !ok || len(service.Instances) == 0 {
				continue
			}
			if work, err := service.Store.HasWork(ctx); err == nil && work {
				if err := service.ReconcileAll(ctx); err != nil {
					r.Metrics.Inc("vertex_deletion_reconcile_errors_total", nil)
				}
			}
			// Audit legacy per-rule deletions too, even without new policies.
			if work, err := service.Store.HasAuditWork(ctx); err == nil && work {
				gaps, err := service.ImportAuditAll(ctx)
				if err != nil {
					r.Metrics.Inc("vertex_deletion_audit_errors_total", nil)
				}
				if gaps > 0 {
					r.Metrics.Add("vertex_deletion_audit_gaps_total", nil, uint64(gaps))
				}
			}
		}
	}()
}
