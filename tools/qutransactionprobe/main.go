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
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
	"github.com/vertex-pro/vertex-pro/internal/dispatchflow"
	"github.com/vertex-pro/vertex-pro/internal/dispatchworker"
	"github.com/vertex-pro/vertex-pro/internal/firstconfirm"
	"github.com/vertex-pro/vertex-pro/internal/identitymatcher"
	"github.com/vertex-pro/vertex-pro/internal/placement"
	"github.com/vertex-pro/vertex-pro/internal/qbadd"
	"github.com/vertex-pro/vertex-pro/internal/qbprofile"
	"github.com/vertex-pro/vertex-pro/internal/qbresponse"
	"github.com/vertex-pro/vertex-pro/internal/qui/proxy"
	"github.com/vertex-pro/vertex-pro/internal/reservation"
	"github.com/vertex-pro/vertex-pro/internal/secrets"
	"github.com/vertex-pro/vertex-pro/internal/syncmerge"
	"github.com/vertex-pro/vertex-pro/internal/syncwaiter"
	"github.com/vertex-pro/vertex-pro/internal/torrentidentity"
	"github.com/vertex-pro/vertex-pro/internal/torrentparse"
	"github.com/vertex-pro/vertex-pro/internal/unknownreconciler"
)

const isolatedLabConfirmation = "I_CONFIRM_EMPTY_ISOLATED_LAB"

type report struct {
	Status  string `json:"status"`
	Profile string `json:"profile"`
	Safety  struct {
		ExplicitIsolatedLabOptIn bool `json:"explicit_isolated_lab_opt_in"`
		BaselineEmpty            bool `json:"baseline_empty"`
		StartInactive            bool `json:"start_inactive"`
		Trackerless              bool `json:"trackerless"`
		ProductionServicesCalled bool `json:"production_services_called"`
		DeleteOnlyAddedIdentity  bool `json:"delete_only_added_identity"`
		LabReturnedEmpty         bool `json:"lab_returned_empty"`
		TemporaryDatabaseRemoved bool `json:"temporary_database_removed"`
		SecretsRedacted          bool `json:"secrets_redacted"`
	} `json:"safety"`
	PlacementGate struct {
		ProfileState  string `json:"profile_state"`
		Selected      bool   `json:"selected"`
		RejectionCode string `json:"rejection_code"`
		LabBypass     string `json:"lab_bypass"`
	} `json:"placement_gate"`
	Fault struct {
		Mode                  string `json:"mode"`
		SenderCalls           int    `json:"sender_calls"`
		ReconcilerOutcome     string `json:"reconciler_outcome"`
		AutomaticRedispatches int    `json:"automatic_redispatches"`
		SyntheticResponse     bool   `json:"synthetic_response"`
	} `json:"fault"`
	Transaction struct {
		ReservationState        string `json:"reservation_state"`
		AttemptAfterReserve     string `json:"attempt_after_reserve"`
		DispatchHTTPStatus      int    `json:"dispatch_http_status"`
		DispatchFormat          string `json:"dispatch_format"`
		DispatchCommitState     string `json:"dispatch_commit_state"`
		DurableMarkerBeforeSend bool   `json:"durable_marker_before_send"`
		ResponseFactRecorded    bool   `json:"response_fact_recorded"`
		SyncMatched             bool   `json:"sync_matched"`
		SyncEvidenceSource      string `json:"sync_evidence_source"`
		ObservedInactive        bool   `json:"observed_inactive"`
		ObservedState           string `json:"observed_state"`
		WaiterClosed            bool   `json:"waiter_closed"`
		AttemptFinal            string `json:"attempt_final"`
		JobFinal                string `json:"job_final"`
		IntentDispatch          string `json:"intent_dispatch"`
		ReservationFinal        string `json:"reservation_final"`
		BindingCreated          bool   `json:"binding_created"`
		FirstConfirmReplay      bool   `json:"first_confirm_replay_idempotent"`
		EventCount              int    `json:"event_count"`
	} `json:"transaction"`
}

