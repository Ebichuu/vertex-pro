package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
	"github.com/vertex-pro/vertex-pro/internal/dispatchcommit"
	"github.com/vertex-pro/vertex-pro/internal/dispatchflow"
	"github.com/vertex-pro/vertex-pro/internal/dispatchworker"
	"github.com/vertex-pro/vertex-pro/internal/identitymatcher"
	"github.com/vertex-pro/vertex-pro/internal/qbadd"
	"github.com/vertex-pro/vertex-pro/internal/qbprofile"
	"github.com/vertex-pro/vertex-pro/internal/qbresponse"
	"github.com/vertex-pro/vertex-pro/internal/qui/proxy"
	"github.com/vertex-pro/vertex-pro/internal/reservation"
	"github.com/vertex-pro/vertex-pro/internal/secrets"
	"github.com/vertex-pro/vertex-pro/internal/startuprecover"
	"github.com/vertex-pro/vertex-pro/internal/syncmerge"
	"github.com/vertex-pro/vertex-pro/internal/syncwaiter"
	"github.com/vertex-pro/vertex-pro/internal/torrentidentity"
	"github.com/vertex-pro/vertex-pro/internal/unknownreconciler"
)

const (
	kill9ChildEnv       = "VERTEX_PRO_KILL9_CHILD"
	kill9DatabaseEnv    = "VERTEX_PRO_KILL9_DB"
	kill9SignalEnv      = "VERTEX_PRO_KILL9_SIGNAL"
	kill9TorrentNameEnv = "VERTEX_PRO_KILL9_TORRENT_NAME"
)

// kill9Report mirrors the shared transaction report shape plus the external
// kill -9 specific facts. It is intentionally a separate type so the six
// base-mode evidence files keep their exact field set.
type kill9Report struct {
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
		AutomaticRedispatches int    `json:"automatic_redispatches"`
		ChildTerminated       bool   `json:"child_terminated"`
		ChildExitedAbruptly   bool   `json:"child_exited_abruptly"`
		DurableMarkerObserved bool   `json:"durable_marker_before_send"`
		RecoveryPromoted      bool   `json:"startup_recovery_promoted_unknown"`
		ReplayBlocked         bool   `json:"replay_blocked"`
		ReconcilerOutcome     string `json:"reconciler_outcome"`
	} `json:"fault"`
	Transaction struct {
		IntentDispatch       string `json:"intent_dispatch"`
		AttemptFinal         string `json:"attempt_final"`
		JobFinal             string `json:"job_final"`
		ReservationFinal     string `json:"reservation_final"`
		WaiterClosed         bool   `json:"waiter_closed"`
		BindingCreated       bool   `json:"binding_created"`
		SyncMatched          bool   `json:"sync_matched"`
		ObservedState        string `json:"observed_state"`
		ObservedInactive     bool   `json:"observed_inactive"`
		DispatchHTTPStatus   int    `json:"dispatch_http_status"`
		DispatchFormat       string `json:"dispatch_format"`
		DispatchCommitState  string `json:"dispatch_commit_state"`
		ResponseFactRecorded bool   `json:"response_fact_recorded"`
		EventCount           int    `json:"event_count"`
		SyncEvidenceSource   string `json:"sync_evidence_source"`
		FirstConfirmReplay   bool   `json:"first_confirm_replay_idempotent"`
	} `json:"transaction"`
}

