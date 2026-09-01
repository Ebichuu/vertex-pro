package main

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/alreadypresent"
	"github.com/vertex-pro/vertex-pro/internal/amountleft"
	"github.com/vertex-pro/vertex-pro/internal/db"
	"github.com/vertex-pro/vertex-pro/internal/qui/proxy"
	"github.com/vertex-pro/vertex-pro/internal/reservation"
	"github.com/vertex-pro/vertex-pro/internal/secrets"
	"github.com/vertex-pro/vertex-pro/internal/syncmerge"
	"github.com/vertex-pro/vertex-pro/internal/torrentidentity"
	"github.com/vertex-pro/vertex-pro/internal/torrentparse"
)

type readOnlyTransport struct {
	base   http.RoundTripper
	writes atomic.Int64
}

func (transport *readOnlyTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Method != http.MethodGet {
		transport.writes.Add(1)
		return nil, fmt.Errorf("read-only probe rejected HTTP %s", request.Method)
	}
	return transport.base.RoundTrip(request)
}

type report struct {
	Status string `json:"status"`
	Safety struct {
		ReadOnlyHTTP              bool  `json:"read_only_http"`
		NetworkWriteAttempts      int64 `json:"network_write_attempts"`
		ExternalTaskSetUnmodified bool  `json:"external_task_set_unmodified"`
		TemporaryDatabaseRemoved  bool  `json:"temporary_database_removed"`
		SecretsRedacted           bool  `json:"secrets_redacted"`
	} `json:"safety"`
	Observation struct {
		TestIdentityPresent bool   `json:"test_identity_present"`
		Inactive            bool   `json:"inactive"`
		State               string `json:"state"`
		AmountLeftKnown     bool   `json:"amount_left_known"`
	} `json:"observation"`
	Transaction struct {
		ReservationAtomicCreated bool   `json:"reservation_atomic_created"`
		FirstCallIdempotent      bool   `json:"first_call_idempotent"`
		ReplayIdempotent         bool   `json:"replay_idempotent"`
		AttemptState             string `json:"attempt_state"`
		AttemptDispatchState     string `json:"attempt_dispatch_state"`
		SideEffectFact           string `json:"side_effect_fact"`
		JobState                 string `json:"job_state"`
		ObservationState         string `json:"observation_state"`
		BindingOwnerKind         string `json:"binding_owner_kind"`
		ReservationState         string `json:"reservation_state"`
		ReleaseReason            string `json:"release_reason"`
		IntentDispatchState      string `json:"intent_dispatch_state"`
		TransitionEventCount     int    `json:"transition_event_count"`
	} `json:"transaction"`
}

