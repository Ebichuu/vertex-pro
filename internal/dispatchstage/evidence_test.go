package dispatchstage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
	"github.com/vertex-pro/vertex-pro/internal/snapshot"
	"github.com/vertex-pro/vertex-pro/internal/syncmerge"
)

func TestLiveCapacityRejectsIncompleteOrUnrelatedEvidence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Evidence)
		valid  bool
	}{
		{"exact default path", func(*Evidence) {}, true},
		{"unknown free space", func(e *Evidence) { delete(e.Snapshot.ServerState, "free_space_on_disk") }, false},
		{"negative free space", func(e *Evidence) { e.Snapshot.ServerState["free_space_on_disk"] = -1 }, false},
		{"fractional free space", func(e *Evidence) { e.Snapshot.ServerState["free_space_on_disk"] = 1.5 }, false},
		{"different filesystem", func(e *Evidence) { e.Preferences["save_path"] = "/other" }, false},
		{"temp folder enabled", func(e *Evidence) { e.Preferences["temp_path_enabled"] = true }, false},
		{"temp setting unknown", func(e *Evidence) { delete(e.Preferences, "temp_path_enabled") }, false},
		{"unknown unmanaged liability", func(e *Evidence) {
			e.Snapshot.Torrents["external"] = syncmerge.Torrent{ID: "external", AmountLeftState: "UNKNOWN"}
		}, false},
		{"known unmanaged liability", func(e *Evidence) {
			left := int64(128)
			e.Snapshot.Torrents["external"] = syncmerge.Torrent{ID: "external", AmountLeftState: "KNOWN", AmountLeft: &left}
		}, true},
		{"stale observation", func(e *Evidence) { e.CollectedAt = e.CollectedAt.Add(-time.Minute) }, false},
		{"snapshot replaced", func(e *Evidence) { e.Record.Digest = "other" }, false},
		{"wrong instance", func(e *Evidence) { e.Record.InstanceID = "other" }, false},
		{"incomplete snapshot", func(e *Evidence) { e.Record.Complete = false }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			now := time.Now().UTC()
			database, err := db.Open(ctx, db.Config{Path: filepath.Join(t.TempDir(), "capacity.db")})
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			runner, _ := db.DefaultRunner()
			if err := runner.Apply(ctx, database); err != nil {
				t.Fatal(err)
			}
			if _, err := database.Exec(`INSERT INTO sync_instance_state(instance_id,sync_state,sync_generation,rid,snapshot_revision,snapshot_digest,updated_at) VALUES('2','READY',1,1,1,'digest',?)`, now.Format(time.RFC3339Nano)); err != nil {
				t.Fatal(err)
			}
			stage := Stage{database: database, config: Config{Storage: StorageConfig{InstanceID: "2", StorageTargetID: "disk", Path: "/managed", SafetyMarginBytes: 64}, Now: func() time.Time { return now }}}
			e := Evidence{Record: snapshot.Record{InstanceID: "2", Generation: 1, Revision: 1, Digest: "digest", Complete: true}, Snapshot: syncmerge.Snapshot{Torrents: map[string]syncmerge.Torrent{}, ServerState: map[string]any{"free_space_on_disk": int64(1024)}}, Preferences: map[string]any{"save_path": "/managed", "temp_path_enabled": false}, CollectedAt: now}
			tc.change(&e)
			capacity, err := stage.prepareCapacity(ctx, e)
			if tc.valid {
				want := int64(960)
				if len(e.Snapshot.Torrents) > 0 {
					want -= 128
				}
				if err != nil || capacity.EffectiveFree != want {
					t.Fatalf("capacity=%+v err=%v want=%d", capacity, err, want)
				}
			} else if err == nil {
				t.Fatal("invalid evidence was accepted")
			}
		})
	}
}
