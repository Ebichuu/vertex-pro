package profileverify

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
	"github.com/vertex-pro/vertex-pro/internal/qbprofile"
	"github.com/vertex-pro/vertex-pro/internal/qui/proxy"
)

func TestRecordIsImmutableAndIdempotent(t *testing.T) {
	database := verificationDatabase(t)
	run := verifiedRun(1, "campaign-1", time.Date(2026, 9, 2, 9, 0, 0, 0, time.UTC))
	result, err := Record(context.Background(), database, run)
	if err != nil {
		t.Fatal(err)
	}
	if result.Idempotent || result.State != StateVerified {
		t.Fatalf("result=%+v", result)
	}
	replay, err := Record(context.Background(), database, run)
	if err != nil {
		t.Fatal(err)
	}
	if !replay.Idempotent {
		t.Fatalf("replay=%+v", replay)
	}
	conflict := run
	conflict.PassedCaseCount--
	conflict.VerificationState = StateFailed
	conflict.FailureCode = "CASE_FAILED"
	conflict.VerifiedAt = nil
	if _, err := Record(context.Background(), database, conflict); !errors.Is(err, ErrRunConflict) {
		t.Fatalf("conflict error=%v", err)
	}
	if _, err := database.Exec(`UPDATE profile_verification_runs SET verification_state='FAILED' WHERE run_id=?`, run.RunID); err == nil || !strings.Contains(err.Error(), "PROFILE_VERIFICATION_RUN_IMMUTABLE") {
		t.Fatalf("update error=%v", err)
	}
	if _, err := database.Exec(`DELETE FROM profile_verification_runs WHERE run_id=?`, run.RunID); err == nil || !strings.Contains(err.Error(), "PROFILE_VERIFICATION_RUN_APPEND_ONLY") {
		t.Fatalf("delete error=%v", err)
	}
}

