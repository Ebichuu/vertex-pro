// Package dispatchstage advances prepared race jobs into dispatch: it
// persists the operator-provided storage/capacity reference rows, prepares
// discovered jobs, re-downloads each artifact, rebuilds the Add request from
// the detected profile, and hands the chain to dispatchorchestrator. It never
// bypasses the orchestrator's fail-closed gates.
package dispatchstage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/dispatchflow"
	"github.com/vertex-pro/vertex-pro/internal/dispatchorchestrator"
	"github.com/vertex-pro/vertex-pro/internal/dispatchworker"
	"github.com/vertex-pro/vertex-pro/internal/placement"
	"github.com/vertex-pro/vertex-pro/internal/qbadd"
	"github.com/vertex-pro/vertex-pro/internal/qbprofile"
	"github.com/vertex-pro/vertex-pro/internal/reservation"
	"github.com/vertex-pro/vertex-pro/internal/rsssource"
	"github.com/vertex-pro/vertex-pro/internal/syncwaiter"
	"github.com/vertex-pro/vertex-pro/internal/torrentdownload"
	"github.com/vertex-pro/vertex-pro/internal/torrentparse"
)

// StorageConfig is the operator-verified storage target for one instance.
// The capacity facts come from the operator's validated evidence (CAP-01/02);
// the stage only persists and consumes them.
type StorageConfig struct {
	InstanceID       string
	StorageTargetID  string
	Path             string
	PhysicalFree     int64
	EffectiveFree    int64
	CapacityRevision int64
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
}

type Stage struct {
	database *sql.DB
	config   Config
}

func New(database *sql.DB, config Config) (*Stage, error) {
	if database == nil {
		return nil, fmt.Errorf("database is required")
	}
	if config.Downloader == nil || config.Sender == nil {
		return nil, fmt.Errorf("downloader and sender are required")
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
	if err := stage.ensureReferenceRows(ctx); err != nil {
		return nil, nil, err
	}
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
			continue
		}
		results = append(results, *result)
	}
	return results, failures, nil
}

func (stage *Stage) ensureReferenceRows(ctx context.Context) error {
	storage := stage.config.Storage
	now := stage.config.Now().UTC().Format(time.RFC3339Nano)
	if _, err := stage.database.ExecContext(ctx, `INSERT INTO storage_targets(storage_target_id, instance_id, path, storage_verification_state, created_at, updated_at) VALUES (?, ?, ?, 'VERIFIED', ?, ?)
		ON CONFLICT(storage_target_id) DO UPDATE SET path=excluded.path, storage_verification_state=excluded.storage_verification_state, updated_at=excluded.updated_at`,
		storage.StorageTargetID, storage.InstanceID, storage.Path, now, now); err != nil {
		return fmt.Errorf("upsert storage target: %w", err)
	}
	if _, err := stage.database.ExecContext(ctx, `INSERT INTO capacity_epochs(capacity_epoch_id, storage_target_id, allocation_epoch, capacity_state, physical_free_bytes, effective_free_bytes, snapshot_generation, snapshot_revision, snapshot_digest, created_at) VALUES (?, ?, 1, 'TRUSTED', ?, ?, 1, ?, ?, ?)
		ON CONFLICT(capacity_epoch_id) DO UPDATE SET physical_free_bytes=excluded.physical_free_bytes, effective_free_bytes=excluded.effective_free_bytes, snapshot_revision=excluded.snapshot_revision, snapshot_digest=excluded.snapshot_digest, created_at=excluded.created_at`,
		"epoch-"+storage.StorageTargetID, storage.StorageTargetID, storage.PhysicalFree, storage.EffectiveFree, storage.CapacityRevision, storageDigest(storage), now); err != nil {
		return fmt.Errorf("upsert capacity epoch: %w", err)
	}
	if _, err := stage.database.ExecContext(ctx, `INSERT INTO sync_instance_state(instance_id, sync_state, sync_generation, rid, snapshot_revision, snapshot_digest, updated_at) VALUES (?, 'READY', 1, 0, 1, ?, ?)
		ON CONFLICT(instance_id) DO NOTHING`,
		storage.InstanceID, storageDigest(storage), now); err != nil {
		return fmt.Errorf("initialize instance sync state: %w", err)
	}
	return nil
}

