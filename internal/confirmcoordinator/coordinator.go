// Package confirmcoordinator drives the confirmation and reconciliation stage
// of the Add chain from sync watcher events. For every active sync waiter on
// the instance it commits confirmation and waiter closure atomically when the
// watched identity appears; it then lets the unknown
// reconciler sweep any UNKNOWN chains, backed by the same event snapshot.
package confirmcoordinator

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/firstconfirm"
	"github.com/vertex-pro/vertex-pro/internal/syncmerge"
	"github.com/vertex-pro/vertex-pro/internal/syncwaiter"
	"github.com/vertex-pro/vertex-pro/internal/unknownreconciler"
)

// Event is one completed sync cycle from the watcher.
type Event struct {
	InstanceID string
	Generation uint64
	Revision   uint64
	Snapshot   syncmerge.Snapshot
}

// Outcome records what happened to one waiter this cycle.
type Outcome struct {
	WaiterID    string
	AttemptID   string
	IdentityKey string
	Action      string // CONFIRMED | NO_IDENTITY_MATCH | NOTIFY_IGNORED
	Idempotent  bool
}

type Coordinator struct {
	database *sql.DB
}

func New(database *sql.DB) (*Coordinator, error) {
	if database == nil {
		return nil, fmt.Errorf("database is required")
	}
	return &Coordinator{database: database}, nil
}

// OnSyncEvent processes one sync event: active waiters whose identity is now
// present in the snapshot are notified and confirmed. Chains in UNKNOWN are
// resolved by unknownreconciler.Sweep against the same snapshot.
func (coordinator *Coordinator) OnSyncEvent(ctx context.Context, event Event, now time.Time) ([]Outcome, error) {
	if event.InstanceID == "" || now.IsZero() {
		return nil, fmt.Errorf("instance id and time are required")
	}
	if err := coordinator.advanceConfirmationTimeouts(ctx, event.InstanceID, now); err != nil {
		return nil, err
	}
	waiters, err := syncwaiter.RestoreActive(ctx, coordinator.database, event.InstanceID)
	if err != nil {
		return nil, fmt.Errorf("restore active waiters: %w", err)
	}
	outcomes := make([]Outcome, 0, len(waiters))
	for _, waiter := range waiters {
		outcome := Outcome{WaiterID: waiter.ID, AttemptID: waiter.AttemptID, IdentityKey: waiter.IdentityKey}
		_, present := event.Snapshot.Torrents[waiter.IdentityKey]
		if !present {
			// Without the identity in the snapshot there is nothing to
			// confirm; the waiter stays active until its deadlines and the
			// reconciler decides UNKNOWN chains.
			outcome.Action = "NO_IDENTITY_MATCH"
			outcomes = append(outcomes, outcome)
			continue
		}
		if int64(event.Generation) < waiter.Generation || (int64(event.Generation) == waiter.Generation && int64(event.Revision) <= waiter.SourceRevision) {
			outcome.Action = "NOTIFY_IGNORED"
			outcomes = append(outcomes, outcome)
			continue
		}
		confirmed, err := coordinator.confirm(ctx, waiter, event, now)
		if err != nil {
			return nil, err
		}
		outcome.Action = "CONFIRMED"
		outcome.Idempotent = confirmed.Idempotent
		outcomes = append(outcomes, outcome)
	}
	sweepOutcomes, err := coordinator.sweep(ctx, event, now)
	if err != nil {
		return nil, err
	}
	for _, sweepOutcome := range sweepOutcomes {
		outcomes = append(outcomes, Outcome{AttemptID: sweepOutcome.AttemptID, Action: sweepOutcome.Outcome})
	}
	return outcomes, nil
}

