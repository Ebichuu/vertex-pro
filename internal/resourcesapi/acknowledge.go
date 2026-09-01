package resourcesapi

import (
	"fmt"
	"net/http"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/expiredunknown"
	"github.com/vertex-pro/vertex-pro/internal/firstconfirm"
	"github.com/vertex-pro/vertex-pro/internal/httpapi"
	"github.com/vertex-pro/vertex-pro/internal/saferetry"
	"github.com/vertex-pro/vertex-pro/internal/unknownreconciler"
)

type chainFacts struct {
	attemptID, attemptState, intentID, reservationID, storageTargetID, instanceID, identityKey, participationDeadline, waiterID string
}

func (d Deps) lookupChain(request *http.Request, jobID string) (chainFacts, bool) {
	var chain chainFacts
	err := d.Database.QueryRowContext(request.Context(), `SELECT a.attempt_id, a.attempt_state, s.submit_intent_id, r.reservation_id, a.storage_target_id, a.instance_id, a.identity_key, COALESCE(w.waiter_id,'') FROM race_jobs j JOIN placement_attempts a ON a.attempt_id = j.current_attempt_id JOIN submit_intents s ON s.attempt_id = a.attempt_id JOIN capacity_reservations r ON r.attempt_id = a.attempt_id AND r.reservation_state = 'ACTIVE' LEFT JOIN sync_waiters w ON w.attempt_id = a.attempt_id AND w.waiter_state = 'ACTIVE' WHERE j.job_id = ?`, jobID).Scan(&chain.attemptID, &chain.attemptState, &chain.intentID, &chain.reservationID, &chain.storageTargetID, &chain.instanceID, &chain.identityKey, &chain.waiterID)
	if err != nil {
		return chainFacts{}, false
	}
	_ = d.Database.QueryRowContext(request.Context(), `SELECT participation_deadline FROM race_jobs WHERE job_id = ?`, jobID).Scan(&chain.participationDeadline)
	return chain, true
}

// acknowledgeUnknown 实现 §16.8 命令矩阵中已具备应用服务的三条人工闭环：
// CONFIRM_EFFECT / CONFIRM_NO_EFFECT_AND_EXPIRE（ADD-11）与
// CONFIRM_NO_EFFECT_AND_RETRY（ADD-12）；其余 action 由 API-07 命令层接线。
// 证据由请求体提供且必须为新鲜完整合并快照，错误统一映射 JOB_STATE_CONFLICT。
func (d Deps) acknowledgeUnknown(response http.ResponseWriter, request *http.Request) {
	jobID := request.PathValue("id")
	var payload acknowledgePayload
	payload.Now = time.Now()
	if d.Now != nil {
		payload.Now = d.Now()
	}
	if err := httpapi.DecodeJSON(response, request, &payload, 64<<10); err != nil {
		return
	}
	chain, ok := d.lookupChain(request, jobID)
	if !ok {
		httpapi.WriteError(response, request, "NOT_FOUND", "job or delivery chain not found")
		return
	}
	evidence := expiredunknown.Evidence{
		SyncGeneration: payload.Evidence.SyncGeneration, SnapshotRevision: payload.Evidence.SnapshotRevision,
		SnapshotDigest: payload.Evidence.SnapshotDigest, IdentityPresent: payload.Evidence.IdentityPresent,
		QBTorrentID: payload.Evidence.QBTorrentID,
	}
	command := expiredunknown.Request{
		CommandID: payload.CommandID, JobID: jobID, AttemptID: chain.attemptID,
		SubmitIntentID: chain.intentID, ReservationID: chain.reservationID,
		StorageTargetID: chain.storageTargetID, InstanceID: chain.instanceID,
		IdentityKey: chain.identityKey, BindingID: payload.BindingID,
		Evidence: evidence, Now: d.Now(),
	}
	switch payload.Action {
	case "CONFIRM_EFFECT":
		if chain.attemptState == "UNKNOWN" {
			result, err := d.confirmUnknownEffect(request, command, chain)
			if err != nil {
				httpapi.WriteError(response, request, "JOB_STATE_CONFLICT", err.Error())
				return
			}
			d.writeData(response, request, map[string]any{"command_id": payload.CommandID, "idempotent": result.Idempotent})
			return
		}
		result, err := expiredunknown.ConfirmEffect(request.Context(), d.Database, command)
		if err != nil {
			httpapi.WriteError(response, request, "JOB_STATE_CONFLICT", err.Error())
			return
		}
		d.writeData(response, request, map[string]any{"command_id": payload.CommandID, "idempotent": result.Idempotent})
	case "CONFIRM_NO_EFFECT_AND_EXPIRE":
		result, err := expiredunknown.ConfirmNoEffectAndExpire(request.Context(), d.Database, command)
		if err != nil {
			httpapi.WriteError(response, request, "JOB_STATE_CONFLICT", err.Error())
			return
		}
		d.writeData(response, request, map[string]any{"command_id": payload.CommandID, "idempotent": result.Idempotent})
	case "CONFIRM_NO_EFFECT_AND_RETRY":
		result, err := saferetry.ConfirmNoEffectAndRetry(request.Context(), d.Database, saferetry.Request{
			CommandID: payload.CommandID, JobID: jobID, AttemptID: chain.attemptID,
			SubmitIntentID: chain.intentID, ReservationID: chain.reservationID,
			StorageTargetID: chain.storageTargetID, InstanceID: chain.instanceID,
			IdentityKey: chain.identityKey, MaxAttempts: payload.MaxAttempts,
			ParticipationDeadline: payload.ParticipationDeadline, Evidence: saferetry.Evidence(evidence), Now: d.Now(),
		})
		if err != nil {
			httpapi.WriteError(response, request, "JOB_STATE_CONFLICT", err.Error())
			return
		}
		d.writeData(response, request, map[string]any{"command_id": payload.CommandID, "idempotent": result.Idempotent, "job_state": result.JobState})
	case "EXPIRE_UNRESOLVED":
		d.expireUnresolved(response, request, jobID, chain, payload.CommandID, payload.Now)
	case "CONTINUE_RECONCILIATION":
		d.continueReconciliation(response, request, chain, unknownreconciler.Snapshot{
			InstanceID: chain.instanceID, SyncGeneration: payload.Evidence.SyncGeneration,
			SnapshotRevision: payload.Evidence.SnapshotRevision, SnapshotDigest: payload.Evidence.SnapshotDigest,
			IdentityPresent: payload.Evidence.IdentityPresent, QBTorrentID: payload.Evidence.QBTorrentID,
		}, payload.CommandID, payload.Now)
	default:
		httpapi.WriteError(response, request, "VALIDATION_FAILED", "unknown action")
	}
}

