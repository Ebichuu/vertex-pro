package app

import (
	"context"
	"time"
)

func (r *Runtime) startPipelineWorkers(ctx context.Context, interval time.Duration) {
	r.workers.Add(1)
	go func() {
		defer r.workers.Done()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			if err := r.dispatchStage.RecoverReceipts(ctx); err != nil {
				r.Metrics.Inc("vertex_receipt_recovery_errors_total", map[string]string{"instance": r.instanceID})
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	r.workers.Add(1)
	go func() {
		defer r.workers.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			results, failures, err := r.dispatchStage.Run(ctx)
			if err != nil {
				r.Metrics.Inc("vertex_dispatch_jobs_total", map[string]string{"result": "allocation_error"})
			}
			for _, result := range results {
				r.Metrics.Inc("vertex_dispatch_jobs_total", map[string]string{"result": "allocated"})
				r.Metrics.Inc("vertex_capacity_reservations_total", map[string]string{"state": result.ReservationState})
			}
			r.Metrics.Add("vertex_dispatch_jobs_total", map[string]string{"result": "waiting"}, uint64(len(failures)))
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			case <-r.allocateWake:
			}
		}
	}()
	for i := 0; i < 2; i++ {
		r.workers.Add(1)
		go func() {
			defer r.workers.Done()
			timer := time.NewTimer(time.Second)
			defer timer.Stop()
			for {
				for drained := 0; drained < 32 && ctx.Err() == nil; drained++ {
					sent, err := r.dispatchStage.SendNext(ctx)
					if err != nil {
						r.Metrics.Inc("vertex_dispatch_jobs_total", map[string]string{"result": "send_error"})
					}
					if !sent {
						break
					}
					if err == nil {
						r.Metrics.Inc("vertex_dispatch_jobs_total", map[string]string{"result": "send_processed"})
					}
				}
				timer.Reset(r.dispatchStage.RetryDelay(ctx))
				select {
				case <-ctx.Done():
					return
				case <-timer.C:
				case <-r.dispatchStage.Notifications():
				}
				timer.Stop()
			}
		}()
	}
	r.workers.Add(1)
	go func() {
		defer r.workers.Done()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			if err := r.dispatchStage.Warm(ctx); err != nil {
				r.Metrics.Inc("vertex_policy_sync_errors_total", map[string]string{"instance": r.instanceID})
			}
			select {
			case r.allocateWake <- struct{}{}:
			default:
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}
