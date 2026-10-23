package resourcesapi

import (
	"fmt"
	"net/http"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/expiredunknown"
	"github.com/vertex-pro/vertex-pro/internal/snapshot"
)

// serverEvidence loads the current complete snapshot and checks its digest.
// Client-supplied presence flags or snapshot coordinates never authorize closure.
func (d Deps) serverEvidence(request *http.Request, chain chainFacts) (expiredunknown.Evidence, error) {
	var state, updated string
	if err := d.Database.QueryRowContext(request.Context(), `SELECT sync_state,updated_at FROM sync_instance_state WHERE instance_id=?`, chain.instanceID).Scan(&state, &updated); err != nil {
		return expiredunknown.Evidence{}, err
	}
	stamp, err := time.Parse(time.RFC3339Nano, updated)
	if err != nil || state != "READY" || d.currentTime().Sub(stamp) > 30*time.Second || stamp.After(d.currentTime().Add(time.Second)) {
		return expiredunknown.Evidence{}, fmt.Errorf("snapshot is not fresh")
	}
	store, err := snapshot.New(d.Database, d.SnapshotRoot)
	if err != nil {
		return expiredunknown.Evidence{}, err
	}
	current, record, err := store.LoadLatest(request.Context(), chain.instanceID)
	if err != nil || !record.Complete {
		return expiredunknown.Evidence{}, fmt.Errorf("complete snapshot unavailable")
	}
	var preGeneration, preRevision int64
	var preDigest string
	if err := d.Database.QueryRowContext(request.Context(), `SELECT pre_add_sync_generation,pre_add_snapshot_revision,pre_add_snapshot_digest FROM submit_intents WHERE submit_intent_id=?`, chain.intentID).Scan(&preGeneration, &preRevision, &preDigest); err != nil {
		return expiredunknown.Evidence{}, err
	}
	if int64(record.Generation) < preGeneration || (int64(record.Generation) == preGeneration && int64(record.Revision) <= preRevision) || record.Digest == preDigest {
		return expiredunknown.Evidence{}, fmt.Errorf("snapshot does not postdate dispatch")
	}
	torrent, present := current.Torrents[chain.identityKey]
	qbID := ""
	if present {
		qbID = torrent.ID
		if qbID == "" {
			qbID = chain.identityKey
		}
	}
	return expiredunknown.Evidence{SyncGeneration: int64(record.Generation), SnapshotRevision: int64(record.Revision), SnapshotDigest: record.Digest, IdentityPresent: present, QBTorrentID: qbID}, nil
}

func exceptionActions(chain chainFacts, evidence expiredunknown.Evidence, fresh bool, now time.Time) []string {
	actions := make([]string, 0, 3)
	if chain.attemptState == "UNKNOWN" {
		actions = append(actions, "EXPIRE_UNRESOLVED")
	}
	if !fresh {
		return actions
	}
	if evidence.IdentityPresent && (chain.attemptState == "UNKNOWN" || chain.attemptState == "EXPIRED_UNKNOWN") {
		return append(actions, "CONFIRM_EFFECT")
	}
	if !evidence.IdentityPresent && chain.attemptState == "EXPIRED_UNKNOWN" {
		return append(actions, "CONFIRM_NO_EFFECT_AND_EXPIRE")
	}
	deadline, err := time.Parse(time.RFC3339Nano, chain.participationDeadline)
	if !evidence.IdentityPresent && chain.attemptState == "UNKNOWN" && err == nil && deadline.After(now) {
		actions = append(actions, "CONFIRM_NO_EFFECT_AND_RETRY")
	}
	return actions
}

func (d Deps) exceptionPayload(request *http.Request, jobID string) map[string]any {
	result := map[string]any{"allowed_actions": []string{}, "evidence_available": false}
	chain, ok := d.lookupChain(request, jobID)
	if !ok {
		return result
	}
	result["attempt_state"] = chain.attemptState
	evidence, err := d.serverEvidence(request, chain)
	result["evidence_available"] = err == nil
	result["allowed_actions"] = exceptionActions(chain, evidence, err == nil, d.currentTime())
	if err == nil {
		result["identity_present"] = evidence.IdentityPresent
	}
	return result
}
