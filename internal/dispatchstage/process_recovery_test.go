package dispatchstage

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
	"github.com/vertex-pro/vertex-pro/internal/qbprofile"
	"github.com/vertex-pro/vertex-pro/internal/qbresponse"
	"github.com/vertex-pro/vertex-pro/internal/startuprecover"
	"github.com/vertex-pro/vertex-pro/internal/torrentdownload"
)

type crashFixture struct {
	Database, Phase, Checkpoint string
	Storage                     StorageConfig
	Profile                     qbprofile.Profile
	Evidence                    Evidence
	Now                         time.Time
}

func TestDurableQueueSurvivesAbruptProcessTermination(t *testing.T) {
	for _, phase := range []string{"queued", "claimed", "started", "returned"} {
		t.Run(phase, func(t *testing.T) {
			f, s, calls := durableFixture(t)
			var seq int
			var name, path string
			if err := f.database.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &path); err != nil {
				t.Fatal(err)
			}
			input := crashFixture{Database: path, Phase: phase, Checkpoint: filepath.Join(t.TempDir(), "checkpoint"), Storage: s.config.Storage, Profile: s.config.Profile, Evidence: f.evidence("qb-1", 1, 1<<30), Now: f.now}
			payload, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			inputPath := input.Checkpoint + ".json"
			if err := os.WriteFile(inputPath, payload, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := f.database.Close(); err != nil {
				t.Fatal(err)
			}
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, executable, "-test.run=^TestDispatchQueueCrashHelper$", "-test.v")
			command.Env = append(os.Environ(), "VERTEX_DISPATCH_CRASH_FIXTURE="+inputPath)
			var output bytes.Buffer
			command.Stdout, command.Stderr = &output, &output
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			waited := false
			defer func() {
				if !waited {
					_ = command.Process.Kill()
					_ = command.Wait()
				}
			}()
			for {
				if _, err := os.Stat(input.Checkpoint); err == nil {
					break
				}
				select {
				case <-ctx.Done():
					_ = command.Wait()
					waited = true
					t.Fatalf("child never reached %s: %s", phase, output.String())
				case <-time.After(10 * time.Millisecond):
				}
			}
			// Kill, rather than canceling a Go context or closing the database:
			// no child deferred cleanup or SQLite Close may run at the checkpoint.
			if err := command.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			waitErr := command.Wait()
			waited = true
			if waitErr == nil {
				t.Fatal("child exited normally instead of being terminated")
			}
			reopened, err := db.Open(context.Background(), db.Config{Path: path})
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			if _, err := startuprecover.Recover(context.Background(), reopened, startuprecover.Input{Now: f.now, ConfirmationWindow: 10 * time.Minute, ReconciliationWindow: time.Hour}); err != nil {
				t.Fatal(err)
			}
			restored, err := New(reopened, s.config)
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if _, err := restored.SendNext(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			childSends := 0
			if _, err := os.Stat(input.Checkpoint + ".sent"); err == nil {
				childSends = 1
			}
			if childSends+int(calls.Load()) != 1 || ((phase == "queued" || phase == "claimed") != (calls.Load() == 1)) {
				t.Fatalf("phase=%s child sends=%d recovered sends=%d", phase, childSends, calls.Load())
			}
			var state string
			var debt int64
			if err := reopened.QueryRow(`SELECT attempt_state,(SELECT sum(reserved_bytes) FROM capacity_reservations WHERE reservation_state!='RELEASED') FROM placement_attempts`).Scan(&state, &debt); err != nil {
				t.Fatal(err)
			}
			want := "AWAITING_CONFIRMATION"
			if phase == "started" {
				want = "UNKNOWN"
			}
			if state != want || debt != 42 {
				t.Fatalf("phase=%s state=%s debt=%d", phase, state, debt)
			}
		})
	}
}

func TestDispatchQueueCrashHelper(t *testing.T) {
	path := os.Getenv("VERTEX_DISPATCH_CRASH_FIXTURE")
	if path == "" {
		t.Skip("only executed by the process recovery test")
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var input crashFixture
	if err := json.Unmarshal(payload, &input); err != nil {
		t.Fatal(err)
	}
	database, err := db.Open(context.Background(), db.Config{Path: input.Database})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	checkpoint := func() {
		if err := os.WriteFile(input.Checkpoint, []byte(input.Phase), 0o600); err != nil {
			t.Fatal(err)
		}
		for {
			time.Sleep(time.Second)
		}
	}
	downloader, err := torrentdownload.New(torrentdownload.Config{AllowedHosts: []string{"example.test"}})
	if err != nil {
		t.Fatal(err)
	}
	allocated := false
	stage, err := New(database, Config{DurableQueue: true, Storage: input.Storage, Profile: input.Profile, Downloader: downloader, Now: func() time.Time { return input.Now },
		Collect: func(context.Context) (Evidence, func(), error) {
			if allocated && input.Phase == "claimed" {
				checkpoint()
			}
			return input.Evidence, nil, nil
		},
		Sender: queueSender(func(context.Context, *http.Request) (qbresponse.RawResponse, error) {
			if err := os.WriteFile(input.Checkpoint+".sent", []byte("1"), 0o600); err != nil {
				t.Fatal(err)
			}
			if input.Phase == "started" {
				checkpoint()
			}
			return qbresponse.RawResponse{StatusCode: 200, Body: []byte("Ok.")}, nil
		})})
	if err != nil {
		t.Fatal(err)
	}
	allocateQueue(t, stage)
	allocated = true
	if input.Phase == "queued" {
		checkpoint()
	}
	if _, err := stage.SendNext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if input.Phase != "returned" {
		t.Fatalf("missed checkpoint %s", input.Phase)
	}
	checkpoint()
}