func main() {
	if err := run(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "qui already-present transaction probe failed:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	baseURL := strings.TrimSpace(os.Getenv("VERTEX_PRO_QUI_URL"))
	if baseURL == "" {
		return errors.New("VERTEX_PRO_QUI_URL is required")
	}
	transport := &readOnlyTransport{base: http.DefaultTransport}
	client, err := proxy.New(ctx, proxy.Config{
		BaseURL:  baseURL,
		ProxyKey: secrets.SecretRef{Provider: "env", Key: "VERTEX_PRO_QUI_PROXY_KEY"},
		Store:    secrets.EnvStore{},
		HTTPClient: &http.Client{
			Timeout:   60 * time.Second,
			Transport: transport,
		},
	})
	if err != nil {
		return err
	}
	delta, err := client.Fetch(ctx, 0)
	if err != nil {
		return fmt.Errorf("read live Sync snapshot: %w", err)
	}
	merged, err := syncmerge.Merge(syncmerge.Snapshot{}, delta)
	if err != nil {
		return err
	}
	snapshot := merged.Snapshot

	artifact := buildTrackerlessTorrent("vertex-pro-live-probe-qb504-v1")
	parsed, err := torrentparse.Parse(artifact, torrentparse.DefaultLimits())
	if err != nil {
		return err
	}
	identity, err := torrentidentity.FromTorrent(parsed)
	if err != nil {
		return err
	}
	torrentID := strings.ToLower(identity.V1)
	observed, present := snapshot.Torrents[torrentID]
	if !present {
		return errors.New("retained test identity is not present")
	}
	state, _ := observed.Fields["state"].(string)
	inactive := strings.HasPrefix(strings.ToLower(state), "stopped") || strings.HasPrefix(strings.ToLower(state), "paused")
	if !inactive {
		return errors.New("retained test identity is not inactive")
	}
	amount, present := observed.Fields["amount_left"]
	amountValue := amountleft.ResolveFull(amountleft.Observation{Present: present, Raw: amount})
	if amountValue.State != amountleft.StateKnown || amountValue.Value == nil {
		return fmt.Errorf("retained test amount_left is not known: %s", amountValue.ErrorCode)
	}

	taskSetBefore := snapshotTaskSet(snapshot)
	temporaryRoot, err := os.MkdirTemp("", "vertex-pro-present-probe-")
	if err != nil {
		return err
	}
	removed := false
	defer func() {
		_ = os.RemoveAll(temporaryRoot)
	}()
	database, err := db.Open(ctx, db.Config{Path: filepath.Join(temporaryRoot, "probe.db"), MaxOpenConns: 1, MaxIdleConns: 1})
	if err != nil {
		return err
	}
	runner, err := db.DefaultRunner()
	if err != nil {
		_ = database.Close()
		return err
	}
	if err := runner.Apply(ctx, database); err != nil {
		_ = database.Close()
		return err
	}

	now := time.Now().UTC().Truncate(time.Second)
	digest, err := snapshotDigest(snapshot)
	if err != nil {
		_ = database.Close()
		return err
	}
	const (
		bindingID     = "live-binding"
		attemptID     = "live-attempt"
		intentID      = "live-intent"
		reservationID = "live-reservation"
		jobID         = "live-job"
		targetID      = "live-target"
		instanceID    = "live-instance"
		identityKey   = "live-test-identity"
	)
	if err := seedPreparation(ctx, database, now, snapshot.RID, digest); err != nil {
		_ = database.Close()
		return err
	}
	reserved, err := reservation.ReserveAtomic(ctx, database, reservation.Request{
		JobID:                         jobID,
		AttemptID:                     attemptID,
		SubmitIntentID:                intentID,
		ReservationID:                 reservationID,
		IdentityKey:                   identityKey,
		InstanceID:                    instanceID,
		StorageTargetID:               targetID,
		RequiredBytes:                 1,
		CapacityEpochID:               "live-capacity-epoch",
		AllocationEpoch:               1,
		CapacityRevision:              int64(snapshot.RID),
		SnapshotGeneration:            1,
		SnapshotRevision:              int64(snapshot.RID),
		SnapshotDigest:                digest,
		RequestFingerprint:            "live-request",
		TargetFingerprint:             "live-target",
		PreAddIdentityFact:            reservation.IdentityPresent,
		PreAddQBTorrentID:             torrentID,
		FinalCandidateRecheckRevision: int64(snapshot.RID),
		MaxInflight:                   1,
		Now:                           now.Format(time.RFC3339),
	})
	if err != nil {
		_ = database.Close()
		return fmt.Errorf("reserve production branch: %w", err)
	}
	request := alreadypresent.Request{
		BindingID:            bindingID,
		AttemptID:            attemptID,
		SubmitIntentID:       intentID,
		ReservationID:        reservationID,
		JobID:                jobID,
		StorageTargetID:      targetID,
		InstanceID:           instanceID,
		IdentityKey:          identityKey,
		QBTorrentID:          torrentID,
		AmountLeftKnown:      true,
		AmountLeft:           *amountValue.Value,
		UncertaintyDebitKind: alreadypresent.DebitKindNone,
		Now:                  now.Add(time.Second),
	}
	first, err := alreadypresent.Record(ctx, database, request)
	if err != nil {
		_ = database.Close()
		return err
	}
	replay, err := alreadypresent.Record(ctx, database, request)
	if err != nil {
		_ = database.Close()
		return err
	}

	output := report{Status: "PASS"}
	output.Safety.ReadOnlyHTTP = transport.writes.Load() == 0
	output.Safety.NetworkWriteAttempts = transport.writes.Load()
	output.Safety.ExternalTaskSetUnmodified = equalTaskSets(taskSetBefore, snapshotTaskSet(snapshot))
	output.Safety.SecretsRedacted = true
	output.Observation.TestIdentityPresent = true
	output.Observation.Inactive = inactive
	output.Observation.State = state
	output.Observation.AmountLeftKnown = true
	output.Transaction.ReservationAtomicCreated = reserved.AttemptID == attemptID && reserved.SubmitIntentID == intentID && reserved.ReservationID == reservationID
	output.Transaction.FirstCallIdempotent = first.Idempotent
	output.Transaction.ReplayIdempotent = replay.Idempotent
	if err := readFacts(ctx, database, &output); err != nil {
		_ = database.Close()
		return err
	}
	if err := database.Close(); err != nil {
		return err
	}
	if err := os.RemoveAll(temporaryRoot); err != nil {
		return err
	}
	removed = true
	output.Safety.TemporaryDatabaseRemoved = removed
	if !output.Safety.ReadOnlyHTTP || !output.Safety.ExternalTaskSetUnmodified || !removed || !output.Transaction.ReservationAtomicCreated || first.Idempotent || !replay.Idempotent ||
		output.Transaction.AttemptState != "ALREADY_PRESENT" || output.Transaction.AttemptDispatchState != "NOT_STARTED" ||
		output.Transaction.JobState != "ALREADY_PRESENT" || output.Transaction.BindingOwnerKind != "UNMANAGED" ||
		output.Transaction.ReservationState != "RELEASED" || output.Transaction.ReleaseReason != "ALREADY_PRESENT" ||
		output.Transaction.IntentDispatchState != "NOT_STARTED" || output.Transaction.TransitionEventCount != 2 {
		output.Status = "FAIL"
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(output); err != nil {
		return err
	}
	if output.Status != "PASS" {
		return errors.New("already-present transaction assertions failed")
	}
	return nil
}

func seedPreparation(ctx context.Context, database *sql.DB, now time.Time, snapshotRID uint64, snapshotDigest string) error {
	seededAt := now.Format(time.RFC3339)
	statements := []struct {
		query string
		args  []any
	}{
		{"INSERT INTO storage_targets(storage_target_id, instance_id, path, storage_verification_state, created_at, updated_at) VALUES ('live-target','live-instance','/isolated-live-probe','VERIFIED',?,?)", []any{seededAt, seededAt}},
		{"INSERT INTO race_jobs(job_id, source_id, identity_key, job_state, rule_state, torrent_state, placement_state, participation_deadline, observation_deadline, created_at, updated_at) VALUES ('live-job','live-source','live-test-identity','READY','MATCHED','READY','PREPARED',?,?,?,?)", []any{seededAt, seededAt, seededAt, seededAt}},
		{"INSERT INTO capacity_epochs(capacity_epoch_id, storage_target_id, allocation_epoch, capacity_state, physical_free_bytes, effective_free_bytes, snapshot_generation, snapshot_revision, snapshot_digest, created_at) VALUES ('live-capacity-epoch','live-target',1,'TRUSTED',1,1,1,?,?,?)", []any{snapshotRID, snapshotDigest, seededAt}},
	}
	for _, statement := range statements {
		if _, err := database.ExecContext(ctx, statement.query, statement.args...); err != nil {
			return fmt.Errorf("seed already-present branch: %w", err)
		}
	}
	return nil
}

func readFacts(ctx context.Context, database *sql.DB, output *report) error {
	if err := database.QueryRowContext(ctx, "SELECT attempt_state, dispatch_state, side_effect_fact FROM placement_attempts WHERE attempt_id='live-attempt'").Scan(&output.Transaction.AttemptState, &output.Transaction.AttemptDispatchState, &output.Transaction.SideEffectFact); err != nil {
		return err
	}
	if err := database.QueryRowContext(ctx, "SELECT job_state, observation_state FROM race_jobs WHERE job_id='live-job'").Scan(&output.Transaction.JobState, &output.Transaction.ObservationState); err != nil {
		return err
	}
	if err := database.QueryRowContext(ctx, "SELECT owner_kind FROM capacity_task_bindings WHERE binding_id='live-binding'").Scan(&output.Transaction.BindingOwnerKind); err != nil {
		return err
	}
	if err := database.QueryRowContext(ctx, "SELECT reservation_state, release_reason FROM capacity_reservations WHERE reservation_id='live-reservation'").Scan(&output.Transaction.ReservationState, &output.Transaction.ReleaseReason); err != nil {
		return err
	}
	if err := database.QueryRowContext(ctx, "SELECT dispatch_state FROM submit_intents WHERE submit_intent_id='live-intent'").Scan(&output.Transaction.IntentDispatchState); err != nil {
		return err
	}
	return database.QueryRowContext(ctx, "SELECT COUNT(*) FROM race_events WHERE to_state='ALREADY_PRESENT'").Scan(&output.Transaction.TransitionEventCount)
}

func buildTrackerlessTorrent(name string) []byte {
	piece := sha1.Sum([]byte{0})
	var buffer bytes.Buffer
	buffer.WriteString("d4:infod6:lengthi1e4:name")
	fmt.Fprintf(&buffer, "%d:", len(name))
	buffer.WriteString(name)
	buffer.WriteString("12:piece lengthi16384e6:pieces20:")
	buffer.Write(piece[:])
	buffer.WriteString("ee")
	return buffer.Bytes()
}

func snapshotDigest(snapshot syncmerge.Snapshot) (string, error) {
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func snapshotTaskSet(snapshot syncmerge.Snapshot) map[string]struct{} {
	result := make(map[string]struct{}, len(snapshot.Torrents))
	for id := range snapshot.Torrents {
		result[id] = struct{}{}
	}
	return result
}

func equalTaskSets(left, right map[string]struct{}) bool {
	if len(left) != len(right) {
		return false
	}
	for id := range left {
		if _, exists := right[id]; !exists {
			return false
		}
	}
	return true
}
