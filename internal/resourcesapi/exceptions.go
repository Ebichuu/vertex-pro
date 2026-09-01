package resourcesapi

import (
	"net/http"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/httpapi"
	"github.com/vertex-pro/vertex-pro/internal/unknownreconciler"
)

// expireUnresolved 实现 §16.8 矩阵的 EXPIRE_UNRESOLVED 受控 action：
// UNKNOWN -> EXPIRED_UNKNOWN（保留 Reservation 与活动链，审计命令）。
func (d Deps) expireUnresolved(response http.ResponseWriter, request *http.Request, jobID string, chain chainFacts, commandID string, now time.Time) {
	expireRequest := unknownreconciler.ExpireRequest{
		CommandID: commandID, JobID: jobID, AttemptID: chain.attemptID,
		ReservationID: chain.reservationID, WaiterID: chain.waiterID, Now: now,
	}
	result, err := unknownreconciler.ExpireUnresolved(request.Context(), d.Database, expireRequest)
	if err != nil {
		httpapi.WriteError(response, request, "JOB_STATE_CONFLICT", err.Error())
		return
	}
	d.writeData(response, request, map[string]any{"command_id": result.CommandID, "outcome": "EXPIRED_UNKNOWN"})
}

// continueReconciliation 实现 CONTINUE_RECONCILIATION：以请求携带的快照
// 证据立即执行一次对账 Sweep，返回该尝试的结果（确认/无副作用/保持）。
func (d Deps) continueReconciliation(response http.ResponseWriter, request *http.Request, chain chainFacts, evidence unknownreconciler.Snapshot, commandID string, now time.Time) {
	provider := unknownreconciler.FixedProvider{Snapshot: evidence}
	outcomes, err := unknownreconciler.Sweep(request.Context(), d.Database, provider, unknownreconciler.SweepInput{Now: now})
	if err != nil {
		httpapi.WriteError(response, request, "JOB_STATE_CONFLICT", err.Error())
		return
	}
	outcome := "HOLD_NO_FRESH_EVIDENCE"
	for _, item := range outcomes {
		if item.AttemptID == chain.attemptID {
			outcome = item.Outcome
		}
	}
	d.writeData(response, request, map[string]any{"command_id": commandID, "outcome": outcome})
}