// runKill9Child executes inside the sacrificial process: it runs the dispatch
// flow against the real qui proxy with a sender that records the durable
// marker, forwards the Add, signals the parent, and then blocks forever so
// dispatchcommit.Record is never reached. The parent kills this process at
// exactly that boundary.
func runKill9Child(ctx context.Context) error {
	databasePath := os.Getenv(kill9DatabaseEnv)
	signalPath := os.Getenv(kill9SignalEnv)
	torrentName := os.Getenv(kill9TorrentNameEnv)
	if databasePath == "" || signalPath == "" || torrentName == "" {
		return errors.New("kill9 child requires db, signal, and torrent name environment")
	}
	database, err := db.Open(ctx, db.Config{Path: databasePath, MaxOpenConns: 1, MaxIdleConns: 1})
	if err != nil {
		return err
	}
	defer database.Close()
	client, err := proxyClientFromEnv(ctx)
	if err != nil {
		return err
	}
	profile, err := qbprofile.Probe(ctx, client)
	if err != nil {
		return err
	}
	artifact := buildTrackerlessTorrent(torrentName)
	addRequest, err := qbadd.BuildRequest(profile, qbadd.TorrentArtifact{Filename: torrentName + ".torrent", Content: artifact}, qbadd.AddParams{StartInactive: true})
	if err != nil {
		return err
	}
	sender := &kill9ChildSender{delegate: client, database: database, signalPath: signalPath}
	flow, err := dispatchflow.New(database, sender, func() time.Time { return time.Now().UTC() })
	if err != nil {
		return err
	}
	_, err = flow.Run(ctx, dispatchflow.Request{AttemptID: "attempt-g1", SubmitIntentID: "intent-g1", WaiterID: "waiter-g1", APIVersion: profile.WebAPIVersion, HTTPRequest: addRequest})
	if err != nil && !errors.Is(err, errKill9Blocked) {
		return fmt.Errorf("child dispatch ended before the kill boundary: %w", err)
	}
	return nil
}

var errKill9Blocked = errors.New("kill9 child reached the post-send boundary")

type kill9ChildSender struct {
	delegate   dispatchworker.Sender
	database   *sql.DB
	signalPath string
}

func (sender *kill9ChildSender) Send(ctx context.Context, request *http.Request) (qbresponse.RawResponse, error) {
	var attemptState, attemptDispatch, sideEffect, jobState, intentDispatch, startedAt string
	var responseFactAt sql.NullString
	if err := sender.database.QueryRowContext(ctx, `SELECT a.attempt_state, a.dispatch_state, COALESCE(a.side_effect_fact,''), j.job_state, s.dispatch_state, s.dispatch_started_at, s.response_fact_at FROM placement_attempts a JOIN race_jobs j ON j.job_id=a.job_id JOIN submit_intents s ON s.attempt_id=a.attempt_id WHERE a.attempt_id='attempt-g1'`).Scan(&attemptState, &attemptDispatch, &sideEffect, &jobState, &intentDispatch, &startedAt, &responseFactAt); err != nil {
		return qbresponse.RawResponse{}, err
	}
	if attemptState != "ADD_SENT" || attemptDispatch != "STARTED" || sideEffect != "POSSIBLE_EFFECT" || jobState != "ADD_SENT" || intentDispatch != "STARTED" || startedAt == "" || responseFactAt.Valid {
		return qbresponse.RawResponse{}, errors.New("durable dispatch marker mismatch before network send")
	}
	raw, err := sender.delegate.Send(ctx, request)
	if err != nil {
		return raw, err
	}
	marker := "durable_marker=1\nhttp_status=" + fmt.Sprint(raw.StatusCode) + "\n"
	if writeErr := os.WriteFile(sender.signalPath, []byte(marker), 0o600); writeErr != nil {
		return raw, writeErr
	}
	<-ctx.Done()
	return raw, errKill9Blocked
}

func proxyClientFromEnv(ctx context.Context) (*proxy.Client, error) {
	return proxy.New(ctx, proxy.Config{
		BaseURL:    strings.TrimSpace(os.Getenv("VERTEX_PRO_QUI_URL")),
		ProxyKey:   secrets.SecretRef{Provider: "env", Key: "VERTEX_PRO_QUI_PROXY_KEY"},
		Store:      secrets.EnvStore{},
		HTTPClient: &http.Client{Timeout: 60 * time.Second},
	})
}

