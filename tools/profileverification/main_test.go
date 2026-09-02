package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/buildidentity"
	"github.com/vertex-pro/vertex-pro/internal/db"
	"github.com/vertex-pro/vertex-pro/internal/profileverify"
	"github.com/vertex-pro/vertex-pro/internal/qui/proxy"
)

func TestExecutePersistsRunsAndWritesBindingOnlyAfterThirdPass(t *testing.T) {
	originalIdentity := currentBuildIdentity
	currentBuildIdentity = func() buildidentity.Identity {
		return buildidentity.Identity{Revision: "0123456789abcdef0123456789abcdef01234567", Clean: true, Available: true}
	}
	t.Cleanup(func() { currentBuildIdentity = originalIdentity })
	directory := t.TempDir()
	databasePath := filepath.Join(directory, "vertex-pro.db")
	bindingPath := filepath.Join(directory, "binding.json")
	base := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	fixturePaths := map[string]string{}
	for _, fixture := range []string{"add", "sync", "tracker", "transfer"} {
		path := filepath.Join(directory, fixture+".json")
		if err := os.WriteFile(path, []byte(fixture), 0o600); err != nil {
			t.Fatal(err)
		}
		fixturePaths[fixture] = path
	}
	for repetition := 1; repetition <= 3; repetition++ {
		run := toolRun(repetition, base.Add(time.Duration(repetition)*time.Minute))
		runPath := filepath.Join(directory, "run-"+string(rune('0'+repetition))+".json")
		reportPath := filepath.Join(directory, "report-"+string(rune('0'+repetition))+".json")
		if err := os.WriteFile(reportPath, []byte("report-"+string(rune('0'+repetition))), 0o600); err != nil {
			t.Fatal(err)
		}
		payload, err := json.Marshal(run)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(runPath, payload, 0o600); err != nil {
			t.Fatal(err)
		}
		var output bytes.Buffer
		if err := execute(context.Background(), []string{
			"-database", databasePath, "-run", runPath, "-report", reportPath,
			"-add-fixture", fixturePaths["add"], "-sync-fixture", fixturePaths["sync"],
			"-tracker-fixture", fixturePaths["tracker"], "-transfer-info-fixture", fixturePaths["transfer"],
			"-binding-output", bindingPath,
		}, &output); err != nil {
			t.Fatal(err)
		}
		var result struct {
			ProfileState   string `json:"profile_state"`
			DecisionReason string `json:"decision_reason"`
			BindingWritten bool   `json:"binding_written"`
		}
		if err := json.Unmarshal(output.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if repetition < 3 {
			if result.ProfileState != profileverify.StateExpected || result.BindingWritten {
				t.Fatalf("repetition=%d output=%s", repetition, output.String())
			}
			if _, err := os.Stat(bindingPath); !os.IsNotExist(err) {
				t.Fatalf("binding exists before stable campaign: %v", err)
			}
			continue
		}
		if result.ProfileState != profileverify.StateVerified || result.DecisionReason != profileverify.ReasonVerifiedRunMatched || !result.BindingWritten {
			t.Fatalf("third output=%s", output.String())
		}
	}

	database, err := db.Open(context.Background(), db.Config{Path: databasePath})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM profile_verification_runs`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("run count=%d", count)
	}
	bindingPayload, err := os.ReadFile(bindingPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(bindingPayload), toolDigest("report-3")) {
		t.Fatalf("binding=%s", bindingPayload)
	}
}

func TestExecuteRejectsEvidenceDigestMismatchBeforeDatabaseWrite(t *testing.T) {
	originalIdentity := currentBuildIdentity
	currentBuildIdentity = func() buildidentity.Identity {
		return buildidentity.Identity{Revision: "0123456789abcdef0123456789abcdef01234567", Clean: true, Available: true}
	}
	t.Cleanup(func() { currentBuildIdentity = originalIdentity })
	directory := t.TempDir()
	run := toolRun(1, time.Now().UTC())
	runPath := filepath.Join(directory, "run.json")
	payload, err := json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runPath, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	reportPath := filepath.Join(directory, "report.json")
	if err := os.WriteFile(reportPath, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(directory, "vertex-pro.db")
	err = execute(context.Background(), []string{"-database", databasePath, "-run", runPath, "-report", reportPath}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("error=%v", err)
	}
	if _, statErr := os.Stat(databasePath); !os.IsNotExist(statErr) {
		t.Fatalf("database was created before digest validation: %v", statErr)
	}
}

func TestReadRunRejectsUnknownAndTrailingJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.json")
	if err := os.WriteFile(path, []byte(`{"unknown":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readRun(path); err == nil {
		t.Fatal("unknown field was accepted")
	}
	run := toolRun(1, time.Now().UTC())
	payload, err := json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	payload = append(payload, []byte(` {}`)...)
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readRun(path); err == nil {
		t.Fatal("trailing JSON was accepted")
	}
}

func toolRun(repetition int, finished time.Time) profileverify.Run {
	verifiedAt := finished.Add(time.Second)
	return profileverify.Run{
		RunID: "tool-run-" + string(rune('0'+repetition)), CampaignID: "tool-campaign", Repetition: repetition, RequiredRepetitions: 3,
		InstanceID: "qb-522", ProfileID: "QB_522", SourceKind: profileverify.SourceRealProxyLab,
		QBAppVersion: "5.2.2", QBBuildFingerprint: toolDigest("qb-build"), QBArtifactDigest: toolDigest("qb-artifact"), WebAPIVersion: "2.15.1", LibtorrentVersion: "2.0.13.0",
		QuiVersion: "8fcfc4d2beaf5e68ccebe837d7c0d9be965c8896", QuiImageDigest: toolDigest("qui-image"),
		ProxyRouteFingerprint: proxy.RouteFingerprint(), HarnessImageDigest: toolDigest("harness-image"),
		VertexCommit: "0123456789abcdef0123456789abcdef01234567", VertexWorktreeClean: true, TestSuiteRevision: profileverify.CurrentTestSuiteRevision,
		AddFixtureDigest: toolDigest("add"), SyncFixtureDigest: toolDigest("sync"), TrackerFixtureDigest: toolDigest("tracker"), TransferInfoFixtureDigest: toolDigest("transfer"),
		VerificationReportDigest: toolDigest("report-" + string(rune('0'+repetition))), RequiredCaseCount: 18, PassedCaseCount: 18,
		LabIsolated: true, LabReturnedEmpty: true, SecretsRedacted: true, Kill9CasePassed: true,
		VerificationState: profileverify.StateVerified, StartedAt: finished.Add(-time.Minute), FinishedAt: finished, VerifiedAt: &verifiedAt, CreatedAt: verifiedAt, ExpiresAt: verifiedAt.Add(24 * time.Hour),
	}
}

func toolDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(digest[:])
}
