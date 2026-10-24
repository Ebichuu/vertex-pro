package app

import (
	"context"
	"errors"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/dispatchstage"
)

func (r *Runtime) storeObservation(e *dispatchstage.Evidence) {
	r.observationMu.Lock()
	r.selectionEvidence = e
	r.observationMu.Unlock()
}
func (r *Runtime) loadObservation() *dispatchstage.Evidence {
	r.observationMu.RLock()
	defer r.observationMu.RUnlock()
	return r.selectionEvidence
}

// This path never waits on syncMu or performs a network request. A background
// refresh publishes immutable evidence only after all observation checks pass.
func (r *Runtime) collectCachedEvidence(ctx context.Context) (dispatchstage.Evidence, func(), error) {
	e := r.loadObservation()
	if e == nil || e.CollectedAt.IsZero() || r.now().Before(e.CollectedAt) || r.now().Sub(e.CollectedAt) > 30*time.Second {
		return dispatchstage.Evidence{}, nil, errors.New("INSTANCE_EVIDENCE_NOT_READY")
	}
	var state, digest string
	var generation, revision uint64
	if err := r.Database.QueryRowContext(ctx, `SELECT sync_state,sync_generation,snapshot_revision,snapshot_digest FROM sync_instance_state WHERE instance_id=?`, r.instanceID).Scan(&state, &generation, &revision, &digest); err != nil {
		return dispatchstage.Evidence{}, nil, err
	}
	if state != "READY" || !e.Record.Complete || e.Record.InstanceID != r.instanceID || generation != e.Record.Generation || revision != e.Record.Revision || digest != e.Record.Digest {
		return dispatchstage.Evidence{}, nil, errors.New("INSTANCE_EVIDENCE_CHANGED")
	}
	return *e, nil, nil
}

func (r *Runtime) wakeDispatch() {
	for _, w := range append([]*Runtime{r}, r.children...) {
		if w.allocateWake != nil {
			select {
			case w.allocateWake <- struct{}{}:
			default:
			}
		}
		if w.dispatchStage != nil {
			w.dispatchStage.Wake()
		}
	}
}