func TestResolveRequiresThreeConsecutiveExactVerifiedRuns(t *testing.T) {
	database := verificationDatabase(t)
	profile := detectedProfile()
	base := time.Date(2026, 9, 2, 9, 0, 0, 0, time.UTC)
	for repetition := 1; repetition <= 3; repetition++ {
		run := verifiedRun(repetition, "campaign-1", base.Add(time.Duration(repetition)*time.Minute))
		if _, err := Record(context.Background(), database, run); err != nil {
			t.Fatal(err)
		}
		binding := bindingFor(run)
		resolved, decision, err := Apply(context.Background(), database, profile, binding, run.CreatedAt.Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if repetition < 3 {
			if decision.State != StateExpected || decision.Reason != ReasonInsufficientStable || resolved.ProfileVerificationState != StateExpected {
				t.Fatalf("repetition=%d profile=%+v decision=%+v", repetition, resolved, decision)
			}
			continue
		}
		if decision.State != StateVerified || decision.Reason != ReasonVerifiedRunMatched || resolved.ProfileVerificationState != StateVerified {
			t.Fatalf("profile=%+v decision=%+v", resolved, decision)
		}
		if resolved.VerifiedQBArtifactDigest != run.QBArtifactDigest || resolved.VerifiedQuiVersion != run.QuiVersion || resolved.VerificationReportDigest != run.VerificationReportDigest || resolved.ProfileVerifiedAt == nil {
			t.Fatalf("verified profile metadata=%+v", resolved)
		}
		if resolved.VerificationAddFixtureDigest != run.AddFixtureDigest || resolved.VerificationSyncFixtureDigest != run.SyncFixtureDigest || resolved.VerificationTrackerFixtureDigest != run.TrackerFixtureDigest || resolved.VerificationTransferFixtureDigest != run.TransferInfoFixtureDigest {
			t.Fatalf("verified fixture metadata=%+v", resolved)
		}
	}
}

func TestResolveFailsClosedForStaleDigestChangedBindingAndLatestFailure(t *testing.T) {
	database := verificationDatabase(t)
	profile := detectedProfile()
	base := time.Date(2026, 9, 2, 9, 0, 0, 0, time.UTC)
	var latest Run
	for repetition := 1; repetition <= 3; repetition++ {
		latest = verifiedRun(repetition, "campaign-1", base.Add(time.Duration(repetition)*time.Minute))
		if _, err := Record(context.Background(), database, latest); err != nil {
			t.Fatal(err)
		}
	}

	stale := bindingFor(latest)
	stale.VerificationReportDigest = digest('0')
	decision, err := Resolve(context.Background(), database, profile, stale, base.Add(30*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if decision.State != StateExpected || decision.Reason != ReasonReportDigestStale {
		t.Fatalf("stale decision=%+v", decision)
	}

	changed := bindingFor(latest)
	changed.QuiImageDigest = digest('9')
	decision, err = Resolve(context.Background(), database, profile, changed, base.Add(30*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if decision.State != StateExpected || decision.Reason != ReasonNoMatchingRun {
		t.Fatalf("changed decision=%+v", decision)
	}

	decision, err = Resolve(context.Background(), database, profile, bindingFor(latest), latest.ExpiresAt.Add(time.Nanosecond))
	if err != nil {
		t.Fatal(err)
	}
	if decision.State != StateExpected || decision.Reason != ReasonEvidenceExpired {
		t.Fatalf("expired decision=%+v", decision)
	}

	failed := failedRun("campaign-2", base.Add(10*time.Minute))
	if _, err := Record(context.Background(), database, failed); err != nil {
		t.Fatal(err)
	}
	decision, err = Resolve(context.Background(), database, profile, bindingFor(latest), base.Add(30*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if decision.State != StateFailed || decision.Reason != ReasonLatestRunFailed || decision.RunID != failed.RunID {
		t.Fatalf("newer failed decision=%+v", decision)
	}
	decision, err = Resolve(context.Background(), database, profile, bindingFor(failed), base.Add(30*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if decision.State != StateFailed || decision.Reason != ReasonLatestRunFailed || decision.RunID != failed.RunID {
		t.Fatalf("failed decision=%+v", decision)
	}
}

func TestRecordRejectsUnsafeOrIncompleteEvidence(t *testing.T) {
	database := verificationDatabase(t)
	base := verifiedRun(1, "campaign-1", time.Now().UTC())
	cases := []Run{
		func() Run { run := base; run.SourceKind = "MOCK"; return run }(),
		func() Run { run := base; run.SecretsRedacted = false; return run }(),
		func() Run { run := base; run.VertexWorktreeClean = false; return run }(),
		func() Run { run := base; run.ProductionServicesCalled = true; return run }(),
		func() Run { run := base; run.Kill9CasePassed = false; return run }(),
		func() Run { run := base; run.HarnessImageDigest = "UNSET"; return run }(),
		func() Run { run := base; run.AddFixtureDigest = ""; return run }(),
		func() Run { run := base; run.AutomaticRedispatches = 1; return run }(),
	}
	for index, run := range cases {
		run.RunID += string(rune('a' + index))
		run.VerificationReportDigest = digest(byte('a' + index))
		if _, err := Record(context.Background(), database, run); !errors.Is(err, ErrInvalidRun) {
			t.Fatalf("case %d error=%v", index, err)
		}
	}
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM profile_verification_runs`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("persisted unsafe runs=%d", count)
	}
}

func TestDatabaseRejectsVerifiedRunWithoutKill9Fact(t *testing.T) {
	database := verificationDatabase(t)
	run := verifiedRun(1, "campaign-1", time.Now().UTC())
	_, err := database.Exec(`INSERT INTO profile_verification_runs(
		run_id, campaign_id, repetition, required_repetitions, instance_id, profile_id, source_kind,
		qb_app_version, qb_build_fingerprint, qb_artifact_digest, webapi_version, libtorrent_version, qui_version,
		qui_image_digest, proxy_route_fingerprint, harness_image_digest, vertex_commit, vertex_worktree_clean, test_suite_revision,
		add_fixture_digest, sync_fixture_digest, tracker_fixture_digest, transfer_info_fixture_digest,
		verification_report_digest, required_case_count, passed_case_count, lab_isolated, lab_returned_empty,
		production_services_called, secrets_redacted, kill9_case_passed, automatic_redispatches,
		verification_state, failure_code, started_at, finished_at, verified_at, created_at, expires_at
	) VALUES (?, ?, 1, 3, ?, ?, 'REAL_PROXY_LAB', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?, 18, 18, 1, 1, 0, 1, 0, 0, 'VERIFIED', NULL, ?, ?, ?, ?, ?)`,
		run.RunID, run.CampaignID, run.InstanceID, run.ProfileID, run.QBAppVersion, run.QBBuildFingerprint, run.QBArtifactDigest,
		run.WebAPIVersion, run.LibtorrentVersion, run.QuiVersion, run.QuiImageDigest, run.ProxyRouteFingerprint,
		run.HarnessImageDigest, run.VertexCommit, run.TestSuiteRevision, run.AddFixtureDigest, run.SyncFixtureDigest,
		run.TrackerFixtureDigest, run.TransferInfoFixtureDigest, run.VerificationReportDigest,
		formatTime(run.StartedAt), formatTime(run.FinishedAt), formatTime(*run.VerifiedAt), formatTime(run.CreatedAt), formatTime(run.ExpiresAt),
	)
	if err == nil || !strings.Contains(err.Error(), "CHECK constraint failed") {
		t.Fatalf("database error=%v; want VERIFIED kill-9 CHECK rejection", err)
	}
}

func verificationDatabase(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.Open(context.Background(), db.Config{Path: filepath.Join(t.TempDir(), "profile-verification.db")})
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

func detectedProfile() qbprofile.Profile {
	return qbprofile.Profile{ID: "QB_522", QBAppVersion: "5.2.2", QBBuildFingerprint: digest('1'), WebAPIVersion: "2.15.1", LibtorrentVersion: "2.0.13.0", ProfileVerificationState: StateExpected}
}

func verifiedRun(repetition int, campaign string, finished time.Time) Run {
	verifiedAt := finished.Add(time.Second)
	return Run{
		RunID: "run-" + campaign + "-" + string(rune('0'+repetition)), CampaignID: campaign, Repetition: repetition, RequiredRepetitions: 3,
		InstanceID: "lab-qb522", ProfileID: "QB_522", SourceKind: SourceRealProxyLab,
		QBAppVersion: "5.2.2", QBBuildFingerprint: digest('1'), QBArtifactDigest: digest('9'), WebAPIVersion: "2.15.1", LibtorrentVersion: "2.0.13.0",
		QuiVersion: "8fcfc4d2beaf5e68ccebe837d7c0d9be965c8896", QuiImageDigest: digest('2'), ProxyRouteFingerprint: proxy.RouteFingerprint(),
		HarnessImageDigest: digest('4'), VertexCommit: "0123456789abcdef0123456789abcdef01234567", VertexWorktreeClean: true, TestSuiteRevision: CurrentTestSuiteRevision,
		AddFixtureDigest: digest('5'), SyncFixtureDigest: digest('6'), TrackerFixtureDigest: digest('7'), TransferInfoFixtureDigest: digest('8'),
		VerificationReportDigest: digest(byte('a' + repetition)), RequiredCaseCount: 18, PassedCaseCount: 18,
		LabIsolated: true, LabReturnedEmpty: true, SecretsRedacted: true, Kill9CasePassed: true,
		VerificationState: StateVerified, StartedAt: finished.Add(-time.Minute), FinishedAt: finished, VerifiedAt: &verifiedAt, CreatedAt: verifiedAt, ExpiresAt: verifiedAt.Add(24 * time.Hour),
	}
}

func failedRun(campaign string, finished time.Time) Run {
	run := verifiedRun(1, campaign, finished)
	run.VerificationReportDigest = digest('f')
	run.VerificationState = StateFailed
	run.PassedCaseCount = 17
	run.FailureCode = "KILL9_REPLAY_DETECTED"
	run.VerifiedAt = nil
	run.Kill9CasePassed = false
	return run
}

func bindingFor(run Run) Binding {
	binding := BindingFromRun(run)
	binding.CurrentVertexCommit = run.VertexCommit
	binding.CurrentWorktreeClean = true
	return binding
}

func digest(character byte) string {
	return "sha256:" + strings.Repeat(string(character), 64)
}
