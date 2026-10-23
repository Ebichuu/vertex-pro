package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/confirmcoordinator"
	"github.com/vertex-pro/vertex-pro/internal/db"
	"github.com/vertex-pro/vertex-pro/internal/dispatchstage"
	"github.com/vertex-pro/vertex-pro/internal/qui/control"
	"github.com/vertex-pro/vertex-pro/internal/qui/proxy"
	"github.com/vertex-pro/vertex-pro/internal/secrets"
	"github.com/vertex-pro/vertex-pro/internal/snapshot"
	"github.com/vertex-pro/vertex-pro/internal/syncwatcher"
	"github.com/vertex-pro/vertex-pro/internal/torrentdownload"
)

type selectionRequests struct {
	instances, sync, preferences, transfers int32
}

type selectionFixture struct {
	runtime                                 *Runtime
	now                                     time.Time
	instances, sync, preferences, transfers atomic.Int32
	fail                                    atomic.Int32 // 1: instances, 2: sync, 3: preferences, 4: transfer
	disabled                                atomic.Bool
}

func newSelectionFixture(t *testing.T, withStage bool) *selectionFixture {
	t.Helper()
	ctx := context.Background()
	f := &selectionFixture{now: time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/instances":
			f.instances.Add(1)
			if f.fail.Load() == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			fmt.Fprintf(w, `[{"id":2,"name":"selected","enabled":%t}]`, !f.disabled.Load())
		case "/api/instances/2/transfer-info":
			f.transfers.Add(1)
			if f.fail.Load() == 4 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			fmt.Fprint(w, `{"dl_info_speed":0,"up_info_speed":0,"dl_info_data":0,"up_info_data":0}`)
		case "/proxy/test-key/api/v2/sync/maindata":
			rid := f.sync.Add(1)
			if f.fail.Load() == 2 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			fmt.Fprintf(w, `{"rid":%d,"full_update":true,"torrents":{},"server_state":{"free_space_on_disk":1024}}`, rid)
		case "/proxy/test-key/api/v2/app/preferences":
			f.preferences.Add(1)
			if f.fail.Load() == 3 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			fmt.Fprint(w, `{"save_path":"/managed","temp_path_enabled":false}`)
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	dir := t.TempDir()
	runtime, err := New(ctx, Config{Database: db.Config{Path: filepath.Join(dir, "selection.db")}, Hosts: []string{"vertex.local"}, Now: func() time.Time { return f.now }})
	if err != nil {
		t.Fatal(err)
	}
	f.runtime = runtime
	t.Cleanup(func() { _ = runtime.Close() })
	runtime.instanceID, runtime.snapshotRoot = "2", filepath.Join(dir, "snapshots")
	t.Setenv("SELECTION_CACHE_TEST_KEY", "test-key")
	key := secrets.SecretRef{Provider: "env", Key: "SELECTION_CACHE_TEST_KEY"}
	runtime.controlClient, err = control.New(ctx, control.Config{BaseURL: server.URL, APIKey: key, Store: secrets.EnvStore{}, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	runtime.proxyClient, err = proxy.New(ctx, proxy.Config{BaseURL: server.URL, ProxyKey: key, Store: secrets.EnvStore{}, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	store, err := snapshot.New(runtime.Database, runtime.snapshotRoot)
	if err != nil {
		t.Fatal(err)
	}
	runtime.watcher, err = syncwatcher.New(runtime.instanceID, runtime.proxyClient, store)
	if err != nil {
		t.Fatal(err)
	}
	runtime.confirmations, err = confirmcoordinator.New(runtime.Database)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Database.Exec(`INSERT INTO sync_instance_state(instance_id,sync_state,sync_generation,rid,snapshot_revision,snapshot_digest,updated_at) VALUES('2','READY',0,0,0,'',?)`, f.now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if withStage {
		downloader, err := torrentdownload.New(torrentdownload.Config{AllowedHosts: []string{strings.TrimPrefix(server.URL, "http://")}})
		if err != nil {
			t.Fatal(err)
		}
		runtime.dispatchStage, err = dispatchstage.New(runtime.Database, dispatchstage.Config{
			Storage:    dispatchstage.StorageConfig{InstanceID: "2", StorageTargetID: "managed", Path: "/managed"},
			Downloader: downloader, Sender: runtime.proxyClient, Now: runtime.now, Collect: runtime.collectDispatchEvidence,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func (f *selectionFixture) collect(final bool) (dispatchstage.Evidence, error) {
	collect := f.runtime.collectSelectionEvidence
	if final {
		collect = f.runtime.collectDispatchEvidence
	}
	evidence, release, err := collect(context.Background())
	if release != nil {
		release()
	}
	return evidence, err
}

func (f *selectionFixture) mustCollect(t *testing.T, final bool) dispatchstage.Evidence {
	t.Helper()
	evidence, err := f.collect(final)
	if err != nil {
		t.Fatal(err)
	}
	return evidence
}

func (f *selectionFixture) counts() selectionRequests {
	return selectionRequests{f.instances.Load(), f.sync.Load(), f.preferences.Load(), f.transfers.Load()}
}

func (f *selectionFixture) assertCounts(t *testing.T, want selectionRequests) {
	t.Helper()
	if got := f.counts(); got != want {
		t.Fatalf("requests=%+v want=%+v", got, want)
	}
}

func TestSelectionCacheExpiresFromOriginalCollection(t *testing.T) {
	f := newSelectionFixture(t, false)
	initial := f.mustCollect(t, false)
	f.assertCounts(t, selectionRequests{1, 1, 1, 0})
	for _, age := range []time.Duration{29 * time.Second, 30 * time.Second} {
		f.now = initial.CollectedAt.Add(age)
		cached := f.mustCollect(t, false)
		if !cached.CollectedAt.Equal(initial.CollectedAt) || cached.Record != initial.Record {
			t.Fatalf("cache hit renewed or replaced the observation at %s", age)
		}
		f.assertCounts(t, selectionRequests{1, 1, 1, 0})
	}
	f.now = initial.CollectedAt.Add(30*time.Second + time.Nanosecond)
	fresh := f.mustCollect(t, false)
	if !fresh.CollectedAt.Equal(f.now) || fresh.Record.Revision <= initial.Record.Revision {
		t.Fatal("expired selection evidence was reused")
	}
	f.assertCounts(t, selectionRequests{2, 2, 2, 0})
}

func TestFinalEvidenceAlwaysCollectsAndUsesOnePreferencesRequest(t *testing.T) {
	f := newSelectionFixture(t, true)
	initial := f.mustCollect(t, false)
	f.now = initial.CollectedAt.Add(time.Second)
	fresh := f.mustCollect(t, true)
	if !fresh.CollectedAt.Equal(f.now) || fresh.Record.Revision <= initial.Record.Revision {
		t.Fatal("final collection reused fresh ranking evidence")
	}
	f.assertCounts(t, selectionRequests{2, 2, 2, 0})
}

func TestSelectionCacheInvalidatesChangedDatabaseFacts(t *testing.T) {
	for _, tc := range []struct {
		name, update string
	}{
		{"stale", `UPDATE sync_instance_state SET sync_state='STALE' WHERE instance_id='2'`},
		{"offline", `UPDATE sync_instance_state SET sync_state='OFFLINE' WHERE instance_id='2'`},
		{"generation", `UPDATE sync_instance_state SET sync_generation=0 WHERE instance_id='2'`},
		{"revision", `UPDATE sync_instance_state SET snapshot_revision=0 WHERE instance_id='2'`},
		{"digest", `UPDATE sync_instance_state SET snapshot_digest='changed' WHERE instance_id='2'`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSelectionFixture(t, false)
			initial := f.mustCollect(t, false)
			f.now = initial.CollectedAt.Add(time.Second)
			if _, err := f.runtime.Database.Exec(tc.update); err != nil {
				t.Fatal(err)
			}
			fresh := f.mustCollect(t, false)
			if !fresh.CollectedAt.Equal(f.now) || fresh.Record.Revision <= initial.Record.Revision {
				t.Fatal("changed sync facts did not invalidate cache")
			}
			f.assertCounts(t, selectionRequests{2, 2, 2, 0})
		})
	}
}

func TestCollectionFailureClearsSelectionCache(t *testing.T) {
	for _, final := range []bool{false, true} {
		for failure := int32(1); failure <= 3; failure++ {
			t.Run(fmt.Sprintf("final=%t/failure=%d", final, failure), func(t *testing.T) {
				f := newSelectionFixture(t, false)
				f.mustCollect(t, false)
				f.now = f.now.Add(time.Second)
				if !final {
					f.now = f.now.Add(30 * time.Second)
				}
				f.fail.Store(failure)
				if _, err := f.collect(final); err == nil {
					t.Fatal("failed live collection accepted")
				}
				if f.runtime.selectionEvidence != nil {
					t.Fatal("failed live collection left reusable evidence")
				}
				before := f.counts()
				f.fail.Store(0)
				f.mustCollect(t, false)
				f.assertCounts(t, selectionRequests{before.instances + 1, before.sync + 1, before.preferences + 1, 0})
			})
		}
	}
}

func TestCachedSelectionCannotAuthorizeDisabledRemoteInstance(t *testing.T) {
	f := newSelectionFixture(t, false)
	f.mustCollect(t, false)
	f.now = f.now.Add(time.Second)
	f.disabled.Store(true)
	f.mustCollect(t, false) // Ranking alone may use the fresh local observation.
	f.assertCounts(t, selectionRequests{1, 1, 1, 0})
	if _, err := f.collect(true); err == nil {
		t.Fatal("final collection accepted a disabled remote instance")
	}
	f.assertCounts(t, selectionRequests{2, 1, 1, 0})
	var state string
	if err := f.runtime.Database.QueryRow(`SELECT sync_state FROM sync_instance_state WHERE instance_id='2'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "OFFLINE" || f.runtime.selectionEvidence != nil {
		t.Fatalf("disabled state=%s cache=%v", state, f.runtime.selectionEvidence != nil)
	}
}

func TestSelectionDatabaseReadFailureClearsCache(t *testing.T) {
	f := newSelectionFixture(t, false)
	f.mustCollect(t, false)
	if _, err := f.runtime.Database.Exec(`DELETE FROM sync_instance_state WHERE instance_id='2'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.collect(false); err == nil {
		t.Fatal("missing persisted sync state accepted")
	}
	if f.runtime.selectionEvidence != nil {
		t.Fatal("failed database verification retained cached evidence")
	}
	f.assertCounts(t, selectionRequests{1, 1, 1, 0})
}

func TestBackgroundSyncWarmsSelectionCacheWithCapacityEvidence(t *testing.T) {
	f := newSelectionFixture(t, true)
	if err := f.runtime.TickSync(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.assertCounts(t, selectionRequests{1, 1, 1, 1})
	var state string
	var free int64
	if err := f.runtime.Database.QueryRow(`SELECT capacity_state,effective_free_bytes FROM capacity_epochs WHERE storage_target_id='managed'`).Scan(&state, &free); err != nil {
		t.Fatal(err)
	}
	if state != "TRUSTED" || free != 1024 {
		t.Fatalf("capacity=%s free=%d", state, free)
	}
	observedAt := f.now
	f.now = f.now.Add(29 * time.Second)
	warmed := f.mustCollect(t, false)
	if !warmed.CollectedAt.Equal(observedAt) {
		t.Fatal("background evidence was not reused at its original collection time")
	}
	f.assertCounts(t, selectionRequests{1, 1, 1, 1})
	f.mustCollect(t, true)
	f.assertCounts(t, selectionRequests{2, 2, 2, 1})
}

func TestBackgroundMonitoringFailureKeepsCapacityRefreshButClearsCache(t *testing.T) {
	f := newSelectionFixture(t, true)
	f.fail.Store(4)
	if err := f.runtime.TickSync(context.Background()); err == nil {
		t.Fatal("monitoring failure was not reported")
	}
	f.assertCounts(t, selectionRequests{1, 1, 1, 1})
	var state string
	var free int64
	if err := f.runtime.Database.QueryRow(`SELECT capacity_state,effective_free_bytes FROM capacity_epochs WHERE storage_target_id='managed'`).Scan(&state, &free); err != nil {
		t.Fatal(err)
	}
	if state != "TRUSTED" || free != 1024 {
		t.Fatalf("monitoring failure prevented capacity refresh: state=%s free=%d", state, free)
	}
	if f.runtime.selectionEvidence != nil {
		t.Fatal("monitoring failure left cached selection evidence")
	}
	// Placement recovers live evidence independently of transfer-info sampling.
	f.mustCollect(t, false)
	f.assertCounts(t, selectionRequests{2, 2, 2, 1})
}
