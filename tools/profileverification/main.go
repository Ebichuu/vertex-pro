package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/buildidentity"
	"github.com/vertex-pro/vertex-pro/internal/db"
	"github.com/vertex-pro/vertex-pro/internal/profileverify"
	"github.com/vertex-pro/vertex-pro/internal/qbprofile"
	"github.com/vertex-pro/vertex-pro/internal/qui/proxy"
)

var currentBuildIdentity = buildidentity.Current

type output struct {
	RunID          string `json:"run_id"`
	PersistedState string `json:"persisted_state"`
	Idempotent     bool   `json:"idempotent"`
	ProfileState   string `json:"profile_state"`
	DecisionReason string `json:"decision_reason"`
	BindingWritten bool   `json:"binding_written"`
}

func main() {
	if err := execute(context.Background(), os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func execute(ctx context.Context, arguments []string, writer io.Writer) error {
	flags := flag.NewFlagSet("profileverification", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	databasePath := flags.String("database", "", "Vertex Pro SQLite database path")
	runPath := flags.String("run", "", "redacted profile verification run metadata JSON")
	reportPath := flags.String("report", "", "redacted verification report whose digest is persisted")
	addFixturePath := flags.String("add-fixture", "", "redacted Add golden fixture")
	syncFixturePath := flags.String("sync-fixture", "", "redacted Sync golden fixture")
	trackerFixturePath := flags.String("tracker-fixture", "", "redacted tracker golden fixture")
	transferFixturePath := flags.String("transfer-info-fixture", "", "redacted transfer-info golden fixture")
	bindingOutputPath := flags.String("binding-output", "", "write the exact activation binding after a complete stable campaign")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if *databasePath == "" || *runPath == "" || *reportPath == "" {
		return errors.New("-database, -run, and -report are required")
	}
	run, err := readRun(*runPath)
	if err != nil {
		return err
	}
	if run.TestSuiteRevision != profileverify.CurrentTestSuiteRevision {
		return fmt.Errorf("test suite revision %q is not current revision %q", run.TestSuiteRevision, profileverify.CurrentTestSuiteRevision)
	}
	if run.ProxyRouteFingerprint != proxy.RouteFingerprint() {
		return fmt.Errorf("proxy route fingerprint %q does not match current contract %q", run.ProxyRouteFingerprint, proxy.RouteFingerprint())
	}
	if run.VerificationState == profileverify.StateVerified {
		identity := currentBuildIdentity()
		if !identity.Available || !identity.Clean || !run.VertexWorktreeClean || identity.Revision != run.VertexCommit {
			return fmt.Errorf("VERIFIED evidence requires the current clean Vertex commit %q; executable identity is revision=%q available=%t clean=%t", run.VertexCommit, identity.Revision, identity.Available, identity.Clean)
		}
	}
	if err := verifyDigest(*reportPath, run.VerificationReportDigest, "verification report"); err != nil {
		return err
	}
	for _, fixture := range []struct {
		name, path, digest string
	}{
		{name: "Add fixture", path: *addFixturePath, digest: run.AddFixtureDigest},
		{name: "Sync fixture", path: *syncFixturePath, digest: run.SyncFixtureDigest},
		{name: "tracker fixture", path: *trackerFixturePath, digest: run.TrackerFixtureDigest},
		{name: "transfer-info fixture", path: *transferFixturePath, digest: run.TransferInfoFixtureDigest},
	} {
		if fixture.digest == "" {
			if fixture.path != "" {
				return fmt.Errorf("%s path was supplied without a persisted digest", fixture.name)
			}
			continue
		}
		if fixture.path == "" {
			return fmt.Errorf("%s path is required", fixture.name)
		}
		if err := verifyDigest(fixture.path, fixture.digest, fixture.name); err != nil {
			return err
		}
	}
	database, err := db.Open(ctx, db.Config{Path: *databasePath, MaxOpenConns: 1, MaxIdleConns: 1})
	if err != nil {
		return err
	}
	defer database.Close()
	migrationRunner, err := db.DefaultRunner()
	if err != nil {
		return err
	}
	if err := migrationRunner.Apply(ctx, database); err != nil {
		return err
	}
	if err := db.CheckStartupConsistency(ctx, database); err != nil {
		return err
	}
	recorded, err := profileverify.Record(ctx, database, run)
	if err != nil {
		return err
	}
	binding := bindingFor(run)
	decision, err := profileverify.Resolve(ctx, database, qbprofile.Profile{
		ID: run.ProfileID, QBAppVersion: run.QBAppVersion, QBBuildFingerprint: run.QBBuildFingerprint,
		WebAPIVersion: run.WebAPIVersion, LibtorrentVersion: run.LibtorrentVersion,
	}, binding, time.Now().UTC())
	if err != nil {
		return err
	}
	result := output{
		RunID: recorded.RunID, PersistedState: recorded.State, Idempotent: recorded.Idempotent,
		ProfileState: decision.State, DecisionReason: decision.Reason,
	}
	if *bindingOutputPath != "" && decision.State == profileverify.StateVerified {
		payload, err := json.MarshalIndent(binding, "", "  ")
		if err != nil {
			return err
		}
		payload = append(payload, '\n')
		if err := writeImmutable(*bindingOutputPath, payload); err != nil {
			return err
		}
		result.BindingWritten = true
	}
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	return encoder.Encode(result)
}

func verifyDigest(path, expected, name string) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", name, err)
	}
	digest := sha256.Sum256(content)
	actual := "sha256:" + hex.EncodeToString(digest[:])
	if actual != expected {
		return fmt.Errorf("%s digest mismatch: got %s, want %s", name, actual, expected)
	}
	return nil
}

func readRun(path string) (profileverify.Run, error) {
	file, err := os.Open(path)
	if err != nil {
		return profileverify.Run{}, fmt.Errorf("open profile verification run: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var run profileverify.Run
	if err := decoder.Decode(&run); err != nil {
		return profileverify.Run{}, fmt.Errorf("decode profile verification run: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return profileverify.Run{}, errors.New("profile verification run contains trailing JSON")
	}
	return run, nil
}

func bindingFor(run profileverify.Run) profileverify.Binding {
	binding := profileverify.BindingFromRun(run)
	identity := currentBuildIdentity()
	binding.CurrentVertexCommit = identity.Revision
	binding.CurrentWorktreeClean = identity.Available && identity.Clean
	return binding
}

func writeImmutable(path string, payload []byte) error {
	if existing, err := os.ReadFile(path); err == nil {
		if bytes.Equal(existing, payload) {
			return nil
		}
		return errors.New("profile verification binding already exists with different content")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".profile-verification-binding-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(payload); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}
