// Package dispatchstage advances prepared race jobs into dispatch: it
// persists the operator-provided storage/capacity reference rows, prepares
// discovered jobs, re-downloads each artifact, rebuilds the Add request from
// the detected profile, and hands the chain to dispatchorchestrator. It never
// bypasses the orchestrator's fail-closed gates.
package dispatchstage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/dispatchflow"
	"github.com/vertex-pro/vertex-pro/internal/dispatchorchestrator"
	"github.com/vertex-pro/vertex-pro/internal/dispatchworker"
	"github.com/vertex-pro/vertex-pro/internal/placement"
	"github.com/vertex-pro/vertex-pro/internal/qbadd"
	"github.com/vertex-pro/vertex-pro/internal/qbprofile"
	"github.com/vertex-pro/vertex-pro/internal/reservation"
	"github.com/vertex-pro/vertex-pro/internal/rsssource"
	"github.com/vertex-pro/vertex-pro/internal/ruleaggregate"
	"github.com/vertex-pro/vertex-pro/internal/storageverify"
	"github.com/vertex-pro/vertex-pro/internal/syncwaiter"
	"github.com/vertex-pro/vertex-pro/internal/torrentdownload"
	"github.com/vertex-pro/vertex-pro/internal/torrentidentity"
	"github.com/vertex-pro/vertex-pro/internal/torrentparse"
)

// StorageConfig binds an operator-verified filesystem to a live qB snapshot.
// Capacity is collected before each placement, never supplied as a constant.
type StorageConfig struct {
	InstanceID        string
	StorageTargetID   string
	Path              string
	Verification      storageverify.Evidence
	SafetyMarginBytes int64
}

// Config configures one dispatch stage.
type Config struct {
	Storage         StorageConfig
	Downloader      *torrentdownload.Downloader
	Profile         qbprofile.Profile
	Sender          dispatchworker.Sender
	Now             func() time.Time
	ConfirmWindow   time.Duration
	ReconcileWindow time.Duration
	// Collect holds the instance sync lock until release, so the reservation
	// and durable Add marker use the same snapshot as the identity recheck.
	Collect func(context.Context) (Evidence, func(), error)
}

type Stage struct {
	database *sql.DB
	config   Config
	mu       sync.Mutex
}

func New(database *sql.DB, config Config) (*Stage, error) {
	if database == nil {
		return nil, fmt.Errorf("database is required")
	}
	if config.Downloader == nil || config.Sender == nil || config.Collect == nil {
		return nil, fmt.Errorf("downloader, sender and live evidence collector are required")
	}
	if strings.TrimSpace(config.Storage.InstanceID) == "" || strings.TrimSpace(config.Storage.StorageTargetID) == "" || strings.TrimSpace(config.Storage.Path) == "" || config.Storage.SafetyMarginBytes < 0 {
		return nil, fmt.Errorf("a valid instance storage target is required")
	}
	if config.ConfirmWindow <= 0 {
		config.ConfirmWindow = 10 * time.Minute
	}
	if config.ReconcileWindow <= 0 {
		config.ReconcileWindow = time.Hour
	}
	if config.Now == nil {
		config.Now = func() time.Time { return time.Now().UTC() }
	}
	return &Stage{database: database, config: config}, nil
}

type jobRow struct {
	jobID       string
	sourceID    string
	identityKey string
}

// Run advances every discovered job of the configured instance: prepare,
// ensure reference rows, and dispatch through the orchestrator. Per-job
// failures are recorded and do not abort the stage.
func (stage *Stage) Run(ctx context.Context) ([]dispatchorchestrator.Result, []string, error) {
	stage.mu.Lock()
	defer stage.mu.Unlock()
	jobs, err := stage.listDiscoveredJobs(ctx)
	if err != nil {
		return nil, nil, err
	}
	results := make([]dispatchorchestrator.Result, 0, len(jobs))
	failures := make([]string, 0)
	for _, job := range jobs {
		result, err := stage.dispatchJob(ctx, job)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", job.jobID, err))
			// Keep only a non-sensitive code in the user-visible job. A source
			// URL or transport error may contain a site passkey.
			_, _ = stage.database.ExecContext(ctx, `UPDATE race_jobs SET placement_wait_reason='DISPATCH_PREPARATION_FAILED',updated_at=? WHERE job_id=? AND current_attempt_id IS NULL AND job_state IN ('DISCOVERED','READY','WAITING_CAPACITY')`, stage.config.Now().UTC().Format(time.RFC3339Nano), job.jobID)
			continue
		}
		if result != nil {
			results = append(results, *result)
		}
	}
	return results, failures, nil
}

