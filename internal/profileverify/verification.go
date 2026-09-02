package profileverify

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
	"github.com/vertex-pro/vertex-pro/internal/qbprofile"
)

const (
	StateExpected = "EXPECTED"
	StateVerified = "VERIFIED"
	StateFailed   = "FAILED"

	SourceRealProxyLab       = "REAL_PROXY_LAB"
	CurrentTestSuiteRevision = "g1-profile-verification-v1"

	ReasonNoMatchingRun       = "NO_MATCHING_RUN"
	ReasonLatestRunFailed     = "LATEST_RUN_FAILED"
	ReasonReportDigestStale   = "REPORT_DIGEST_STALE"
	ReasonEvidenceExpired     = "EVIDENCE_EXPIRED"
	ReasonRuntimeBuildInvalid = "RUNTIME_BUILD_NOT_ELIGIBLE"
	ReasonInsufficientStable  = "INSUFFICIENT_STABLE_RUNS"
	ReasonVerifiedRunMatched  = "VERIFIED_RUN_MATCHED"
)

var (
	ErrInvalidRun  = errors.New("invalid profile verification run")
	ErrRunConflict = errors.New("profile verification run conflicts with an immutable record")
	digestPattern  = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	commitPattern  = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
)

type Run struct {
	RunID                     string     `json:"run_id"`
	CampaignID                string     `json:"campaign_id"`
	Repetition                int        `json:"repetition"`
	RequiredRepetitions       int        `json:"required_repetitions"`
	InstanceID                string     `json:"instance_id"`
	ProfileID                 string     `json:"profile_id"`
	SourceKind                string     `json:"source_kind"`
	QBAppVersion              string     `json:"qb_app_version"`
	QBBuildFingerprint        string     `json:"qb_build_fingerprint"`
	QBArtifactDigest          string     `json:"qb_artifact_digest"`
	WebAPIVersion             string     `json:"webapi_version"`
	LibtorrentVersion         string     `json:"libtorrent_version"`
	QuiVersion                string     `json:"qui_version"`
	QuiImageDigest            string     `json:"qui_image_digest"`
	ProxyRouteFingerprint     string     `json:"proxy_route_fingerprint"`
	HarnessImageDigest        string     `json:"harness_image_digest"`
	VertexCommit              string     `json:"vertex_commit"`
	VertexWorktreeClean       bool       `json:"vertex_worktree_clean"`
	TestSuiteRevision         string     `json:"test_suite_revision"`
	AddFixtureDigest          string     `json:"add_fixture_digest,omitempty"`
	SyncFixtureDigest         string     `json:"sync_fixture_digest,omitempty"`
	TrackerFixtureDigest      string     `json:"tracker_fixture_digest,omitempty"`
	TransferInfoFixtureDigest string     `json:"transfer_info_fixture_digest,omitempty"`
	VerificationReportDigest  string     `json:"verification_report_digest"`
	RequiredCaseCount         int        `json:"required_case_count"`
	PassedCaseCount           int        `json:"passed_case_count"`
	LabIsolated               bool       `json:"lab_isolated"`
	LabReturnedEmpty          bool       `json:"lab_returned_empty"`
	ProductionServicesCalled  bool       `json:"production_services_called"`
	SecretsRedacted           bool       `json:"secrets_redacted"`
	Kill9CasePassed           bool       `json:"kill9_case_passed"`
	AutomaticRedispatches     int        `json:"automatic_redispatches"`
	VerificationState         string     `json:"verification_state"`
	FailureCode               string     `json:"failure_code,omitempty"`
	StartedAt                 time.Time  `json:"started_at"`
	FinishedAt                time.Time  `json:"finished_at"`
	VerifiedAt                *time.Time `json:"verified_at,omitempty"`
	CreatedAt                 time.Time  `json:"created_at"`
	ExpiresAt                 time.Time  `json:"expires_at"`
}

type RecordResult struct {
	RunID      string
	State      string
	Idempotent bool
}

// Binding identifies the exact implementation and deployment facts against
// which a persisted lab report may be used. Changing any field invalidates the
// old campaign and leaves the detected Profile EXPECTED.
type Binding struct {
	RunID                     string    `json:"run_id"`
	CampaignID                string    `json:"campaign_id"`
	Repetition                int       `json:"repetition"`
	ProfileID                 string    `json:"profile_id"`
	QBAppVersion              string    `json:"qb_app_version"`
	QBBuildFingerprint        string    `json:"qb_build_fingerprint"`
	QBArtifactDigest          string    `json:"qb_artifact_digest"`
	WebAPIVersion             string    `json:"webapi_version"`
	LibtorrentVersion         string    `json:"libtorrent_version"`
	QuiVersion                string    `json:"qui_version"`
	QuiImageDigest            string    `json:"qui_image_digest"`
	ProxyRouteFingerprint     string    `json:"proxy_route_fingerprint"`
	HarnessImageDigest        string    `json:"harness_image_digest"`
	VertexCommit              string    `json:"vertex_commit"`
	TestSuiteRevision         string    `json:"test_suite_revision"`
	AddFixtureDigest          string    `json:"add_fixture_digest"`
	SyncFixtureDigest         string    `json:"sync_fixture_digest"`
	TrackerFixtureDigest      string    `json:"tracker_fixture_digest"`
	TransferInfoFixtureDigest string    `json:"transfer_info_fixture_digest"`
	VerificationReportDigest  string    `json:"verification_report_digest"`
	ExpiresAt                 time.Time `json:"expires_at"`

	// Runtime identity is supplied by the executable, never by the binding file.
	CurrentVertexCommit  string `json:"-"`
	CurrentWorktreeClean bool   `json:"-"`
}