// acknowledgePayload 是 acknowledge-unknown 的请求体。
type acknowledgePayload struct {
	Action    string `json:"action"`
	CommandID string `json:"command_id"`
	Evidence  struct {
		SyncGeneration   int64  `json:"sync_generation"`
		SnapshotRevision int64  `json:"snapshot_revision"`
		SnapshotDigest   string `json:"snapshot_digest"`
		IdentityPresent  bool   `json:"identity_present"`
		QBTorrentID      string `json:"qb_torrent_id"`
	} `json:"evidence"`
	BindingID             string `json:"binding_id"`
	MaxAttempts           int    `json:"max_attempts"`
	ParticipationDeadline string `json:"participation_deadline"`
	Now                   time.Time
}

// confirmUnknownEffect 对 UNKNOWN 尝试直接执行 8.7.3 首次确认原子事务。
func (d Deps) confirmUnknownEffect(request *http.Request, command expiredunknown.Request, chain chainFacts) (firstconfirm.Result, error) {
	var preGeneration, preRevision int64
	var preDigest string
	if err := d.Database.QueryRowContext(request.Context(), `SELECT pre_add_sync_generation, pre_add_snapshot_revision, pre_add_snapshot_digest FROM submit_intents WHERE submit_intent_id = ?`, chain.intentID).Scan(&preGeneration, &preRevision, &preDigest); err != nil {
		return firstconfirm.Result{}, err
	}
	return firstconfirm.Confirm(request.Context(), d.Database, firstconfirm.Request{
		BindingID: command.BindingID, AttemptID: command.AttemptID, SubmitIntentID: command.SubmitIntentID,
		ReservationID: command.ReservationID, JobID: command.JobID, StorageTargetID: command.StorageTargetID,
		IdentityKey: command.IdentityKey, QBTorrentID: command.Evidence.QBTorrentID,
		SyncGeneration: command.Evidence.SyncGeneration, SnapshotRevision: command.Evidence.SnapshotRevision,
		SnapshotDigest: command.Evidence.SnapshotDigest, PreAddSyncGeneration: preGeneration,
		PreAddSnapshotRevision: preRevision, PreAddSnapshotDigest: preDigest,
		EvidenceDigest: fmt.Sprintf("snapshot|%d|%d|%s", command.Evidence.SyncGeneration, command.Evidence.SnapshotRevision, command.Evidence.SnapshotDigest),
		Now:            command.Now,
	})
}