func (stage *Stage) listDiscoveredJobs(ctx context.Context) ([]jobRow, error) {
	rows, err := stage.database.QueryContext(ctx, `SELECT j.job_id, j.source_id, j.identity_key FROM race_jobs j JOIN job_rule_snapshots r ON r.job_id=j.job_id WHERE j.job_state IN ('DISCOVERED','READY','WAITING_CAPACITY') AND j.current_attempt_id IS NULL AND j.identity_key IS NOT NULL AND j.identity_key != '' AND json_extract(r.aggregation_json,'$.Winner.TargetMode')='FIXED_INSTANCE' AND json_extract(r.aggregation_json,'$.Winner.TargetInstanceID')=? ORDER BY j.created_at,j.job_id`, stage.config.Storage.InstanceID)
	if err != nil {
		return nil, fmt.Errorf("list discovered jobs: %w", err)
	}
	defer rows.Close()
	jobs := make([]jobRow, 0, 8)
	for rows.Next() {
		var job jobRow
		if err := rows.Scan(&job.jobID, &job.sourceID, &job.identityKey); err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

func (stage *Stage) dispatchJob(ctx context.Context, job jobRow) (*dispatchorchestrator.Result, error) {
	source, err := stage.loadSource(ctx, job.sourceID)
	if err != nil {
		return nil, err
	}
	artifact, err := stage.config.Downloader.Download(ctx, torrentdownload.Request{URL: source.URL})
	if err != nil {
		return nil, fmt.Errorf("re-download artifact: %w", err)
	}
	parsed, parseErr := torrentparse.Parse(artifact.Body, torrentparse.DefaultLimits())
	if parseErr != nil {
		return nil, fmt.Errorf("re-parse artifact: %w", parseErr)
	}
	identity, err := torrentidentity.FromTorrent(parsed)
	if err != nil || identity.V1 != job.identityKey || parsed.TotalSize <= 0 {
		return nil, fmt.Errorf("artifact identity or size no longer matches job")
	}
	var raw string
	var originalSize int64
	if err := stage.database.QueryRowContext(ctx, `SELECT aggregation_json,content_size FROM job_rule_snapshots WHERE job_id=?`, job.jobID).Scan(&raw, &originalSize); err != nil {
		return nil, fmt.Errorf("load immutable rule decision: %w", err)
	}
	var decision ruleaggregate.Result
	if err := json.Unmarshal([]byte(raw), &decision); err != nil {
		return nil, err
	}
	if decision.Rejected || originalSize != parsed.TotalSize || decision.Winner.TargetMode != "FIXED_INSTANCE" || decision.Winner.TargetInstanceID != stage.config.Storage.InstanceID {
		return nil, fmt.Errorf("rule decision does not authorize this target")
	}
	if len(decision.StorageLabels) > 0 || len(decision.AllowedProfiles) > 0 || decision.Winner.SavePath != "" || decision.Winner.AddParams["save_path_policy"] == "CATEGORY_SUBDIRECTORY" || decision.Winner.AddParams["sequential_download"] == "true" {
		return nil, fmt.Errorf("rule actions require unsupported placement evidence")
	}
	evidence, release, err := stage.config.Collect(ctx)
	if release != nil {
		defer release()
	}
	if err != nil {
		return nil, fmt.Errorf("collect placement evidence: %w", err)
	}
	capacity, err := stage.prepareCapacity(ctx, evidence)
	if err != nil {
		return nil, err
	}
	if _, exists := evidence.Snapshot.Torrents[job.identityKey]; exists {
		return nil, stage.recordAlreadyPresent(ctx, job, evidence, capacity)
	}
	addRequest, err := qbadd.BuildRequest(stage.config.Profile, qbadd.TorrentArtifact{Filename: parsed.Name + ".torrent", Content: artifact.Body}, qbadd.AddParams{StartInactive: true, SavePath: stage.config.Storage.Path, Category: decision.Winner.Category, Tags: decision.Tags})
	if err != nil {
		return nil, fmt.Errorf("build add request: %w", err)
	}
	now := stage.config.Now()
	result, err := dispatchorchestrator.Run(ctx, stage.database, dispatchorchestrator.Request{
		Prepare:   stage.prepareJob(job),
		Placement: stage.placementInput(job.identityKey, parsed.TotalSize, capacity.EffectiveFree),
		Reservation: reservation.Request{
			JobID: job.jobID, AttemptID: "attempt-" + job.jobID, SubmitIntentID: "intent-" + job.jobID, ReservationID: "reservation-" + job.jobID,
			IdentityKey: job.identityKey, InstanceID: stage.config.Storage.InstanceID, StorageTargetID: stage.config.Storage.StorageTargetID, RequiredBytes: parsed.TotalSize,
			CapacityEpochID: capacity.ID, AllocationEpoch: capacity.AllocationEpoch, CapacityRevision: int64(evidence.Record.Revision), SnapshotGeneration: int64(evidence.Record.Generation),
			SnapshotRevision: int64(evidence.Record.Revision), SnapshotDigest: evidence.Record.Digest, RequestFingerprint: "fp-" + job.jobID, TargetFingerprint: "target-" + stage.config.Storage.StorageTargetID,
			PreAddIdentityFact: reservation.IdentityAbsent, FinalCandidateRecheckRevision: int64(evidence.Record.Revision), MaxInflight: 1,
			Now: now.Format(time.RFC3339),
		},
		Waiter: syncwaiter.Request{
			WaiterID: "waiter-attempt-" + job.jobID, InstanceID: stage.config.Storage.InstanceID, RegisteredGeneration: int64(evidence.Record.Generation),
			SourceSnapshotRevision: int64(evidence.Record.Revision), IdentityKey: job.identityKey,
			ConfirmationDeadline:   now.Add(stage.config.ConfirmWindow).Format(time.RFC3339),
			ReconciliationDeadline: now.Add(stage.config.ReconcileWindow).Format(time.RFC3339),
			CreatedAt:              now.Format(time.RFC3339),
		},
		Dispatch: dispatchflow.Request{
			AttemptID: "attempt-" + job.jobID, SubmitIntentID: "intent-" + job.jobID, WaiterID: "waiter-attempt-" + job.jobID,
			APIVersion: stage.config.Profile.WebAPIVersion, HTTPRequest: addRequest,
		},
		Sender: stage.config.Sender,
		Now:    stage.config.Now,
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (stage *Stage) prepareJob(job jobRow) func(context.Context, *sql.DB) error {
	now := stage.config.Now().UTC().Format(time.RFC3339Nano)
	return func(ctx context.Context, database *sql.DB) error {
		_, err := database.ExecContext(ctx, `UPDATE race_jobs SET job_state='READY', rule_state='MATCHED', torrent_state='READY', placement_state='PREPARED', placement_wait_reason=NULL, preparation_revision=preparation_revision+1, updated_at=? WHERE job_id=? AND job_state IN ('DISCOVERED','READY','WAITING_CAPACITY') AND current_attempt_id IS NULL`, now, job.jobID)
		return err
	}
}

func (stage *Stage) placementInput(identityKey string, requiredBytes, effectiveFree int64) placement.DecisionInput {
	storage := stage.config.Storage
	profileState := stage.config.Profile.ProfileVerificationState
	if profileState == "" {
		profileState = "EXPECTED"
	}
	return placement.DecisionInput{
		IdentityKey: identityKey, TargetMode: placement.TargetFixedInstance, TargetInstanceID: storage.InstanceID, RequiredBytes: requiredBytes,
		Weights: placement.DecisionWeights{InstanceWeight: 1, FreeRatioWeight: 1, InflightPenaltyWeight: 1, CurrentDownloadLoadPenaltyWeight: 1, RecentAddLatencyPenaltyWeight: 1},
		Candidates: []placement.DecisionCandidate{{
			InstanceID: storage.InstanceID, StorageTargetID: storage.StorageTargetID, CanPlace: true,
			HealthState: "AVAILABLE", ProfileState: profileState, StorageState: "VERIFIED", CapacityState: "TRUSTED",
			InstanceWeight: 1, FreeRatioPPM: 1_000_000, EffectiveFreeBytes: effectiveFree, MaxInflight: 1,
		}},
	}
}

func (stage *Stage) loadSource(ctx context.Context, sourceID string) (rsssource.Source, error) {
	separator := strings.Index(sourceID, "/")
	if separator <= 0 || separator == len(sourceID)-1 {
		return rsssource.Source{}, fmt.Errorf("malformed source id %q", sourceID)
	}
	siteID, sourceKey := sourceID[:separator], sourceID[separator+1:]
	var url string
	err := stage.database.QueryRowContext(ctx, `SELECT url FROM rss_sources WHERE site_id=? AND source_key=?`, siteID, sourceKey).Scan(&url)
	if err != nil {
		return rsssource.Source{}, fmt.Errorf("load source %q: %w", sourceID, err)
	}
	return rsssource.Source{SiteID: siteID, SourceKey: sourceKey, URL: url}, nil
}