func main() {
	if err := run(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "qui transaction probe failed:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	if os.Getenv(kill9ChildEnv) == "1" {
		return runKill9Child(ctx)
	}
	if os.Getenv("VERTEX_PRO_G1_ISOLATED_LAB") != isolatedLabConfirmation {
		return errors.New("isolated lab confirmation is missing")
	}
	baseURL := strings.TrimSpace(os.Getenv("VERTEX_PRO_QUI_URL"))
	proxyKey := strings.TrimSpace(os.Getenv("VERTEX_PRO_QUI_PROXY_KEY"))
	if baseURL == "" || proxyKey == "" {
		return errors.New("VERTEX_PRO_QUI_URL and VERTEX_PRO_QUI_PROXY_KEY are required")
	}
	client, err := proxy.New(ctx, proxy.Config{
		BaseURL:    baseURL,
		ProxyKey:   secrets.SecretRef{Provider: "env", Key: "VERTEX_PRO_QUI_PROXY_KEY"},
		Store:      secrets.EnvStore{},
		HTTPClient: &http.Client{Timeout: 60 * time.Second},
	})
	if err != nil {
		return err
	}
	profile, err := qbprofile.Probe(ctx, client)
	if err != nil {
		return err
	}
	faultMode := strings.ToLower(strings.TrimSpace(os.Getenv("VERTEX_PRO_G1_FAULT_MODE")))
	if faultMode == "" {
		faultMode = "none"
	}
	if faultMode != "none" && faultMode != "response_loss" && faultMode != "timeout" && faultMode != "external_kill" && faultMode != "fixture_capture" && faultMode != "protocol_suite" && !isSyntheticTransportMode(faultMode) {
		return fmt.Errorf("unsupported VERTEX_PRO_G1_FAULT_MODE %q", faultMode)
	}
	baselineDelta, err := client.Fetch(ctx, 0)
	if err != nil {
		return fmt.Errorf("read baseline snapshot: %w", err)
	}
	baseline, err := syncmerge.Merge(syncmerge.Snapshot{}, baselineDelta)
	if err != nil {
		return err
	}
	if len(baseline.Snapshot.Torrents) != 0 {
		return fmt.Errorf("isolated lab baseline is not empty: %d tasks", len(baseline.Snapshot.Torrents))
	}

	placementResult, err := placement.Decide(placement.DecisionInput{
		IdentityKey: "g1-placement-gate", TargetMode: placement.TargetFixedInstance, TargetInstanceID: "lab-instance", RequiredBytes: 1,
		Weights:    placement.DecisionWeights{InstanceWeight: 1, FreeRatioWeight: 1, InflightPenaltyWeight: 1, CurrentDownloadLoadPenaltyWeight: 1, RecentAddLatencyPenaltyWeight: 1},
		Candidates: []placement.DecisionCandidate{{InstanceID: "lab-instance", StorageTargetID: "lab-target", CanPlace: true, HealthState: "AVAILABLE", ProfileState: profile.ProfileVerificationState, StorageState: "VERIFIED", CapacityState: "TRUSTED", InstanceWeight: 1, FreeRatioPPM: 1_000_000, EffectiveFreeBytes: 1 << 30, MaxInflight: 1}},
	})
	if err != nil {
		return err
	}
	rejection := ""
	if len(placementResult.Rejected) == 1 {
		rejection = placementResult.Rejected[0].Reason
	}
	if placementResult.Selected != nil || rejection != "PROFILE_NOT_VERIFIED" {
		return fmt.Errorf("production placement gate did not fail closed: selected=%v rejection=%s", placementResult.Selected != nil, rejection)
	}

	name := "vertex-pro-g1-transaction-" + strings.ToLower(profile.ID) + "-" + strings.ReplaceAll(faultMode, "_", "-") + "-v1"
	artifact := buildTrackerlessTorrent(name)
	parsed, err := torrentparse.Parse(artifact, torrentparse.DefaultLimits())
	if err != nil {
		return err
	}
	identity, err := torrentidentity.FromTorrent(parsed)
	if err != nil {
		return err
	}
	identityKey := strings.ToLower(identity.V1)
	if identityKey == "" {
		return errors.New("transaction fixture lacks v1 identity")
	}
	if faultMode == "protocol_suite" {
		return runProtocolSuite(ctx, client, profile, identity, artifact, name, baseline.Snapshot)
	}

	tempRoot, err := os.MkdirTemp("", "vertex-pro-g1-transaction-")
	if err != nil {
		return err
	}
	databasePath := filepath.Join(tempRoot, "transaction.db")
	database, err := openDatabase(ctx, databasePath)
	if err != nil {
		_ = os.RemoveAll(tempRoot)
		return err
	}
	removed := false
	defer func() {
		_ = database.Close()
		_ = os.RemoveAll(tempRoot)
	}()

	now := time.Now().UTC().Truncate(time.Second)
	baselineDigest := snapshotDigest(baseline.Snapshot)
	if faultMode == "fixture_capture" {
		fixtureDir := strings.TrimSpace(os.Getenv("VERTEX_PRO_FIXTURE_DIR"))
		if fixtureDir == "" {
			return errors.New("VERTEX_PRO_FIXTURE_DIR is required for fixture_capture")
		}
		return runFixtureCapture(ctx, client, profile, identity, artifact, name, fixtureDir)
	}
	if faultMode == "external_kill" {
		return runExternalKill(ctx, client, profile, identity, identityKey, name, rejection, baselineDigest)
	}
	if err := seedTransaction(ctx, database, identityKey, baselineDigest, now); err != nil {
		return err
	}
	reserved, err := reservation.ReserveAtomic(ctx, database, reservation.Request{
		JobID: "job-g1", AttemptID: "attempt-g1", SubmitIntentID: "intent-g1", ReservationID: "reservation-g1",
		IdentityKey: identityKey, InstanceID: "instance-g1", StorageTargetID: "target-g1", RequiredBytes: 1,
		CapacityEpochID: "epoch-g1", AllocationEpoch: 1, CapacityRevision: 1, SnapshotGeneration: 1,
		SnapshotRevision: 1, SnapshotDigest: baselineDigest, RequestFingerprint: "request-g1", TargetFingerprint: "target-g1",
		PreAddIdentityFact: reservation.IdentityAbsent, FinalCandidateRecheckRevision: 1, MaxInflight: 1,
		Now: now.Format(time.RFC3339),
	})
	if err != nil {
		return fmt.Errorf("reserve transaction: %w", err)
	}
	if _, err := syncwaiter.Register(ctx, database, syncwaiter.Request{
		WaiterID: "waiter-g1", AttemptID: reserved.AttemptID, InstanceID: "instance-g1", RegisteredGeneration: 1,
		SourceSnapshotRevision: 1, IdentityKey: identityKey, ConfirmationDeadline: now.Add(2 * time.Minute).Format(time.RFC3339),
		ReconciliationDeadline: now.Add(5 * time.Minute).Format(time.RFC3339), CreatedAt: now.Format(time.RFC3339),
	}); err != nil {
		return fmt.Errorf("register sync waiter: %w", err)
	}
	addRequest, err := qbadd.BuildRequest(profile, qbadd.TorrentArtifact{Filename: name + ".torrent", Content: artifact}, qbadd.AddParams{StartInactive: true})
	if err != nil {
		return err
	}
	injectedSender := &faultSender{delegate: client, mode: faultMode, database: database}
	flow, err := dispatchflow.New(database, injectedSender, func() time.Time { return time.Now().UTC() })
	if err != nil {
		return err
	}
	dispatchContext := ctx
	cancelDispatch := func() {}
	if faultMode == "timeout" {
		dispatchContext, cancelDispatch = context.WithTimeout(ctx, 250*time.Millisecond)
	}
	dispatched, dispatchErr := flow.Run(dispatchContext, dispatchflow.Request{AttemptID: "attempt-g1", SubmitIntentID: "intent-g1", WaiterID: "waiter-g1", APIVersion: profile.WebAPIVersion, HTTPRequest: addRequest})
	cancelDispatch()
	networkFault := faultMode == "response_loss" || faultMode == "timeout"
	syntheticTransport := isSyntheticTransportMode(faultMode)
	if faultMode == "none" && dispatchErr != nil {
		return fmt.Errorf("dispatch transaction: %w", dispatchErr)
	}
	if networkFault && dispatched.Dispatch.Response.ResponseFormat != "transport_error" {
		return fmt.Errorf("fault injection response format=%s, want transport_error", dispatched.Dispatch.Response.ResponseFormat)
	}
	if syntheticTransport && dispatchErr != nil {
		return fmt.Errorf("synthetic transport dispatch returned error: %w", dispatchErr)
	}
	expectedCommitState := "AWAITING_CONFIRMATION"
	if faultMode != "none" && faultMode != "transport_202" {
		expectedCommitState = "UNKNOWN"
	}
	if dispatched.Commit.State != expectedCommitState {
		return fmt.Errorf("dispatch committed %s, want %s", dispatched.Commit.State, expectedCommitState)
	}
	if injectedSender.calls != 1 {
		return fmt.Errorf("dispatch sender called %d times, want exactly once", injectedSender.calls)
	}
	if injectedSender.markerErr != nil {
		return fmt.Errorf("inspect durable marker before send: %w", injectedSender.markerErr)
	}
	if !injectedSender.markerObserved {
		return errors.New("durable ADD_SENT/STARTED marker was not visible before sender invocation")
	}

	current := baseline.Snapshot
	match := identitymatcher.Result{Status: identitymatcher.StatusNotFound}
	observedState := ""
	syncEvidenceSource := "QBITTORRENT_SYNC"
	if syntheticTransport {
		current.RID = 1
		if faultMode == "transport_202" {
			match = identitymatcher.Result{Status: identitymatcher.StatusMatched, CandidateID: identityKey, MatchedByV1: true}
			observedState = "stoppedDL"
		}
		syncEvidenceSource = "SYNTHETIC_TRANSPORT_FIXTURE"
	} else {
		observationWindow := 45 * time.Second
		if faultMode == "timeout" {
			observationWindow = 3 * time.Second
		}
		deadline := time.Now().Add(observationWindow)
		for time.Now().Before(deadline) {
			delta, fetchErr := client.Fetch(ctx, current.RID)
			if fetchErr != nil {
				return fetchErr
			}
			merged, mergeErr := syncmerge.Merge(current, delta)
			if mergeErr != nil {
				return mergeErr
			}
			current = merged.Snapshot
			match, err = identitymatcher.Match(identitymatcher.Request{Identity: identity}, snapshotCandidates(current))
			if err != nil {
				return err
			}
			if match.Status == identitymatcher.StatusMatched {
				observedState, _ = current.Torrents[match.CandidateID].Fields["state"].(string)
				if inactiveState(observedState) {
					break
				}
			}
			time.Sleep(500 * time.Millisecond)
		}
		if faultMode == "timeout" {
			if match.Status != identitymatcher.StatusNotFound {
				return fmt.Errorf("timeout-before-forward unexpectedly created identity: %s", match.Status)
			}
		} else if match.Status != identitymatcher.StatusMatched || !inactiveState(observedState) {
			return fmt.Errorf("transaction identity did not reach inactive state: %s/%s", match.Status, observedState)
		}
	}
	afterDigest := snapshotDigest(current)
	if afterDigest == baselineDigest {
		digest := sha256.Sum256([]byte(baselineDigest + ":fresh-after-dispatch:" + faultMode))
		afterDigest = hex.EncodeToString(digest[:])
	}

	replayIdempotent := false
	reconcilerOutcome := ""
	if faultMode == "none" || faultMode == "transport_202" {
		notification, notifyErr := syncwaiter.Notify(ctx, database, "waiter-g1", syncwaiter.Notification{Generation: 1, SnapshotRevision: 2, ObservedIdentityKey: identityKey}, time.Now().UTC().Format(time.RFC3339))
		if notifyErr != nil {
			return fmt.Errorf("notify waiter: %w", notifyErr)
		}
		if !notification.IdentityMatched || notification.Ignored {
			return fmt.Errorf("waiter notification = %+v", notification)
		}
		confirmRequest := firstconfirm.Request{
			BindingID: "binding-g1", AttemptID: "attempt-g1", SubmitIntentID: "intent-g1", ReservationID: "reservation-g1",
			JobID: "job-g1", StorageTargetID: "target-g1", IdentityKey: identityKey, QBTorrentID: match.CandidateID,
			SyncGeneration: 1, SnapshotRevision: 2, SnapshotDigest: afterDigest,
			PreAddSyncGeneration: 1, PreAddSnapshotRevision: 1, PreAddSnapshotDigest: baselineDigest,
			EvidenceDigest: "g1-sync-confirmation", Now: time.Now().UTC(),
		}
		confirmed, confirmErr := firstconfirm.Confirm(ctx, database, confirmRequest)
		if confirmErr != nil {
			return fmt.Errorf("first confirm: %w", confirmErr)
		}
		if confirmed.Idempotent {
			return errors.New("first confirmation unexpectedly idempotent")
		}
		replay, replayErr := firstconfirm.Confirm(ctx, database, confirmRequest)
		if replayErr != nil {
			return fmt.Errorf("replay first confirm: %w", replayErr)
		}
		replayIdempotent = replay.Idempotent
		if faultMode == "transport_202" {
			reconcilerOutcome = "FIRST_CONFIRM_AFTER_ACCEPTED"
		}
	} else {
		provider := fixedSnapshotProvider{snapshot: unknownreconciler.Snapshot{
			InstanceID: "instance-g1", SyncGeneration: 1, SnapshotRevision: 2, SnapshotDigest: afterDigest,
			IdentityPresent: match.Status == identitymatcher.StatusMatched, QBTorrentID: match.CandidateID,
		}}
		outcomes, sweepErr := unknownreconciler.Sweep(ctx, database, provider, unknownreconciler.SweepInput{Now: time.Now().UTC()})
		if sweepErr != nil {
			return fmt.Errorf("reconcile injected fault: %w", sweepErr)
		}
		if len(outcomes) != 1 {
			return fmt.Errorf("reconciler outcomes=%d, want 1", len(outcomes))
		}
		reconcilerOutcome = outcomes[0].Outcome
		expectedOutcome := unknownreconciler.OutcomeConfirmed
		if faultMode == "timeout" || faultMode == "transport_204" || faultMode == "transport_409" {
			expectedOutcome = unknownreconciler.OutcomeNoEffect
		}
		if reconcilerOutcome != expectedOutcome {
			return fmt.Errorf("reconciler outcome=%s, want %s", reconcilerOutcome, expectedOutcome)
		}
	}

	finalFacts, err := readFinalFacts(ctx, database)
	if err != nil {
		return err
	}
	if err := validateFinalFacts(faultMode, finalFacts); err != nil {
		return err
	}
	if match.Status == identitymatcher.StatusMatched && !syntheticTransport {
		deleteStatus, deleteErr := deleteIdentity(ctx, baseURL, proxyKey, match.CandidateID)
		if deleteErr != nil {
			return deleteErr
		}
		if deleteStatus != http.StatusOK && deleteStatus != http.StatusNoContent {
			return fmt.Errorf("cleanup delete returned HTTP %d", deleteStatus)
		}
	}
	finalEmpty := false
	for attempt := 0; attempt < 30; attempt++ {
		delta, fetchErr := client.Fetch(ctx, 0)
		if fetchErr != nil {
			return fetchErr
		}
		merged, mergeErr := syncmerge.Merge(syncmerge.Snapshot{}, delta)
		if mergeErr != nil {
			return mergeErr
		}
		if len(merged.Snapshot.Torrents) == 0 {
			finalEmpty = true
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if !finalEmpty {
		return errors.New("lab did not return to an empty task set")
	}
	if err := database.Close(); err != nil {
		return err
	}
	if err := os.RemoveAll(tempRoot); err != nil {
		return err
	}
	removed = true

	output := report{Status: "PASS", Profile: profile.ID}
	output.Safety.ExplicitIsolatedLabOptIn = true
	output.Safety.BaselineEmpty = true
	output.Safety.StartInactive = true
	output.Safety.Trackerless = true
	output.Safety.ProductionServicesCalled = false
	output.Safety.DeleteOnlyAddedIdentity = true
	output.Safety.LabReturnedEmpty = finalEmpty
	output.Safety.TemporaryDatabaseRemoved = removed
	output.Safety.SecretsRedacted = true
	output.PlacementGate.ProfileState = profile.ProfileVerificationState
	output.PlacementGate.Selected = false
	output.PlacementGate.RejectionCode = rejection
	output.PlacementGate.LabBypass = "TRANSACTION_FIXTURE_ONLY_NO_PROFILE_PROMOTION"
	output.Transaction.ReservationState = "ACTIVE"
	output.Transaction.AttemptAfterReserve = "RESERVED"
	output.Fault.Mode = faultMode
	output.Fault.SenderCalls = injectedSender.calls
	output.Fault.ReconcilerOutcome = reconcilerOutcome
	output.Fault.SyntheticResponse = syntheticTransport
	output.Fault.AutomaticRedispatches = injectedSender.calls - 1
	output.Transaction.DispatchHTTPStatus = dispatched.Dispatch.Response.HTTPStatus
	output.Transaction.DispatchFormat = dispatched.Dispatch.Response.ResponseFormat
	output.Transaction.DispatchCommitState = dispatched.Commit.State
	output.Transaction.DurableMarkerBeforeSend = injectedSender.markerObserved
	output.Transaction.ResponseFactRecorded = finalFacts.responseFactRecorded
	output.Transaction.SyncMatched = match.Status == identitymatcher.StatusMatched
	output.Transaction.SyncEvidenceSource = syncEvidenceSource
	output.Transaction.ObservedInactive = inactiveState(observedState)
	output.Transaction.ObservedState = observedState
	output.Transaction.WaiterClosed = finalFacts.waiterState == "CLOSED"
	output.Transaction.AttemptFinal = finalFacts.attemptState
	output.Transaction.JobFinal = finalFacts.jobState
	output.Transaction.IntentDispatch = finalFacts.intentDispatch
	output.Transaction.ReservationFinal = finalFacts.reservationState
	output.Transaction.BindingCreated = finalFacts.bindingCount == 1
	output.Transaction.FirstConfirmReplay = replayIdempotent
	output.Transaction.EventCount = finalFacts.eventCount
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(output)
}

func openDatabase(ctx context.Context, path string) (*sql.DB, error) {
	database, err := db.Open(ctx, db.Config{Path: path, MaxOpenConns: 1, MaxIdleConns: 1})
	if err != nil {
		return nil, err
	}
	runner, err := db.DefaultRunner()
	if err != nil {
		_ = database.Close()
		return nil, err
	}
	if err := runner.Apply(ctx, database); err != nil {
		_ = database.Close()
		return nil, err
	}
	return database, nil
}

func seedTransaction(ctx context.Context, database *sql.DB, identityKey, digest string, now time.Time) error {
	timestamp := now.Format(time.RFC3339)
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO storage_targets(storage_target_id, instance_id, path, storage_verification_state, created_at, updated_at) VALUES ('target-g1','instance-g1','/isolated-g1','VERIFIED',?,?)`, []any{timestamp, timestamp}},
		{`INSERT INTO capacity_epochs(capacity_epoch_id, storage_target_id, allocation_epoch, capacity_state, physical_free_bytes, effective_free_bytes, snapshot_generation, snapshot_revision, snapshot_digest, created_at) VALUES ('epoch-g1','target-g1',1,'TRUSTED',1073741824,1073741824,1,1,?,?)`, []any{digest, timestamp}},
		{`INSERT INTO sync_instance_state(instance_id, sync_state, sync_generation, rid, snapshot_revision, snapshot_digest, updated_at) VALUES ('instance-g1','READY',1,0,1,?,?)`, []any{digest, timestamp}},
		{`INSERT INTO sync_snapshots(instance_id, sync_generation, snapshot_revision, rid, snapshot_digest, snapshot_path, is_complete, created_at) VALUES ('instance-g1',1,1,0,?,'/isolated-g1/baseline.json',1,?)`, []any{digest, timestamp}},
		{`INSERT INTO race_jobs(job_id, source_id, identity_key, job_state, rule_state, torrent_state, placement_state, participation_deadline, observation_deadline, created_at, updated_at) VALUES ('job-g1','source-g1',?,'READY','MATCHED','READY','PREPARED',?,?,?,?)`, []any{identityKey, now.Add(time.Hour).Format(time.RFC3339), now.Add(time.Hour).Format(time.RFC3339), timestamp, timestamp}},
	}
	for _, statement := range statements {
		if _, err := database.ExecContext(ctx, statement.query, statement.args...); err != nil {
			return err
		}
	}
	return nil
}

type finalFacts struct {
	attemptState         string
	jobState             string
	intentDispatch       string
	reservationState     string
	waiterState          string
	responseFactRecorded bool
	bindingCount         int
	eventCount           int
}

func readFinalFacts(ctx context.Context, database *sql.DB) (finalFacts, error) {
	var facts finalFacts
	var responseFactAt sql.NullString
	if err := database.QueryRowContext(ctx, `SELECT a.attempt_state, j.job_state, s.dispatch_state, r.reservation_state, w.waiter_state, s.response_fact_at FROM placement_attempts a JOIN race_jobs j ON j.job_id=a.job_id JOIN submit_intents s ON s.attempt_id=a.attempt_id JOIN capacity_reservations r ON r.attempt_id=a.attempt_id JOIN sync_waiters w ON w.attempt_id=a.attempt_id WHERE a.attempt_id='attempt-g1'`).Scan(&facts.attemptState, &facts.jobState, &facts.intentDispatch, &facts.reservationState, &facts.waiterState, &responseFactAt); err != nil {
		return facts, err
	}
	facts.responseFactRecorded = responseFactAt.Valid
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM capacity_task_bindings`).Scan(&facts.bindingCount); err != nil {
		return facts, err
	}
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM race_events WHERE attempt_id='attempt-g1'`).Scan(&facts.eventCount); err != nil {
		return facts, err
	}
	return facts, nil
}

func validateFinalFacts(faultMode string, facts finalFacts) error {
	expectedAttempt := "HASH_SEEN"
	expectedJob := "HASH_SEEN"
	expectedReservation := "REFLECTING"
	expectedBindings := 1
	if faultMode == "response_loss" {
		expectedAttempt = "RECONCILED_CONFIRMED"
		expectedJob = "RECONCILED_CONFIRMED"
	}
	if faultMode == "timeout" || faultMode == "transport_204" || faultMode == "transport_409" {
		expectedAttempt = "RECONCILED_NO_EFFECT"
		expectedJob = "RECONCILED_NO_EFFECT"
		expectedReservation = "RELEASED"
		expectedBindings = 0
	}
	if facts.attemptState != expectedAttempt || facts.jobState != expectedJob || facts.intentDispatch != "STARTED" || !facts.responseFactRecorded || facts.reservationState != expectedReservation || facts.waiterState != "CLOSED" || facts.bindingCount != expectedBindings {
		return fmt.Errorf("unexpected final facts for %s: %+v", faultMode, facts)
	}
	return nil
}

type fixedSnapshotProvider struct {
	snapshot unknownreconciler.Snapshot
}

func (provider fixedSnapshotProvider) FreshestComplete(context.Context, string) (unknownreconciler.Snapshot, bool) {
	return provider.snapshot, true
}

type faultSender struct {
	delegate       dispatchworker.Sender
	mode           string
	database       *sql.DB
	calls          int
	markerObserved bool
	markerErr      error
}

func (sender *faultSender) Send(ctx context.Context, request *http.Request) (qbresponse.RawResponse, error) {
	sender.calls++
	var attemptState, attemptDispatch, sideEffect, jobState, intentDispatch string
	var startedAt string
	var responseFactAt sql.NullString
	sender.markerErr = sender.database.QueryRowContext(ctx, `SELECT a.attempt_state, a.dispatch_state, COALESCE(a.side_effect_fact,''), j.job_state, s.dispatch_state, s.dispatch_started_at, s.response_fact_at FROM placement_attempts a JOIN race_jobs j ON j.job_id=a.job_id JOIN submit_intents s ON s.attempt_id=a.attempt_id WHERE a.attempt_id='attempt-g1'`).Scan(&attemptState, &attemptDispatch, &sideEffect, &jobState, &intentDispatch, &startedAt, &responseFactAt)
	if sender.markerErr != nil {
		return qbresponse.RawResponse{}, sender.markerErr
	}
	sender.markerObserved = attemptState == "ADD_SENT" && attemptDispatch == "STARTED" && sideEffect == "POSSIBLE_EFFECT" && jobState == "ADD_SENT" && intentDispatch == "STARTED" && startedAt != "" && !responseFactAt.Valid
	if !sender.markerObserved {
		return qbresponse.RawResponse{}, errors.New("durable dispatch marker mismatch before network send")
	}
	switch sender.mode {
	case "none":
		return sender.delegate.Send(ctx, request)
	case "response_loss":
		raw, err := sender.delegate.Send(ctx, request)
		if err != nil {
			return raw, err
		}
		return raw, errors.New("injected response loss after upstream accepted request")
	case "timeout":
		<-ctx.Done()
		return qbresponse.RawResponse{}, ctx.Err()
	case "transport_202":
		return qbresponse.RawResponse{StatusCode: http.StatusAccepted, Body: []byte(`{"pending_count":1}`)}, nil
	case "transport_204":
		return qbresponse.RawResponse{StatusCode: http.StatusNoContent}, nil
	case "transport_409":
		return qbresponse.RawResponse{StatusCode: http.StatusConflict, Body: []byte(`{"failure_count":1}`)}, nil
	default:
		return qbresponse.RawResponse{}, fmt.Errorf("unsupported fault mode %q", sender.mode)
	}
}

func deleteIdentity(ctx context.Context, baseURL, proxyKey, torrentID string) (int, error) {
	endpoint := strings.TrimRight(baseURL, "/") + "/proxy/" + url.PathEscape(proxyKey) + "/api/v2/torrents/delete"
	body := url.Values{"hashes": {torrentID}, "deleteFiles": {"true"}}.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(body))
	if err != nil {
		return 0, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := (&http.Client{Timeout: 30 * time.Second}).Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	return response.StatusCode, nil
}

func snapshotDigest(snapshot syncmerge.Snapshot) string {
	encoded, _ := json.Marshal(snapshot)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
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

func snapshotCandidates(snapshot syncmerge.Snapshot) []identitymatcher.Candidate {
	ids := make([]string, 0, len(snapshot.Torrents))
	for id := range snapshot.Torrents {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	candidates := make([]identitymatcher.Candidate, 0, len(ids))
	for _, id := range ids {
		identity := torrentidentity.Identity{}
		switch len(id) {
		case 40:
			identity.Version, identity.V1 = torrentidentity.VersionV1, id
		case 64:
			identity.Version, identity.V2 = torrentidentity.VersionV2, id
		default:
			continue
		}
		candidates = append(candidates, identitymatcher.Candidate{ID: id, QBTorrentID: id, Identity: identity})
	}
	return candidates
}

func isSyntheticTransportMode(mode string) bool {
	switch mode {
	case "transport_202", "transport_204", "transport_409":
		return true
	default:
		return false
	}
}

func inactiveState(state string) bool {
	normalized := strings.ToLower(strings.TrimSpace(state))
	return strings.HasPrefix(normalized, "stopped") || strings.HasPrefix(normalized, "paused")
}
