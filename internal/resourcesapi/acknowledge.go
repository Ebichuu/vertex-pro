package resourcesapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/apiidempotency"
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
	if err := httpapi.DecodeJSON(response, request, &payload, 64<<10); err != nil {
		writeRuleRequestError(response, request, err)
		return
	}
	key := strings.TrimSpace(request.Header.Get("Idempotency-Key"))
	if key == "" {
		httpapi.WriteError(response, request, "INVALID_REQUEST", "Idempotency-Key is required")
		return
	}
	now := d.currentTime()
	fingerprint := apiidempotency.Fingerprint([]byte(jobID + "\x00" + payload.Action))
	begin, err := apiidempotency.Begin(request.Context(), d.Database, key, "admin", fingerprint, now)
	if err != nil {
		if errors.Is(err, apiidempotency.ErrKeyReused) {
			httpapi.WriteError(response, request, "IDEMPOTENCY_KEY_REUSED", "Idempotency-Key was used with a different action")
			return
		}
		httpapi.WriteError(response, request, "INTERNAL_ERROR", "register exception command failed")
		return
	}
	if begin.Replayed {
		d.writeAcceptedCommand(response, request, begin.CommandID)
		return
	}
	fail := func(code, message string) {
		_ = apiidempotency.Complete(request.Context(), d.Database, begin.CommandID, code, "", now)
		httpapi.WriteError(response, request, httpapi.ErrorCode(code), message)
	}
	chain, ok := d.lookupChain(request, jobID)
	if !ok {
		fail("NOT_FOUND", "job or delivery chain not found")
		return
	}
	evidence, evidenceErr := d.serverEvidence(request, chain)
	allowed := exceptionActions(chain, evidence, evidenceErr == nil, now)
	valid := false
	for _, action := range allowed {
		if action == payload.Action {
			valid = true
		}
	}
	if !valid {
		fail("JOB_STATE_CONFLICT", "action requires a compatible job state and fresh server snapshot")
		return
	}
	command := expiredunknown.Request{
		CommandID: begin.CommandID, JobID: jobID, AttemptID: chain.attemptID,
		SubmitIntentID: chain.intentID, ReservationID: chain.reservationID,
		StorageTargetID: chain.storageTargetID, InstanceID: chain.instanceID,
		IdentityKey: chain.identityKey, BindingID: "binding-" + chain.attemptID,
		Evidence: evidence, Now: now,
	}
	result := map[string]any{"job_id": jobID, "action": payload.Action}
	var actionErr error
	switch payload.Action {
	case "CONFIRM_EFFECT":
		if chain.attemptState == "UNKNOWN" {
			_, actionErr = d.confirmUnknownEffect(request, command, chain)
		} else {
			_, actionErr = expiredunknown.ConfirmEffect(request.Context(), d.Database, command)
		}
	case "CONFIRM_NO_EFFECT_AND_EXPIRE":
		_, actionErr = expiredunknown.ConfirmNoEffectAndExpire(request.Context(), d.Database, command)
	case "CONFIRM_NO_EFFECT_AND_RETRY":
		_, actionErr = saferetry.ConfirmNoEffectAndRetry(request.Context(), d.Database, saferetry.Request{
			CommandID: begin.CommandID, JobID: jobID, AttemptID: chain.attemptID,
			SubmitIntentID: chain.intentID, ReservationID: chain.reservationID,
			StorageTargetID: chain.storageTargetID, InstanceID: chain.instanceID,
			IdentityKey: chain.identityKey, MaxAttempts: 3,
			ParticipationDeadline: chain.participationDeadline, Evidence: saferetry.Evidence(evidence), Now: now,
		})
	case "EXPIRE_UNRESOLVED":
		_, actionErr = unknownreconciler.ExpireUnresolved(request.Context(), d.Database, unknownreconciler.ExpireRequest{CommandID: begin.CommandID, JobID: jobID, AttemptID: chain.attemptID, ReservationID: chain.reservationID, WaiterID: chain.waiterID, Now: now})
	default:
		fail("VALIDATION_FAILED", "unknown action")
		return
	}
	if actionErr != nil {
		fail("JOB_STATE_CONFLICT", "exception action could not be applied to the current state")
		return
	}
	var state string
	_ = d.Database.QueryRowContext(request.Context(), `SELECT job_state FROM race_jobs WHERE job_id=?`, jobID).Scan(&state)
	result["job_state"] = state
	raw, _ := json.Marshal(result)
	if err := apiidempotency.Complete(request.Context(), d.Database, begin.CommandID, "", string(raw), now); err != nil {
		httpapi.WriteError(response, request, "INTERNAL_ERROR", "complete exception command failed")
		return
	}
	d.writeAcceptedCommand(response, request, begin.CommandID)
}

func (d Deps) writeAcceptedCommand(response http.ResponseWriter, request *http.Request, id string) {
	command, err := commandPayload(request.Context(), d.Database, id)
	if err != nil {
		httpapi.WriteError(response, request, "INTERNAL_ERROR", "read exception command failed")
		return
	}
	response.Header().Set("Location", "/api/v1/commands/"+id)
	httpapi.WriteJSON(response, http.StatusAccepted, command)
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
	BindingID             string    `json:"binding_id"`
	MaxAttempts           int       `json:"max_attempts"`
	ParticipationDeadline string    `json:"participation_deadline"`
	Now                   time.Time `json:"-"`
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
