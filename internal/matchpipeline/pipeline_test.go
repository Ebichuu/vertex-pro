package matchpipeline

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
	"github.com/vertex-pro/vertex-pro/internal/dispatchflow"
	"github.com/vertex-pro/vertex-pro/internal/dispatchorchestrator"
	"github.com/vertex-pro/vertex-pro/internal/placement"
	"github.com/vertex-pro/vertex-pro/internal/qbadd"
	"github.com/vertex-pro/vertex-pro/internal/qbprofile"
	"github.com/vertex-pro/vertex-pro/internal/qbresponse"
	"github.com/vertex-pro/vertex-pro/internal/racejob"
	"github.com/vertex-pro/vertex-pro/internal/racejobstore"
	"github.com/vertex-pro/vertex-pro/internal/reservation"
	"github.com/vertex-pro/vertex-pro/internal/rsssource"
	"github.com/vertex-pro/vertex-pro/internal/ruleaggregate"
	"github.com/vertex-pro/vertex-pro/internal/rules"
	"github.com/vertex-pro/vertex-pro/internal/ruleservice"
	"github.com/vertex-pro/vertex-pro/internal/syncwaiter"
)

func pipelineDatabase(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.Open(context.Background(), db.Config{Path: filepath.Join(t.TempDir(), "pipeline.db"), MaxOpenConns: 1, MaxIdleConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := db.DefaultRunner()
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Apply(context.Background(), database); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func seedSite(t *testing.T, database *sql.DB, siteID string) {
	t.Helper()
	if _, err := database.Exec(`INSERT INTO sites(site_id, name, base_url, enabled, created_at, updated_at) VALUES (?, 'Site', 'https://site.example', 1, strftime('%Y-%m-%dT%H:%M:%SZ','now'), strftime('%Y-%m-%dT%H:%M:%SZ','now'))`, siteID); err != nil {
		t.Fatal(err)
	}
}

func containsTree(field, needle string) json.RawMessage {
	tree := map[string]any{
		"version": 1,
		"root": map[string]any{
			"kind": "leaf", "field": field, "operator": "contains", "value": map[string]any{"type": "string", "string": needle},
		},
	}
	payload, _ := json.Marshal(tree)
	return payload
}

func sizeTree(minimum float64) json.RawMessage {
	tree := map[string]any{
		"version": 1,
		"root": map[string]any{
			"kind": "leaf", "field": "size", "operator": "greater_than", "value": map[string]any{"type": "number", "number": minimum},
		},
	}
	payload, _ := json.Marshal(tree)
	return payload
}

func missingFieldTree() json.RawMessage {
	tree := map[string]any{
		"version": 1,
		"root": map[string]any{
			"kind": "leaf", "field": "publisher", "operator": "equals", "value": map[string]any{"type": "string", "string": "someone"},
		},
	}
	payload, _ := json.Marshal(tree)
	return payload
}

func seedRule(t *testing.T, service ruleservice.Service, siteID, name string, priority int, definition json.RawMessage) ruleservice.Rule {
	t.Helper()
	t.Helper()
	rule, err := service.Create(context.Background(), ruleservice.CreateInput{SiteID: siteID, Name: name, Enabled: true, Priority: priority, Definition: definition})
	if err != nil {
		t.Fatal(err)
	}
	return rule
}

func item() Item {
	size := int64(2048)
	return Item{
		Source:      rsssource.Source{SiteID: "site-1", SourceKey: "key-1", GUID: "guid-1", URL: "https://site.example/a", Title: "Ubuntu Linux 24.04", SizeBytes: &size},
		IdentityKey: "identity-key",
	}
}

func TestPipelineMatchesAndCreatesRaceJob(t *testing.T) {
	database := pipelineDatabase(t)
	seedSite(t, database, "site-1")
	rulesService, err := ruleservice.New(database, nil)
	if err != nil {
		t.Fatal(err)
	}
	seedRule(t, rulesService, "site-1", "linux", 10, containsTree("title", "Linux"))
	store, err := racejobstore.New(database)
	if err != nil {
		t.Fatal(err)
	}
	creator, err := racejob.New(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	deps := Deps{Rules: &rulesService, JobCreator: creator, MatchFactory: func(rule ruleservice.Rule) (ruleaggregate.Match, error) {
		return ruleaggregate.Match{TargetMode: "FIXED_INSTANCE", TargetInstanceID: "instance-1", ParticipationWindow: time.Hour}, nil
	}}
	outcome, err := Process(context.Background(), deps, item())
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.Matched || !outcome.JobCreated {
		t.Fatalf("outcome=%+v", outcome)
	}
	if outcome.Aggregation == nil || outcome.Aggregation.Winner.TargetInstanceID != "instance-1" {
		t.Fatalf("aggregation=%+v", outcome.Aggregation)
	}
	if outcome.Job.JobState != "DISCOVERED" {
		t.Fatalf("job=%+v", outcome.Job)
	}
	// Same item again must reuse the race job.
	repeat, err := Process(context.Background(), deps, item())
	if err != nil {
		t.Fatal(err)
	}
	if !repeat.Matched || !repeat.JobDuplicate || repeat.Job.ID != outcome.Job.ID {
		t.Fatalf("repeat=%+v", repeat)
	}
}

func TestPipelineUnknownFailsClosed(t *testing.T) {
	database := pipelineDatabase(t)
	seedSite(t, database, "site-1")
	rulesService, err := ruleservice.New(database, nil)
	if err != nil {
		t.Fatal(err)
	}
	seedRule(t, rulesService, "site-1", "publisher-rule", 10, missingFieldTree())
	store, err := racejobstore.New(database)
	if err != nil {
		t.Fatal(err)
	}
	creator, err := racejob.New(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	deps := Deps{Rules: &rulesService, JobCreator: creator, MatchFactory: func(rule ruleservice.Rule) (ruleaggregate.Match, error) {
		return ruleaggregate.Match{}, nil
	}}
	outcome, err := Process(context.Background(), deps, item())
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Matched || outcome.Aggregation != nil {
		t.Fatalf("UNKNOWN matched: %+v", outcome)
	}
	if len(outcome.RuleStates) != 1 || outcome.RuleStates[0].State != rules.UnknownState {
		t.Fatalf("ruleStates=%+v", outcome.RuleStates)
	}
	if outcome.SkippedReason != "NO_RULE_MATCHED" {
		t.Fatalf("skippedReason=%s", outcome.SkippedReason)
	}
}

func TestPipelineSizeGateRejects(t *testing.T) {
	database := pipelineDatabase(t)
	seedSite(t, database, "site-1")
	rulesService, err := ruleservice.New(database, nil)
	if err != nil {
		t.Fatal(err)
	}
	seedRule(t, rulesService, "site-1", "big-only", 10, sizeTree(4096))
	store, err := racejobstore.New(database)
	if err != nil {
		t.Fatal(err)
	}
	creator, err := racejob.New(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	deps := Deps{Rules: &rulesService, JobCreator: creator, MatchFactory: func(rule ruleservice.Rule) (ruleaggregate.Match, error) {
		return ruleaggregate.Match{}, nil
	}}
	outcome, err := Process(context.Background(), deps, item())
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Matched || outcome.SkippedReason != "NO_RULE_MATCHED" {
		t.Fatalf("outcome=%+v", outcome)
	}
}

// TestFrontToBackChain wires the full production chain for one item: dedupe
// source -> rule pipeline -> persisted race job -> orchestrator dispatch.
func TestFrontToBackChain(t *testing.T) {
	database := pipelineDatabase(t)
	seedSite(t, database, "site-1")
	rulesService, err := ruleservice.New(database, nil)
	if err != nil {
		t.Fatal(err)
	}
	seedRule(t, rulesService, "site-1", "linux", 10, containsTree("title", "Linux"))
	store, err := racejobstore.New(database)
	if err != nil {
		t.Fatal(err)
	}
	creator, err := racejob.New(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	deps := Deps{Rules: &rulesService, JobCreator: creator, MatchFactory: func(rule ruleservice.Rule) (ruleaggregate.Match, error) {
		return ruleaggregate.Match{TargetMode: "FIXED_INSTANCE", TargetInstanceID: "instance-1", ParticipationWindow: time.Hour}, nil
	}}
	outcome, err := Process(context.Background(), deps, item())
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.Matched {
		t.Fatalf("outcome=%+v", outcome)
	}

	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		body, readErr := io.ReadAll(request.Body)
		if readErr != nil || request.Method != http.MethodPost || request.URL.Path != "/api/v2/torrents/add" || len(body) == 0 {
			http.Error(response, "invalid add", http.StatusBadRequest)
			return
		}
		calls++
		response.WriteHeader(http.StatusOK)
		_, _ = response.Write([]byte("Ok."))
	}))
	defer server.Close()
	target, parseErr := url.Parse(server.URL)
	if parseErr != nil {
		t.Fatal(parseErr)
	}

	result, err := dispatchorchestrator.Run(context.Background(), database, dispatchorchestrator.Request{
		Placement:   placementInput("VERIFIED"),
		Prepare:     prepareChain(database, outcome.Job.ID),
		Reservation: reservationRequestFor(outcome.Job.ID),
		Waiter:      waiterRequestFor(),
		Dispatch: dispatchflow.Request{
			AttemptID: "attempt-1", SubmitIntentID: "intent-1", WaiterID: "waiter-1",
			APIVersion: "2.11.2", HTTPRequest: newAddRequest(t),
		},
		Sender: redirectSender{target: target, calls: &calls},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.PlacementSelected || calls != 1 || result.Dispatch.Commit.State != "AWAITING_CONFIRMATION" {
		t.Fatalf("result=%+v calls=%d", result, calls)
	}
	var jobState string
	if err := database.QueryRow(`SELECT job_state FROM race_jobs WHERE job_id=?`, outcome.Job.ID).Scan(&jobState); err != nil {
		t.Fatal(err)
	}
	if jobState != "AWAITING_CONFIRMATION" {
		t.Fatalf("jobState=%s", jobState)
	}
}

// The following helpers re-create the orchestrator test fixtures against the
// persisted race job created by the pipeline.
func placementInput(profileState string) placement.DecisionInput {
	return placement.DecisionInput{
		IdentityKey: "identity-key", TargetMode: placement.TargetFixedInstance, TargetInstanceID: "instance-1", RequiredBytes: 1,
		Weights: placement.DecisionWeights{InstanceWeight: 1, FreeRatioWeight: 1, InflightPenaltyWeight: 1, CurrentDownloadLoadPenaltyWeight: 1, RecentAddLatencyPenaltyWeight: 1},
		Candidates: []placement.DecisionCandidate{{
			InstanceID: "instance-1", StorageTargetID: "target-1", CanPlace: true,
			HealthState: "AVAILABLE", ProfileState: profileState, StorageState: "VERIFIED", CapacityState: "TRUSTED",
			InstanceWeight: 1, FreeRatioPPM: 1_000_000, EffectiveFreeBytes: 1 << 30, MaxInflight: 1,
		}},
	}
}

func prepareChain(database *sql.DB, jobID string) func(context.Context, *sql.DB) error {
	now := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	timestamp := now.Format(time.RFC3339)
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO storage_targets(storage_target_id, instance_id, path, storage_verification_state, created_at, updated_at) VALUES ('target-1','instance-1','/managed','VERIFIED',?,?)`, []any{timestamp, timestamp}},
		{`INSERT INTO capacity_epochs(capacity_epoch_id, storage_target_id, allocation_epoch, capacity_state, physical_free_bytes, effective_free_bytes, snapshot_generation, snapshot_revision, snapshot_digest, created_at) VALUES ('epoch-1','target-1',1,'TRUSTED',1073741824,1073741824,1,1,'baseline-digest',?)`, []any{timestamp}},
		{`INSERT INTO sync_instance_state(instance_id, sync_state, sync_generation, rid, snapshot_revision, snapshot_digest, updated_at) VALUES ('instance-1','READY',1,0,1,'baseline-digest',?)`, []any{timestamp}},
		{`INSERT INTO sync_snapshots(instance_id, sync_generation, snapshot_revision, rid, snapshot_digest, snapshot_path, is_complete, created_at) VALUES ('instance-1',1,1,0,'baseline-digest','/managed/baseline.json',1,?)`, []any{timestamp}},
		{`UPDATE race_jobs SET job_state='READY', rule_state='MATCHED', torrent_state='READY', placement_state='PREPARED', preparation_revision=1, updated_at=? WHERE job_id=?`, []any{timestamp, jobID}},
	}
	return func(ctx context.Context, database *sql.DB) error {
		for _, statement := range statements {
			if _, err := database.ExecContext(ctx, statement.query, statement.args...); err != nil {
				return err
			}
		}
		return nil
	}
}

func reservationRequestFor(jobID string) reservation.Request {
	now := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	return reservation.Request{
		JobID: jobID, AttemptID: "attempt-1", SubmitIntentID: "intent-1", ReservationID: "reservation-1",
		IdentityKey: "identity-key", InstanceID: "instance-1", StorageTargetID: "target-1", RequiredBytes: 1,
		CapacityEpochID: "epoch-1", AllocationEpoch: 1, CapacityRevision: 1, SnapshotGeneration: 1,
		SnapshotRevision: 1, SnapshotDigest: "baseline-digest", RequestFingerprint: "request-1", TargetFingerprint: "target-1",
		PreAddIdentityFact: reservation.IdentityAbsent, FinalCandidateRecheckRevision: 1, MaxInflight: 1,
		Now: now.Format(time.RFC3339),
	}
}

func waiterRequestFor() syncwaiter.Request {
	now := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	return syncwaiter.Request{
		WaiterID: "waiter-1", InstanceID: "instance-1", RegisteredGeneration: 1,
		SourceSnapshotRevision: 1, IdentityKey: "identity-key",
		ConfirmationDeadline:   now.Add(2 * time.Minute).Format(time.RFC3339),
		ReconciliationDeadline: now.Add(5 * time.Minute).Format(time.RFC3339),
		CreatedAt:              now.Format(time.RFC3339),
	}
}

func newAddRequest(t *testing.T) *http.Request {
	t.Helper()
	profile := qbprofile.Profile{ID: "QB_504", WebAPIVersion: "2.11.2", AddStartField: "stopped"}
	request, err := qbadd.BuildRequest(profile, qbadd.TorrentArtifact{Filename: "fixture.torrent", Content: []byte("torrent")}, qbadd.AddParams{StartInactive: true})
	if err != nil {
		t.Fatal(err)
	}
	return request
}

type redirectSender struct {
	target *url.URL
	calls  *int
}

func (sender redirectSender) Send(ctx context.Context, request *http.Request) (qbresponse.RawResponse, error) {
	outgoing, err := http.NewRequestWithContext(ctx, request.Method, sender.target.String(), request.Body)
	if err != nil {
		return qbresponse.RawResponse{}, err
	}
	outgoing.URL.Path = request.URL.Path
	outgoing.Header = request.Header
	response, err := (&http.Client{Timeout: 10 * time.Second}).Do(outgoing)
	if err != nil {
		return qbresponse.RawResponse{}, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return qbresponse.RawResponse{StatusCode: response.StatusCode}, err
	}
	return qbresponse.RawResponse{StatusCode: response.StatusCode, Body: body}, nil
}