func (coordinator *Coordinator) confirm(ctx context.Context, waiter syncwaiter.Waiter, event Event, now time.Time) (firstconfirm.Result, error) {
	var submitIntentID, jobID, storageTargetID, reservationID string
	var preGeneration, preRevision int64
	var preDigest, preFact string
	err := coordinator.database.QueryRowContext(ctx, `SELECT s.submit_intent_id, a.job_id, a.storage_target_id, r.reservation_id, s.pre_add_sync_generation, s.pre_add_snapshot_revision, s.pre_add_snapshot_digest, s.pre_add_identity_fact
		FROM placement_attempts a
		JOIN race_jobs j ON j.job_id = a.job_id
		JOIN submit_intents s ON s.attempt_id = a.attempt_id
		JOIN capacity_reservations r ON r.attempt_id = a.attempt_id
		WHERE a.attempt_id = ?`, waiter.AttemptID).Scan(&submitIntentID, &jobID, &storageTargetID, &reservationID, &preGeneration, &preRevision, &preDigest, &preFact)
	if err != nil {
		return firstconfirm.Result{}, fmt.Errorf("load chain for attempt %s: %w", waiter.AttemptID, err)
	}
	request := firstconfirm.Request{
		BindingID:              "binding-" + waiter.AttemptID,
		AttemptID:              waiter.AttemptID,
		SubmitIntentID:         submitIntentID,
		ReservationID:          reservationID,
		JobID:                  jobID,
		StorageTargetID:        storageTargetID,
		IdentityKey:            waiter.IdentityKey,
		QBTorrentID:            waiter.IdentityKey,
		SyncGeneration:         int64(event.Generation),
		SnapshotRevision:       int64(event.Revision),
		SnapshotDigest:         SnapshotDigest(event.Snapshot),
		PreAddSyncGeneration:   preGeneration,
		PreAddSnapshotRevision: preRevision,
		PreAddSnapshotDigest:   preDigest,
		Now:                    now.UTC(),
	}
	// The pre-add identity fact is already validated by firstconfirm; the
	// extra evidence digest binds this confirmation to the snapshot bytes.
	request.EvidenceDigest = "sync-snapshot:" + request.SnapshotDigest
	result, err := firstconfirm.Confirm(ctx, coordinator.database, request)
	if err != nil {
		return firstconfirm.Result{}, fmt.Errorf("first confirm attempt %s: %w", waiter.AttemptID, err)
	}
	return result, nil
}

// sweep resolves UNKNOWN chains against the event snapshot. The reconciler's
// provider is instance-keyed; when several UNKNOWN chains for one instance
// disagree about identity presence the provider returns no evidence and the
// reconciler holds the chains (fail closed) instead of guessing.
func (coordinator *Coordinator) sweep(ctx context.Context, event Event, now time.Time) ([]unknownreconciler.Outcome, error) {
	type unknownChain struct {
		attemptID   string
		identityKey string
	}
	rows, err := coordinator.database.QueryContext(ctx, `SELECT a.attempt_id, a.identity_key FROM placement_attempts a JOIN race_jobs j ON j.job_id = a.job_id AND j.job_state = 'UNKNOWN' WHERE a.attempt_state = 'UNKNOWN' AND a.instance_id = ?`, event.InstanceID)
	if err != nil {
		return nil, fmt.Errorf("list unknown chains: %w", err)
	}
	defer rows.Close()
	chains := make([]unknownChain, 0)
	for rows.Next() {
		var chain unknownChain
		if err := rows.Scan(&chain.attemptID, &chain.identityKey); err != nil {
			return nil, err
		}
		chains = append(chains, chain)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	if len(chains) == 0 {
		return nil, nil
	}
	digest := SnapshotDigest(event.Snapshot)
	// Fail closed on ambiguity: if the UNKNOWN chains for this instance
	// disagree about identity presence in this snapshot, the reconciler gets
	// no evidence and holds every chain instead of guessing.
	present := make(map[string]bool, len(chains))
	allPresent, allAbsent := true, true
	qbTorrentID := ""
	for _, chain := range chains {
		_, seen := event.Snapshot.Torrents[chain.identityKey]
		present[chain.attemptID] = seen
		allPresent = allPresent && seen
		allAbsent = allAbsent && !seen
	}
	unambiguous := allPresent || allAbsent
	if allPresent {
		for _, chain := range chains {
			if qbTorrentID == "" {
				qbTorrentID = chain.identityKey
			}
		}
	}
	provider := eventSnapshotProvider{complete: unambiguous, snapshot: unknownreconciler.Snapshot{
		InstanceID:       event.InstanceID,
		SyncGeneration:   int64(event.Generation),
		SnapshotRevision: int64(event.Revision),
		SnapshotDigest:   digest,
		IdentityPresent:  unambiguous && allPresent,
		QBTorrentID:      qbTorrentID,
	}}
	return unknownreconciler.Sweep(ctx, coordinator.database, provider, unknownreconciler.SweepInput{Now: now.UTC(), InstanceID: event.InstanceID})
}

type eventSnapshotProvider struct {
	snapshot unknownreconciler.Snapshot
	complete bool
}

func (provider eventSnapshotProvider) FreshestComplete(_ context.Context, instanceID string) (unknownreconciler.Snapshot, bool) {
	return provider.snapshot, provider.complete && provider.snapshot.InstanceID == instanceID
}

// SnapshotDigest is the canonical digest of a sync snapshot: sha256 over its
// JSON encoding. The same derivation is used by the G1 lab evidence.
func SnapshotDigest(snapshot syncmerge.Snapshot) string {
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}