type Decision struct {
	State                     string
	Reason                    string
	RunID                     string
	CampaignID                string
	ReportDigest              string
	QBArtifactDigest          string
	QuiImageDigest            string
	HarnessImageDigest        string
	AddFixtureDigest          string
	SyncFixtureDigest         string
	TrackerFixtureDigest      string
	TransferInfoFixtureDigest string
	VerifiedAt                *time.Time
}

// BindingFromRun creates the activation snapshot for one persisted run. It is
// intentionally complete so an operator cannot activate a profile with a
// binding file that omits a version, fixture, or expiry fact.
func BindingFromRun(run Run) Binding {
	return Binding{
		RunID: run.RunID, CampaignID: run.CampaignID, Repetition: run.Repetition, ProfileID: run.ProfileID,
		QBAppVersion: run.QBAppVersion, QBBuildFingerprint: run.QBBuildFingerprint, QBArtifactDigest: run.QBArtifactDigest,
		WebAPIVersion: run.WebAPIVersion, LibtorrentVersion: run.LibtorrentVersion, QuiVersion: run.QuiVersion,
		QuiImageDigest: run.QuiImageDigest, ProxyRouteFingerprint: run.ProxyRouteFingerprint, HarnessImageDigest: run.HarnessImageDigest,
		VertexCommit: run.VertexCommit, TestSuiteRevision: run.TestSuiteRevision,
		AddFixtureDigest: run.AddFixtureDigest, SyncFixtureDigest: run.SyncFixtureDigest,
		TrackerFixtureDigest: run.TrackerFixtureDigest, TransferInfoFixtureDigest: run.TransferInfoFixtureDigest,
		VerificationReportDigest: run.VerificationReportDigest, ExpiresAt: run.ExpiresAt,
	}
}