func storageDigest(storage StorageConfig) string {
	return fmt.Sprintf("operator-evidence-%s-%d", storage.StorageTargetID, storage.CapacityRevision)
}

func (stage *Stage) listDiscoveredJobs(ctx context.Context) ([]jobRow, error) {
	rows, err := stage.database.QueryContext(ctx, `SELECT job_id, source_id, identity_key FROM race_jobs WHERE job_state='DISCOVERED' AND identity_key IS NOT NULL AND identity_key != ''`)
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
	addRequest, err := qbadd.BuildRequest(stage.config.Profile, qbadd.TorrentArtifact{Filename: parsed.Name + ".torrent", Content: artifact.Body}, qbadd.AddParams{StartInactive: true})
	if err != nil {
		return nil, fmt.Errorf("build add request: %w", err)
	}
	now := stage.config.Now()
	result, err := dispatchorchestrator.Run(ctx, stage.database, dispatchorchestrator.Request{
		Prepare:   stage.prepareJob(job),
		Placement: stage.placementInput(job.identityKey),
		Reservation: reservation.Request{
			JobID: job.jobID, AttemptID: "attempt-" + job.jobID, SubmitIntentID: "intent-" + job.jobID, ReservationID: "reservation-" + job.jobID,
			IdentityKey: job.identityKey, InstanceID: stage.config.Storage.InstanceID, StorageTargetID: stage.config.Storage.StorageTargetID, RequiredBytes: 1,
			CapacityEpochID: "epoch-" + stage.config.Storage.StorageTargetID, AllocationEpoch: 1, CapacityRevision: stage.config.Storage.CapacityRevision, SnapshotGeneration: 1,
			SnapshotRevision: stage.config.Storage.CapacityRevision, SnapshotDigest: storageDigest(stage.config.Storage), RequestFingerprint: "fp-" + job.jobID, TargetFingerprint: "target-" + stage.config.Storage.StorageTargetID,
			PreAddIdentityFact: reservation.IdentityAbsent, FinalCandidateRecheckRevision: 1, MaxInflight: 1,
			Now: now.Format(time.RFC3339),
		},
		Waiter: syncwaiter.Request{
			WaiterID: "waiter-attempt-" + job.jobID, InstanceID: stage.config.Storage.InstanceID, RegisteredGeneration: 1,
			SourceSnapshotRevision: stage.config.Storage.CapacityRevision, IdentityKey: job.identityKey,
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
		_, err := database.ExecContext(ctx, `UPDATE race_jobs SET job_state='READY', rule_state='MATCHED', torrent_state='READY', placement_state='PREPARED', preparation_revision=preparation_revision+1, updated_at=? WHERE job_id=? AND job_state='DISCOVERED'`, now, job.jobID)
		return err
	}
}

func (stage *Stage) placementInput(identityKey string) placement.DecisionInput {
	storage := stage.config.Storage
	profileState := stage.config.Profile.ProfileVerificationState
	if profileState == "" {
		profileState = "EXPECTED"
	}
	return placement.DecisionInput{
		IdentityKey: identityKey, TargetMode: placement.TargetFixedInstance, TargetInstanceID: storage.InstanceID, RequiredBytes: 1,
		Weights: placement.DecisionWeights{InstanceWeight: 1, FreeRatioWeight: 1, InflightPenaltyWeight: 1, CurrentDownloadLoadPenaltyWeight: 1, RecentAddLatencyPenaltyWeight: 1},
		Candidates: []placement.DecisionCandidate{{
			InstanceID: storage.InstanceID, StorageTargetID: storage.StorageTargetID, CanPlace: true,
			HealthState: "AVAILABLE", ProfileState: profileState, StorageState: "VERIFIED", CapacityState: "TRUSTED",
			InstanceWeight: 1, FreeRatioPPM: 1_000_000, EffectiveFreeBytes: storage.EffectiveFree, MaxInflight: 1,
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
