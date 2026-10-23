package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/vertex-pro/vertex-pro/internal/quipolicy"
)

// Sync policy shutdown after configuration changes before reporting success.
// Each instance shares its lock with pre-Add policy creation and dispatch.
func (r *Runtime) syncDisabledPolicies(ctx context.Context) error {
	var failures []error
	for _, worker := range append([]*Runtime{r}, r.children...) {
		if worker.controlClient == nil {
			continue
		}
		worker.syncMu.Lock()
		err := (quipolicy.Service{DB: r.Database, Client: worker.controlClient, Instance: worker.instanceID, Now: r.now}).SyncDisabled(ctx)
		worker.syncMu.Unlock()
		if err != nil {
			failures = append(failures, err)
		}
	}
	var pending int
	if err := r.Database.QueryRowContext(ctx, `SELECT COUNT(*) FROM qui_policy_bindings b JOIN rules r ON r.rule_id=b.rule_id LEFT JOIN sites s ON s.site_id=r.site_id WHERE b.enabled=1 AND (r.enabled=0 OR r.archived=1 OR s.enabled=0 OR s.archived=1)`).Scan(&pending); err != nil {
		failures = append(failures, err)
	} else if pending > 0 {
		failures = append(failures, fmt.Errorf("qui policy shutdown remains unconfirmed"))
	}
	return errors.Join(failures...)
}