// Record appends one immutable, redacted real-proxy lab result. Idempotency is
// allowed only when the full run is identical; a reused run ID or report digest
// with different facts is rejected.
func Record(ctx context.Context, database *sql.DB, input Run) (RecordResult, error) {
	if database == nil {
		return RecordResult{}, errors.New("database is required")
	}
	run := normalizeRun(input)
	if err := validateRun(run); err != nil {
		return RecordResult{}, err
	}
	result := RecordResult{RunID: run.RunID, State: run.VerificationState}
	err := db.RunImmediate(ctx, database, func(ctx context.Context, transaction *db.ImmediateTx) error {
		existing, found, err := loadRunByID(ctx, transaction, run.RunID)
		if err != nil {
			return err
		}
		if found {
			if reflect.DeepEqual(existing, run) {
				result.Idempotent = true
				return nil
			}
			return ErrRunConflict
		}
		var digestRunID string
		err = transaction.QueryRowContext(ctx, `SELECT run_id FROM profile_verification_runs WHERE verification_report_digest=?`, run.VerificationReportDigest).Scan(&digestRunID)
		if err == nil {
			return ErrRunConflict
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("read verification report digest: %w", err)
		}

		_, err = transaction.ExecContext(ctx, `INSERT INTO profile_verification_runs(
			run_id, campaign_id, repetition, required_repetitions, instance_id, profile_id, source_kind,
			qb_app_version, qb_build_fingerprint, qb_artifact_digest, webapi_version, libtorrent_version, qui_version,
			qui_image_digest, proxy_route_fingerprint, harness_image_digest, vertex_commit, vertex_worktree_clean,
			test_suite_revision, add_fixture_digest, sync_fixture_digest, tracker_fixture_digest,
			transfer_info_fixture_digest, verification_report_digest, required_case_count,
			passed_case_count, lab_isolated, lab_returned_empty, production_services_called,
			secrets_redacted, kill9_case_passed, automatic_redispatches, verification_state,
			failure_code, started_at, finished_at, verified_at, created_at, expires_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			run.RunID, run.CampaignID, run.Repetition, run.RequiredRepetitions, run.InstanceID, run.ProfileID, run.SourceKind,
			run.QBAppVersion, run.QBBuildFingerprint, run.QBArtifactDigest, run.WebAPIVersion, run.LibtorrentVersion, run.QuiVersion,
			run.QuiImageDigest, run.ProxyRouteFingerprint, run.HarnessImageDigest, run.VertexCommit, boolInt(run.VertexWorktreeClean),
			run.TestSuiteRevision, nullable(run.AddFixtureDigest), nullable(run.SyncFixtureDigest), nullable(run.TrackerFixtureDigest),
			nullable(run.TransferInfoFixtureDigest), run.VerificationReportDigest, run.RequiredCaseCount,
			run.PassedCaseCount, boolInt(run.LabIsolated), boolInt(run.LabReturnedEmpty), boolInt(run.ProductionServicesCalled),
			boolInt(run.SecretsRedacted), boolInt(run.Kill9CasePassed), run.AutomaticRedispatches, run.VerificationState,
			nullable(run.FailureCode), formatTime(run.StartedAt), formatTime(run.FinishedAt), nullableTime(run.VerifiedAt), formatTime(run.CreatedAt), formatTime(run.ExpiresAt),
		)
		if err != nil {
			return fmt.Errorf("insert profile verification run: %w", err)
		}
		return nil
	})
	if err != nil {
		return RecordResult{}, err
	}
	return result, nil
}

// Apply resolves the latest exact-build campaign and projects its fail-closed
// decision onto a detected qB Profile. Three consecutive VERIFIED repetitions,
// including the external kill-9 case and zero automatic redispatches, are
// required before the Profile can become VERIFIED.
func Apply(ctx context.Context, database *sql.DB, profile qbprofile.Profile, binding Binding, now time.Time) (qbprofile.Profile, Decision, error) {
	decision, err := Resolve(ctx, database, profile, binding, now)
	if err != nil {
		return qbprofile.Profile{}, Decision{}, err
	}
	profile.ProfileVerificationState = decision.State
	profile.VerifiedQBArtifactDigest = ""
	profile.VerifiedQuiVersion = ""
	profile.VerifiedQuiImageDigest = ""
	profile.VerifiedHarnessImageDigest = ""
	profile.VerificationReportDigest = ""
	profile.VerificationAddFixtureDigest = ""
	profile.VerificationSyncFixtureDigest = ""
	profile.VerificationTrackerFixtureDigest = ""
	profile.VerificationTransferFixtureDigest = ""
	profile.ProfileVerifiedAt = nil
	if decision.State == StateVerified {
		profile.VerifiedQBArtifactDigest = decision.QBArtifactDigest
		profile.VerifiedQuiVersion = binding.QuiVersion
		profile.VerifiedQuiImageDigest = decision.QuiImageDigest
		profile.VerifiedHarnessImageDigest = decision.HarnessImageDigest
		profile.VerificationReportDigest = decision.ReportDigest
		profile.VerificationAddFixtureDigest = decision.AddFixtureDigest
		profile.VerificationSyncFixtureDigest = decision.SyncFixtureDigest
		profile.VerificationTrackerFixtureDigest = decision.TrackerFixtureDigest
		profile.VerificationTransferFixtureDigest = decision.TransferInfoFixtureDigest
		profile.ProfileVerifiedAt = cloneTime(decision.VerifiedAt)
	}
	return profile, decision, nil
}

func Resolve(ctx context.Context, database *sql.DB, profile qbprofile.Profile, input Binding, now time.Time) (Decision, error) {
	if database == nil {
		return Decision{}, errors.New("database is required")
	}
	if now.IsZero() {
		return Decision{}, errors.New("verification decision time is required")
	}
	now = now.UTC()
	binding := normalizeBinding(input)
	if err := validateBinding(binding); err != nil {
		return Decision{}, err
	}
	if strings.TrimSpace(profile.ID) == "" || strings.TrimSpace(profile.QBAppVersion) == "" || strings.TrimSpace(profile.WebAPIVersion) == "" || strings.TrimSpace(profile.LibtorrentVersion) == "" || !digestPattern.MatchString(profile.QBBuildFingerprint) {
		return Decision{}, errors.New("detected profile facts are incomplete")
	}

	latest, found, err := loadPersistedRunByID(ctx, database, binding.RunID)
	if err != nil {
		return Decision{}, err
	}
	if !found {
		return Decision{State: StateExpected, Reason: ReasonNoMatchingRun}, nil
	}
	if !bindingMatchesProfile(profile, binding) {
		return Decision{State: StateExpected, Reason: ReasonNoMatchingRun, RunID: latest.RunID, CampaignID: latest.CampaignID}, nil
	}
	if !bindingMatchesRun(latest, binding, true) {
		if bindingMatchesRun(latest, binding, false) {
			return Decision{State: StateExpected, Reason: ReasonReportDigestStale, RunID: latest.RunID, CampaignID: latest.CampaignID}, nil
		}
		return Decision{State: StateExpected, Reason: ReasonNoMatchingRun, RunID: latest.RunID, CampaignID: latest.CampaignID}, nil
	}
	newest, newestFound, err := loadNewestComparableRun(ctx, database, profile, binding)
	if err != nil {
		return Decision{}, err
	}
	if newestFound && isLaterRun(newest.FinishedAt, newest.RunID, latest.FinishedAt, latest.RunID) {
		decision := Decision{State: StateExpected, Reason: ReasonNoMatchingRun, RunID: newest.RunID, CampaignID: newest.CampaignID}
		if newest.VerificationState == StateFailed {
			decision.State = StateFailed
			decision.Reason = ReasonLatestRunFailed
		}
		return decision, nil
	}
	decision := Decision{
		State: latest.VerificationState, RunID: latest.RunID, CampaignID: latest.CampaignID,
		ReportDigest: latest.VerificationReportDigest, QBArtifactDigest: latest.QBArtifactDigest,
		QuiImageDigest: latest.QuiImageDigest, HarnessImageDigest: latest.HarnessImageDigest,
		AddFixtureDigest: latest.AddFixtureDigest, SyncFixtureDigest: latest.SyncFixtureDigest,
		TrackerFixtureDigest:      latest.TrackerFixtureDigest,
		TransferInfoFixtureDigest: latest.TransferInfoFixtureDigest, VerifiedAt: cloneTime(latest.VerifiedAt),
	}
	if latest.VerificationState == StateFailed {
		decision.Reason = ReasonLatestRunFailed
		return decision, nil
	}
	if !binding.CurrentWorktreeClean || strings.TrimSpace(binding.CurrentVertexCommit) == "" || binding.CurrentVertexCommit != binding.VertexCommit {
		decision.State = StateExpected
		decision.Reason = ReasonRuntimeBuildInvalid
		decision.VerifiedAt = nil
		return decision, nil
	}

	var verifiedRuns, distinctRepetitions, freshRuns int
	err = database.QueryRowContext(ctx, `SELECT COUNT(*), COUNT(DISTINCT repetition),
		COALESCE(SUM(CASE WHEN expires_at > ? THEN 1 ELSE 0 END), 0)
		FROM profile_verification_runs
		WHERE campaign_id=? AND profile_id=? AND instance_id=?
		  AND qb_app_version=? AND qb_build_fingerprint=? AND qb_artifact_digest=? AND webapi_version=? AND libtorrent_version=?
		  AND qui_version=? AND qui_image_digest=? AND proxy_route_fingerprint=? AND harness_image_digest=?
		  AND vertex_commit=? AND test_suite_revision=? AND required_repetitions=3
		  AND required_case_count=? AND add_fixture_digest=? AND sync_fixture_digest=?
		  AND tracker_fixture_digest=? AND transfer_info_fixture_digest=?
		  AND verification_state='VERIFIED'`,
		now.Format(time.RFC3339Nano), latest.CampaignID, profile.ID, latest.InstanceID,
		profile.QBAppVersion, profile.QBBuildFingerprint, binding.QBArtifactDigest, profile.WebAPIVersion, profile.LibtorrentVersion,
		binding.QuiVersion, binding.QuiImageDigest, binding.ProxyRouteFingerprint, binding.HarnessImageDigest,
		binding.VertexCommit, binding.TestSuiteRevision, latest.RequiredCaseCount, latest.AddFixtureDigest,
		latest.SyncFixtureDigest, latest.TrackerFixtureDigest, latest.TransferInfoFixtureDigest,
	).Scan(&verifiedRuns, &distinctRepetitions, &freshRuns)
	if err != nil {
		return Decision{}, fmt.Errorf("count stable verification runs: %w", err)
	}
	if latest.ExpiresAt.Before(now) || latest.ExpiresAt.Equal(now) {
		decision.State = StateExpected
		decision.Reason = ReasonEvidenceExpired
		decision.VerifiedAt = nil
		return decision, nil
	}
	if verifiedRuns == 3 && distinctRepetitions == 3 && freshRuns != 3 {
		decision.State = StateExpected
		decision.Reason = ReasonEvidenceExpired
		decision.VerifiedAt = nil
		return decision, nil
	}
	if latest.Repetition != 3 || verifiedRuns != 3 || distinctRepetitions != 3 || freshRuns != 3 {
		decision.State = StateExpected
		decision.Reason = ReasonInsufficientStable
		decision.VerifiedAt = nil
		return decision, nil
	}
	decision.State = StateVerified
	decision.Reason = ReasonVerifiedRunMatched
	return decision, nil
}

type comparableRun struct {
	RunID             string
	CampaignID        string
	VerificationState string
	FinishedAt        time.Time
}

func loadNewestComparableRun(ctx context.Context, database *sql.DB, profile qbprofile.Profile, binding Binding) (comparableRun, bool, error) {
	var run comparableRun
	var finishedAt string
	err := database.QueryRowContext(ctx, `SELECT run_id, campaign_id, verification_state, finished_at
		FROM profile_verification_runs
		WHERE source_kind=? AND profile_id=? AND qb_app_version=? AND qb_build_fingerprint=?
		  AND vertex_commit=? AND test_suite_revision=?
		ORDER BY finished_at DESC, run_id DESC LIMIT 1`,
		SourceRealProxyLab, profile.ID, profile.QBAppVersion, profile.QBBuildFingerprint,
		binding.VertexCommit, binding.TestSuiteRevision).Scan(&run.RunID, &run.CampaignID, &run.VerificationState, &finishedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return comparableRun{}, false, nil
	}
	if err != nil {
		return comparableRun{}, false, fmt.Errorf("read newest comparable profile verification run: %w", err)
	}
	parsed, err := time.Parse(time.RFC3339Nano, finishedAt)
	if err != nil {
		return comparableRun{}, false, fmt.Errorf("parse newest comparable run finished_at: %w", err)
	}
	run.FinishedAt = parsed
	return run, true, nil
}

func isLaterRun(candidateFinished time.Time, candidateID string, boundFinished time.Time, boundID string) bool {
	return candidateFinished.After(boundFinished) || (candidateFinished.Equal(boundFinished) && candidateID != boundID)
}

func loadPersistedRunByID(ctx context.Context, database *sql.DB, runID string) (Run, bool, error) {
	run, err := scanRun(database.QueryRowContext(ctx, `SELECT `+runColumns+` FROM profile_verification_runs WHERE run_id=?`, runID))
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, false, nil
	}
	if err != nil {
		return Run{}, false, fmt.Errorf("read persisted profile verification run: %w", err)
	}
	return run, true, nil
}

func bindingMatchesProfile(profile qbprofile.Profile, binding Binding) bool {
	return binding.ProfileID == profile.ID && binding.QBAppVersion == profile.QBAppVersion &&
		binding.QBBuildFingerprint == profile.QBBuildFingerprint && binding.WebAPIVersion == profile.WebAPIVersion &&
		binding.LibtorrentVersion == profile.LibtorrentVersion
}

func bindingMatchesRun(run Run, binding Binding, includeReport bool) bool {
	if run.RunID != binding.RunID || run.CampaignID != binding.CampaignID || run.Repetition != binding.Repetition ||
		run.ProfileID != binding.ProfileID || run.QBAppVersion != binding.QBAppVersion ||
		run.QBBuildFingerprint != binding.QBBuildFingerprint || run.QBArtifactDigest != binding.QBArtifactDigest ||
		run.WebAPIVersion != binding.WebAPIVersion || run.LibtorrentVersion != binding.LibtorrentVersion ||
		run.QuiVersion != binding.QuiVersion || run.QuiImageDigest != binding.QuiImageDigest ||
		run.ProxyRouteFingerprint != binding.ProxyRouteFingerprint || run.HarnessImageDigest != binding.HarnessImageDigest ||
		run.VertexCommit != binding.VertexCommit || run.TestSuiteRevision != binding.TestSuiteRevision ||
		run.AddFixtureDigest != binding.AddFixtureDigest || run.SyncFixtureDigest != binding.SyncFixtureDigest ||
		run.TrackerFixtureDigest != binding.TrackerFixtureDigest || run.TransferInfoFixtureDigest != binding.TransferInfoFixtureDigest ||
		!run.ExpiresAt.Equal(binding.ExpiresAt) {
		return false
	}
	return !includeReport || run.VerificationReportDigest == binding.VerificationReportDigest
}

const runColumns = `run_id, campaign_id, repetition, required_repetitions, instance_id, profile_id, source_kind,
	qb_app_version, qb_build_fingerprint, qb_artifact_digest, webapi_version, libtorrent_version, qui_version,
	qui_image_digest, proxy_route_fingerprint, harness_image_digest, vertex_commit, vertex_worktree_clean, test_suite_revision,
	add_fixture_digest, sync_fixture_digest, tracker_fixture_digest, transfer_info_fixture_digest,
	verification_report_digest, required_case_count, passed_case_count, lab_isolated, lab_returned_empty,
	production_services_called, secrets_redacted, kill9_case_passed, automatic_redispatches,
	verification_state, failure_code, started_at, finished_at, verified_at, created_at, expires_at`

type rowScanner interface {
	Scan(...any) error
}

func loadRunByID(ctx context.Context, transaction *db.ImmediateTx, runID string) (Run, bool, error) {
	run, err := scanRun(transaction.QueryRowContext(ctx, `SELECT `+runColumns+` FROM profile_verification_runs WHERE run_id=?`, runID))
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, false, nil
	}
	if err != nil {
		return Run{}, false, fmt.Errorf("read profile verification run: %w", err)
	}
	return run, true, nil
}

func scanRun(row rowScanner) (Run, error) {
	var run Run
	var addDigest, syncDigest, trackerDigest, transferDigest, failureCode, verifiedAt sql.NullString
	var vertexWorktreeClean, labIsolated, labReturnedEmpty, productionServicesCalled, secretsRedacted, kill9CasePassed int
	var startedAt, finishedAt, createdAt, expiresAt string
	err := row.Scan(
		&run.RunID, &run.CampaignID, &run.Repetition, &run.RequiredRepetitions, &run.InstanceID, &run.ProfileID, &run.SourceKind,
		&run.QBAppVersion, &run.QBBuildFingerprint, &run.QBArtifactDigest, &run.WebAPIVersion, &run.LibtorrentVersion, &run.QuiVersion,
		&run.QuiImageDigest, &run.ProxyRouteFingerprint, &run.HarnessImageDigest, &run.VertexCommit, &vertexWorktreeClean, &run.TestSuiteRevision,
		&addDigest, &syncDigest, &trackerDigest, &transferDigest, &run.VerificationReportDigest,
		&run.RequiredCaseCount, &run.PassedCaseCount, &labIsolated, &labReturnedEmpty, &productionServicesCalled,
		&secretsRedacted, &kill9CasePassed, &run.AutomaticRedispatches, &run.VerificationState, &failureCode,
		&startedAt, &finishedAt, &verifiedAt, &createdAt, &expiresAt,
	)
	if err != nil {
		return Run{}, err
	}
	run.AddFixtureDigest = nullString(addDigest)
	run.SyncFixtureDigest = nullString(syncDigest)
	run.TrackerFixtureDigest = nullString(trackerDigest)
	run.TransferInfoFixtureDigest = nullString(transferDigest)
	run.FailureCode = nullString(failureCode)
	run.VertexWorktreeClean = vertexWorktreeClean == 1
	run.LabIsolated = labIsolated == 1
	run.LabReturnedEmpty = labReturnedEmpty == 1
	run.ProductionServicesCalled = productionServicesCalled == 1
	run.SecretsRedacted = secretsRedacted == 1
	run.Kill9CasePassed = kill9CasePassed == 1
	if run.StartedAt, err = time.Parse(time.RFC3339Nano, startedAt); err != nil {
		return Run{}, fmt.Errorf("parse run started_at: %w", err)
	}
	if run.FinishedAt, err = time.Parse(time.RFC3339Nano, finishedAt); err != nil {
		return Run{}, fmt.Errorf("parse run finished_at: %w", err)
	}
	if run.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt); err != nil {
		return Run{}, fmt.Errorf("parse run created_at: %w", err)
	}
	if run.ExpiresAt, err = time.Parse(time.RFC3339Nano, expiresAt); err != nil {
		return Run{}, fmt.Errorf("parse run expires_at: %w", err)
	}
	if verifiedAt.Valid {
		value, parseErr := time.Parse(time.RFC3339Nano, verifiedAt.String)
		if parseErr != nil {
			return Run{}, fmt.Errorf("parse run verified_at: %w", parseErr)
		}
		run.VerifiedAt = &value
	}
	return run, nil
}

func validateRun(run Run) error {
	required := map[string]string{
		"run_id": run.RunID, "campaign_id": run.CampaignID, "instance_id": run.InstanceID, "profile_id": run.ProfileID,
		"source_kind": run.SourceKind, "qb_app_version": run.QBAppVersion, "qb_build_fingerprint": run.QBBuildFingerprint,
		"qb_artifact_digest": run.QBArtifactDigest, "webapi_version": run.WebAPIVersion, "libtorrent_version": run.LibtorrentVersion, "qui_version": run.QuiVersion,
		"qui_image_digest": run.QuiImageDigest, "proxy_route_fingerprint": run.ProxyRouteFingerprint,
		"harness_image_digest": run.HarnessImageDigest, "vertex_commit": run.VertexCommit,
		"test_suite_revision": run.TestSuiteRevision, "verification_report_digest": run.VerificationReportDigest,
	}
	for name, value := range required {
		if value == "" {
			return fmt.Errorf("%w: %s is required", ErrInvalidRun, name)
		}
	}
	if run.SourceKind != SourceRealProxyLab || (run.ProfileID != "QB_438" && run.ProfileID != "QB_504" && run.ProfileID != "QB_522") {
		return fmt.Errorf("%w: unsupported source or profile", ErrInvalidRun)
	}
	if run.TestSuiteRevision != CurrentTestSuiteRevision || !commitPattern.MatchString(run.VertexCommit) {
		return fmt.Errorf("%w: test suite revision or Vertex commit is invalid", ErrInvalidRun)
	}
	if run.Repetition < 1 || run.Repetition > 3 || run.RequiredRepetitions != 3 || run.RequiredCaseCount <= 0 || run.PassedCaseCount < 0 || run.PassedCaseCount > run.RequiredCaseCount || run.AutomaticRedispatches < 0 {
		return fmt.Errorf("%w: run counters are invalid", ErrInvalidRun)
	}
	for name, value := range map[string]string{
		"qb_build_fingerprint": run.QBBuildFingerprint, "qb_artifact_digest": run.QBArtifactDigest, "qui_image_digest": run.QuiImageDigest,
		"proxy_route_fingerprint": run.ProxyRouteFingerprint, "harness_image_digest": run.HarnessImageDigest,
		"verification_report_digest": run.VerificationReportDigest,
	} {
		if !digestPattern.MatchString(value) {
			return fmt.Errorf("%w: %s is not a sha256 digest", ErrInvalidRun, name)
		}
	}
	for name, value := range map[string]string{
		"add_fixture_digest": run.AddFixtureDigest, "sync_fixture_digest": run.SyncFixtureDigest,
		"tracker_fixture_digest": run.TrackerFixtureDigest, "transfer_info_fixture_digest": run.TransferInfoFixtureDigest,
	} {
		if value != "" && !digestPattern.MatchString(value) {
			return fmt.Errorf("%w: %s is not a sha256 digest", ErrInvalidRun, name)
		}
	}
	if run.StartedAt.IsZero() || run.FinishedAt.IsZero() || run.CreatedAt.IsZero() || run.ExpiresAt.IsZero() || run.FinishedAt.Before(run.StartedAt) || run.CreatedAt.Before(run.FinishedAt) || !run.ExpiresAt.After(run.CreatedAt) {
		return fmt.Errorf("%w: run timestamps are invalid", ErrInvalidRun)
	}
	if !run.SecretsRedacted || run.ProductionServicesCalled {
		return fmt.Errorf("%w: unsafe evidence cannot be persisted", ErrInvalidRun)
	}
	switch run.VerificationState {
	case StateVerified:
		if run.PassedCaseCount != run.RequiredCaseCount || !run.LabIsolated || !run.LabReturnedEmpty || !run.Kill9CasePassed || run.AutomaticRedispatches != 0 || !run.VertexWorktreeClean || run.FailureCode != "" || run.VerifiedAt == nil {
			return fmt.Errorf("%w: VERIFIED run lacks mandatory safety facts", ErrInvalidRun)
		}
		if run.AddFixtureDigest == "" || run.SyncFixtureDigest == "" || run.TrackerFixtureDigest == "" || run.TransferInfoFixtureDigest == "" {
			return fmt.Errorf("%w: VERIFIED run lacks fixture digests", ErrInvalidRun)
		}
		if run.VerifiedAt.Before(run.FinishedAt) || run.CreatedAt.Before(*run.VerifiedAt) {
			return fmt.Errorf("%w: verified_at is outside the completed-run interval", ErrInvalidRun)
		}
	case StateFailed:
		if run.FailureCode == "" || run.VerifiedAt != nil {
			return fmt.Errorf("%w: FAILED run lacks failure facts", ErrInvalidRun)
		}
	default:
		return fmt.Errorf("%w: unsupported verification state", ErrInvalidRun)
	}
	return nil
}

func validateBinding(binding Binding) error {
	if binding.RunID == "" || binding.CampaignID == "" || binding.ProfileID == "" || binding.QBAppVersion == "" ||
		binding.WebAPIVersion == "" || binding.LibtorrentVersion == "" || binding.QuiVersion == "" ||
		binding.VertexCommit == "" || binding.TestSuiteRevision == "" || binding.AddFixtureDigest == "" ||
		binding.SyncFixtureDigest == "" || binding.TrackerFixtureDigest == "" || binding.TransferInfoFixtureDigest == "" {
		return errors.New("verification binding is incomplete")
	}
	if binding.Repetition < 1 || binding.Repetition > 3 || binding.TestSuiteRevision != CurrentTestSuiteRevision || !commitPattern.MatchString(binding.VertexCommit) {
		return errors.New("verification binding run or Vertex identity is invalid")
	}
	if binding.ProfileID != "QB_438" && binding.ProfileID != "QB_504" && binding.ProfileID != "QB_522" {
		return errors.New("verification binding profile is unsupported")
	}
	if binding.ExpiresAt.IsZero() {
		return errors.New("verification binding expiry is required")
	}
	for name, value := range map[string]string{
		"qb_build_fingerprint":         binding.QBBuildFingerprint,
		"qb_artifact_digest":           binding.QBArtifactDigest,
		"qui_image_digest":             binding.QuiImageDigest,
		"proxy_route_fingerprint":      binding.ProxyRouteFingerprint,
		"harness_image_digest":         binding.HarnessImageDigest,
		"verification_report_digest":   binding.VerificationReportDigest,
		"add_fixture_digest":           binding.AddFixtureDigest,
		"sync_fixture_digest":          binding.SyncFixtureDigest,
		"tracker_fixture_digest":       binding.TrackerFixtureDigest,
		"transfer_info_fixture_digest": binding.TransferInfoFixtureDigest,
	} {
		if !digestPattern.MatchString(value) {
			return fmt.Errorf("verification binding %s is not a sha256 digest", name)
		}
	}
	return nil
}

func normalizeRun(run Run) Run {
	run.RunID = strings.TrimSpace(run.RunID)
	run.CampaignID = strings.TrimSpace(run.CampaignID)
	run.InstanceID = strings.TrimSpace(run.InstanceID)
	run.ProfileID = strings.TrimSpace(run.ProfileID)
	run.SourceKind = strings.TrimSpace(run.SourceKind)
	run.QBAppVersion = strings.TrimSpace(run.QBAppVersion)
	run.QBBuildFingerprint = strings.ToLower(strings.TrimSpace(run.QBBuildFingerprint))
	run.QBArtifactDigest = strings.ToLower(strings.TrimSpace(run.QBArtifactDigest))
	run.WebAPIVersion = strings.TrimSpace(run.WebAPIVersion)
	run.LibtorrentVersion = strings.TrimSpace(run.LibtorrentVersion)
	run.QuiVersion = strings.TrimSpace(run.QuiVersion)
	run.QuiImageDigest = strings.ToLower(strings.TrimSpace(run.QuiImageDigest))
	run.ProxyRouteFingerprint = strings.ToLower(strings.TrimSpace(run.ProxyRouteFingerprint))
	run.HarnessImageDigest = strings.ToLower(strings.TrimSpace(run.HarnessImageDigest))
	run.VertexCommit = strings.TrimSpace(run.VertexCommit)
	run.TestSuiteRevision = strings.TrimSpace(run.TestSuiteRevision)
	run.AddFixtureDigest = strings.ToLower(strings.TrimSpace(run.AddFixtureDigest))
	run.SyncFixtureDigest = strings.ToLower(strings.TrimSpace(run.SyncFixtureDigest))
	run.TrackerFixtureDigest = strings.ToLower(strings.TrimSpace(run.TrackerFixtureDigest))
	run.TransferInfoFixtureDigest = strings.ToLower(strings.TrimSpace(run.TransferInfoFixtureDigest))
	run.VerificationReportDigest = strings.ToLower(strings.TrimSpace(run.VerificationReportDigest))
	run.VerificationState = strings.TrimSpace(run.VerificationState)
	run.FailureCode = strings.TrimSpace(run.FailureCode)
	run.StartedAt = run.StartedAt.UTC()
	run.FinishedAt = run.FinishedAt.UTC()
	run.CreatedAt = run.CreatedAt.UTC()
	run.ExpiresAt = run.ExpiresAt.UTC()
	if run.VerifiedAt != nil {
		value := run.VerifiedAt.UTC()
		run.VerifiedAt = &value
	}
	return run
}

func normalizeBinding(binding Binding) Binding {
	binding.RunID = strings.TrimSpace(binding.RunID)
	binding.CampaignID = strings.TrimSpace(binding.CampaignID)
	binding.ProfileID = strings.TrimSpace(binding.ProfileID)
	binding.QBAppVersion = strings.TrimSpace(binding.QBAppVersion)
	binding.QBBuildFingerprint = strings.ToLower(strings.TrimSpace(binding.QBBuildFingerprint))
	binding.QBArtifactDigest = strings.ToLower(strings.TrimSpace(binding.QBArtifactDigest))
	binding.WebAPIVersion = strings.TrimSpace(binding.WebAPIVersion)
	binding.LibtorrentVersion = strings.TrimSpace(binding.LibtorrentVersion)
	binding.QuiVersion = strings.TrimSpace(binding.QuiVersion)
	binding.QuiImageDigest = strings.ToLower(strings.TrimSpace(binding.QuiImageDigest))
	binding.ProxyRouteFingerprint = strings.ToLower(strings.TrimSpace(binding.ProxyRouteFingerprint))
	binding.HarnessImageDigest = strings.ToLower(strings.TrimSpace(binding.HarnessImageDigest))
	binding.VertexCommit = strings.TrimSpace(binding.VertexCommit)
	binding.TestSuiteRevision = strings.TrimSpace(binding.TestSuiteRevision)
	binding.AddFixtureDigest = strings.ToLower(strings.TrimSpace(binding.AddFixtureDigest))
	binding.SyncFixtureDigest = strings.ToLower(strings.TrimSpace(binding.SyncFixtureDigest))
	binding.TrackerFixtureDigest = strings.ToLower(strings.TrimSpace(binding.TrackerFixtureDigest))
	binding.TransferInfoFixtureDigest = strings.ToLower(strings.TrimSpace(binding.TransferInfoFixtureDigest))
	binding.VerificationReportDigest = strings.ToLower(strings.TrimSpace(binding.VerificationReportDigest))
	binding.ExpiresAt = binding.ExpiresAt.UTC()
	binding.CurrentVertexCommit = strings.TrimSpace(binding.CurrentVertexCommit)
	return binding
}

func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return formatTime(*value)
}

func nullString(value sql.NullString) string {
	if value.Valid {
		return value.String
	}
	return ""
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