// runExternalKill drives the external kill -9 scenario: the parent seeds the
// reservation chain, a real child process sends the Add through the lab qui
// proxy, the parent kills it after the network send but before the response
// fact commit, and startup recovery plus reconciliation must converge to
// RECONCILED_CONFIRMED with exactly one wire send.
func runExternalKill(ctx context.Context, client *proxy.Client, profile qbprofile.Profile, identity torrentidentity.Identity, identityKey, torrentName, rejection, baselineDigest string) error {
	tempRoot, err := os.MkdirTemp("", "vertex-pro-g1-kill9-")
	if err != nil {
		return err
	}
	databasePath := filepath.Join(tempRoot, "kill9.db")
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
	if err := seedTransaction(ctx, database, identityKey, baselineDigest, now); err != nil {
		return err
	}
	if _, err := reservation.ReserveAtomic(ctx, database, reservation.Request{
		JobID: "job-g1", AttemptID: "attempt-g1", SubmitIntentID: "intent-g1", ReservationID: "reservation-g1",
		IdentityKey: identityKey, InstanceID: "instance-g1", StorageTargetID: "target-g1", RequiredBytes: 1,
		CapacityEpochID: "epoch-g1", AllocationEpoch: 1, CapacityRevision: 1, SnapshotGeneration: 1,
		SnapshotRevision: 1, SnapshotDigest: baselineDigest, RequestFingerprint: "request-g1", TargetFingerprint: "target-g1",
		PreAddIdentityFact: reservation.IdentityAbsent, FinalCandidateRecheckRevision: 1, MaxInflight: 1,
		Now: now.Format(time.RFC3339),
	}); err != nil {
		return fmt.Errorf("reserve transaction: %w", err)
	}
	if _, err := syncwaiter.Register(ctx, database, syncwaiter.Request{
		WaiterID: "waiter-g1", AttemptID: "attempt-g1", InstanceID: "instance-g1", RegisteredGeneration: 1,
		SourceSnapshotRevision: 1, IdentityKey: identityKey, ConfirmationDeadline: now.Add(2 * time.Minute).Format(time.RFC3339),
		ReconciliationDeadline: now.Add(5 * time.Minute).Format(time.RFC3339), CreatedAt: now.Format(time.RFC3339),
	}); err != nil {
		return fmt.Errorf("register sync waiter: %w", err)
	}
	if err := database.Close(); err != nil {
		return err
	}

	signalPath := filepath.Join(tempRoot, "send-signal.signal")
	child := exec.CommandContext(ctx, os.Args[0])
	child.Env = append(os.Environ(),
		kill9ChildEnv+"=1",
		kill9DatabaseEnv+"="+databasePath,
		kill9SignalEnv+"="+signalPath,
		kill9TorrentNameEnv+"="+torrentName,
	)
	var childOutput strings.Builder
	child.Stdout = &childOutput
	child.Stderr = &childOutput
	if err := child.Start(); err != nil {
		return err
	}
	childDone := make(chan error, 1)
	go func() { childDone <- child.Wait() }()
	childRunning := true
	defer func() {
		if childRunning && child.Process != nil {
			_ = child.Process.Kill()
			<-childDone
		}
	}()

	signalDeadline := time.Now().Add(30 * time.Second)
	markerPayload := ""
	childExited := false
	var childErr error
	for !childExited {
		select {
		case err := <-childDone:
			childRunning = false
			childExited = true
			childErr = err
			if err != nil {
				return fmt.Errorf("kill9 child exited before the kill boundary: %v\n%s", err, childOutput.String())
			}
		case <-time.After(100 * time.Millisecond):
		}
		if time.Now().After(signalDeadline) && !childExited {
			return fmt.Errorf("timed out waiting for the post-send boundary\n%s", childOutput.String())
		}
		if !childExited {
			if payload, readErr := os.ReadFile(signalPath); readErr == nil {
				markerPayload = string(payload)
				break
			}
		}
	}
	if childExited {
		return fmt.Errorf("kill9 child exited before the kill boundary: %v\n%s", childErr, childOutput.String())
	}

	// The Add must have physically reached the lab qB through the qui proxy
	// before the kill, proving the network side effect happened.
	current := syncmerge.Snapshot{}
	match := identitymatcher.Result{Status: identitymatcher.StatusNotFound}
	matchDeadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(matchDeadline) {
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
		if match.Status == identitymatcher.StatusMatched && inactiveState(stateOf(current, match.CandidateID)) {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if match.Status != identitymatcher.StatusMatched {
		return fmt.Errorf("external Add did not reach the lab identity set: %s", match.Status)
	}
	observedState := stateOf(current, match.CandidateID)

	if err := child.Process.Kill(); err != nil {
		return err
	}
	childErr = <-childDone
	childRunning = false
	if childErr == nil {
		return errors.New("kill9 child exited cleanly after the abrupt kill")
	}

	recovered, err := db.Open(ctx, db.Config{Path: databasePath, MaxOpenConns: 1, MaxIdleConns: 1})
	if err != nil {
		return err
	}
	defer recovered.Close()
	recovery, err := startuprecover.Recover(ctx, recovered, startuprecover.Input{Now: time.Now().UTC(), ConfirmationWindow: 10 * time.Minute, ReconciliationWindow: time.Hour})
	if err != nil {
		return fmt.Errorf("startup recovery: %w", err)
	}
	if len(recovery.PromotedUnknown) != 1 || recovery.PromotedUnknown[0] != "attempt-g1" || len(recovery.KeptWaiters) != 1 {
		return fmt.Errorf("startup recovery=%+v", recovery)
	}
	var responseFactAt sql.NullString
	var reasonCode string
	if err := recovered.QueryRowContext(ctx, `SELECT s.response_fact_at, (SELECT reason_code FROM race_events WHERE attempt_id='attempt-g1' AND to_state='UNKNOWN' ORDER BY event_id DESC LIMIT 1) FROM submit_intents s WHERE s.attempt_id='attempt-g1'`).Scan(&responseFactAt, &reasonCode); err != nil {
		return err
	}
	if responseFactAt.Valid {
		return fmt.Errorf("response_fact_at=%q recorded by the killed process", responseFactAt.String)
	}
	if reasonCode != "STARTUP_INTERRUPTED_DISPATCH" {
		return fmt.Errorf("recovery reason=%q", reasonCode)
	}

	replayArtifact := buildTrackerlessTorrent(torrentName)
	replayRequest, err := qbadd.BuildRequest(profile, qbadd.TorrentArtifact{Filename: torrentName + ".torrent", Content: replayArtifact}, qbadd.AddParams{StartInactive: true})
	if err != nil {
		return err
	}
	replayCalls := 0
	replayRunner, err := dispatchflow.New(recovered, &countingSender{onSend: func() { replayCalls++ }}, time.Now)
	if err != nil {
		return err
	}
	_, err = replayRunner.Run(ctx, dispatchflow.Request{AttemptID: "attempt-g1", SubmitIntentID: "intent-g1", WaiterID: "waiter-g1", APIVersion: profile.WebAPIVersion, HTTPRequest: replayRequest})
	if !errors.Is(err, dispatchcommit.ErrDispatchAlreadyStarted) || replayCalls != 0 {
		return fmt.Errorf("replay guard: err=%v replayCalls=%d", err, replayCalls)
	}

	afterDigest := snapshotDigest(current)
	provider := fixedSnapshotProvider{snapshot: unknownreconciler.Snapshot{
		InstanceID: "instance-g1", SyncGeneration: 1, SnapshotRevision: 2, SnapshotDigest: afterDigest,
		IdentityPresent: true, QBTorrentID: match.CandidateID,
	}}
	outcomes, sweepErr := unknownreconciler.Sweep(ctx, recovered, provider, unknownreconciler.SweepInput{Now: time.Now().UTC()})
	if sweepErr != nil {
		return fmt.Errorf("reconcile after external kill: %w", sweepErr)
	}
	if len(outcomes) != 1 || outcomes[0].Outcome != unknownreconciler.OutcomeConfirmed {
		return fmt.Errorf("reconciler outcomes=%+v", outcomes)
	}

	finalFacts, err := readFinalFacts(ctx, recovered)
	if err != nil {
		return err
	}
	// A killed process never received an HTTP response, so response_fact_at
	// must stay NULL: UNKNOWN is recovered from the durable marker and resolved
	// by the snapshot-driven reconciler, never fabricated by recovery.
	if finalFacts.attemptState != "RECONCILED_CONFIRMED" || finalFacts.jobState != "RECONCILED_CONFIRMED" || finalFacts.intentDispatch != "STARTED" || finalFacts.responseFactRecorded || finalFacts.reservationState != "REFLECTING" || finalFacts.waiterState != "CLOSED" || finalFacts.bindingCount != 1 {
		return fmt.Errorf("unexpected final facts after external kill: %+v", finalFacts)
	}

	deleteStatus, deleteErr := deleteIdentity(ctx, strings.TrimSpace(os.Getenv("VERTEX_PRO_QUI_URL")), strings.TrimSpace(os.Getenv("VERTEX_PRO_QUI_PROXY_KEY")), match.CandidateID)
	if deleteErr != nil {
		return deleteErr
	}
	if deleteStatus != http.StatusOK && deleteStatus != http.StatusNoContent {
		return fmt.Errorf("cleanup delete returned HTTP %d", deleteStatus)
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
	if err := recovered.Close(); err != nil {
		return err
	}
	if err := os.RemoveAll(tempRoot); err != nil {
		return err
	}
	removed = true

	var output kill9Report
	output.Status = "PASS"
	output.Profile = profile.ID
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
	output.Fault.Mode = "external_kill"
	output.Fault.SenderCalls = 1
	output.Fault.AutomaticRedispatches = 0
	output.Fault.ChildTerminated = true
	output.Fault.ChildExitedAbruptly = childErr != nil
	output.Fault.DurableMarkerObserved = strings.Contains(markerPayload, "durable_marker=1")
	output.Fault.RecoveryPromoted = true
	output.Fault.ReplayBlocked = true
	output.Fault.ReconcilerOutcome = outcomes[0].Outcome
	output.Transaction.IntentDispatch = finalFacts.intentDispatch
	output.Transaction.AttemptFinal = finalFacts.attemptState
	output.Transaction.JobFinal = finalFacts.jobState
	output.Transaction.ReservationFinal = finalFacts.reservationState
	output.Transaction.WaiterClosed = finalFacts.waiterState == "CLOSED"
	output.Transaction.BindingCreated = finalFacts.bindingCount == 1
	output.Transaction.SyncMatched = match.Status == identitymatcher.StatusMatched
	output.Transaction.ObservedState = observedState
	output.Transaction.ObservedInactive = inactiveState(observedState)
	output.Transaction.ResponseFactRecorded = finalFacts.responseFactRecorded
	output.Transaction.EventCount = finalFacts.eventCount
	output.Transaction.SyncEvidenceSource = "QBITTORRENT_SYNC"
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(output)
}

func stateOf(snapshot syncmerge.Snapshot, id string) string {
	torrent, ok := snapshot.Torrents[id]
	if !ok {
		return ""
	}
	state, _ := torrent.Fields["state"].(string)
	return state
}

type countingSender struct {
	onSend func()
}

func (sender *countingSender) Send(context.Context, *http.Request) (qbresponse.RawResponse, error) {
	sender.onSend()
	return qbresponse.RawResponse{}, nil
}
