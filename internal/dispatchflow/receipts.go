package dispatchflow

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/dispatchcommit"
)

// ReceiptRecovery owns responses that could not be committed. Pending attempts
// still consume the existing per-instance admission budget, bounding this map.
// A process exit loses these memory-only facts; startup conservatively recovers
// STARTED as UNKNOWN, never as a request that may be sent again.
type ReceiptRecovery struct {
	database *sql.DB
	mu       sync.Mutex
	retryMu  sync.Mutex
	pending  map[string]dispatchcommit.Request
}

func NewReceiptRecovery(database *sql.DB) *ReceiptRecovery {
	return &ReceiptRecovery{database: database, pending: map[string]dispatchcommit.Request{}}
}

func (r *ReceiptRecovery) remember(receipt dispatchcommit.Request) {
	r.mu.Lock()
	r.pending[receipt.AttemptID] = receipt
	r.mu.Unlock()
}

func (r *ReceiptRecovery) Retry(ctx context.Context) error {
	r.retryMu.Lock()
	defer r.retryMu.Unlock()
	r.mu.Lock()
	list := make([]dispatchcommit.Request, 0, len(r.pending))
	for _, receipt := range r.pending {
		list = append(list, receipt)
	}
	r.mu.Unlock()
	sort.Slice(list, func(i, j int) bool { return list[i].AttemptID < list[j].AttemptID })
	var failures []error
	for _, receipt := range list {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		attemptCtx, cancel := context.WithTimeout(ctx, time.Second)
		committed, err := dispatchcommit.Record(attemptCtx, r.database, receipt)
		cancel()
		if err != nil && committed.AttemptID == "" {
			failures = append(failures, err)
			continue
		}
		// After a successful commit, the durable confirmation coordinator owns
		// any pending replay. Do not keep terminal receipts in the buffer.
		r.mu.Lock()
		delete(r.pending, receipt.AttemptID)
		r.mu.Unlock()
	}
	return errors.Join(failures...)
}
