package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/adminauth"
	"github.com/vertex-pro/vertex-pro/internal/db"
	"github.com/vertex-pro/vertex-pro/internal/dispatchflow"
	"github.com/vertex-pro/vertex-pro/internal/httpapi"
	"github.com/vertex-pro/vertex-pro/internal/qbprofile"
	"github.com/vertex-pro/vertex-pro/internal/qui/control"
	"github.com/vertex-pro/vertex-pro/internal/qui/proxy"
	"github.com/vertex-pro/vertex-pro/internal/realtimecards"
	"github.com/vertex-pro/vertex-pro/internal/resourcesapi"
	"github.com/vertex-pro/vertex-pro/internal/rssfetch"
	"github.com/vertex-pro/vertex-pro/internal/rsspreview"
	"github.com/vertex-pro/vertex-pro/internal/rssservice"
	"github.com/vertex-pro/vertex-pro/internal/ruleservice"
	"github.com/vertex-pro/vertex-pro/internal/secrets"
	"github.com/vertex-pro/vertex-pro/internal/snapshot"
	"github.com/vertex-pro/vertex-pro/internal/sseserver"
	"github.com/vertex-pro/vertex-pro/internal/startuprecover"
	"github.com/vertex-pro/vertex-pro/internal/syncwatcher"
	"github.com/vertex-pro/vertex-pro/internal/webembed"
)

// Config contains the dependencies needed to start the single-process service.
// Startup recovery is always run before HTTP traffic is exposed. External
// mutation workers remain opt-in and are not started by this constructor.
type Config struct {
	Database                     db.Config
	Hosts                        []string
	Origins                      []string
	Now                          func() time.Time
	RecoveryConfirmationWindow   time.Duration
	RecoveryReconciliationWindow time.Duration
	Qui                          *QuiConfig
}

// QuiConfig enables the read-only qui control/profile/sync runtime. Add and
// other mutating qB operations are deliberately not started here.
type QuiConfig struct {
	BaseURL        string
	InstanceID     string
	APIKey         secrets.SecretRef
	ProxyKey       secrets.SecretRef
	Store          secrets.Store
	SnapshotRoot   string
	SyncInterval   time.Duration
	HTTPClient     *http.Client
	EnableDispatch bool
}

// Runtime owns resources initialized during service startup.
type Runtime struct {
	Database *sql.DB
	Auth     *adminauth.Service
	Handler  http.Handler
	Profile  *qbprofile.Profile
	Dispatch *dispatchflow.Runner
	Recovery startuprecover.Result
	hub      *sseserver.Hub
	watcher  *syncwatcher.Watcher
	cancel   context.CancelFunc
	workers  sync.WaitGroup
}

// New opens and validates the database before exposing HTTP traffic. A fresh
// database is migrated first; a damaged or incomplete schema fails closed.
func New(ctx context.Context, config Config) (*Runtime, error) {
	if config.Database.Path == "" {
		return nil, errors.New("database path is required")
	}
	if len(config.Hosts) == 0 {
		return nil, errors.New("at least one allowed host is required")
	}

	database, err := db.Open(ctx, config.Database)
	if err != nil {
		return nil, err
	}
	closeDatabase := true
	defer func() {
		if closeDatabase {
			_ = database.Close()
		}
	}()

	runner, err := db.DefaultRunner()
	if err != nil {
		return nil, err
	}
	if err := runner.Apply(ctx, database); err != nil {
		return nil, err
	}
	if err := db.CheckStartupConsistency(ctx, database); err != nil {
		return nil, err
	}

	now := config.Now
	if now == nil {
		now = time.Now
	}
	confirmationWindow := config.RecoveryConfirmationWindow
	if confirmationWindow <= 0 {
		confirmationWindow = 10 * time.Minute
	}
	reconciliationWindow := config.RecoveryReconciliationWindow
	if reconciliationWindow <= 0 {
		reconciliationWindow = time.Hour
	}
	recovery, err := startuprecover.Recover(ctx, database, startuprecover.Input{Now: now().UTC(), ConfirmationWindow: confirmationWindow, ReconciliationWindow: reconciliationWindow})
	if err != nil {
		return nil, fmt.Errorf("recover interrupted dispatch chains: %w", err)
	}

	auth, err := adminauth.New(database)
	if err != nil {
		return nil, err
	}

	rss, err := rssservice.NewWithClock(database, now)
	if err != nil {
		return nil, err
	}
	feedFetcher, err := rssfetch.New(nil, rssfetch.DefaultMaxBodyBytes)
	if err != nil {
		return nil, err
	}

	rule, err := ruleservice.New(database, ruleservice.ClockFunc(now))
	if err != nil {
		return nil, err
	}

	hub := sseserver.NewHub()
	realtime := realtimecards.NewRegistry(hub, 30*time.Second)
	events := &sseserver.Server{Database: database, Hub: hub}
	web := &httpapi.WebProtection{AllowedHosts: config.Hosts, AllowedOrigins: config.Origins}
	api := resourcesapi.Router(resourcesapi.Deps{
		Database: database,
		Auth:     auth,
		Web:      web,
		Realtime: realtime,
		RSS:      &rss,
		FeedTest: func(ctx context.Context, feed rssservice.Feed) (rsspreview.Preview, error) {
			result, err := feedFetcher.Fetch(ctx, rssfetch.Request{URL: feed.FeedURL})
			if err != nil {
				return rsspreview.Preview{}, err
			}
			return rsspreview.Parse(feed.FeedURL, result.Body, 20)
		},
		Rules:  &rule,
		Events: events,
		Now:    now,
	})

	mux := http.NewServeMux()
	mux.Handle("/api/v1/", api)
	mux.Handle("/", webembed.Handler())
	runtime := &Runtime{Database: database, Auth: auth, Handler: mux, Recovery: recovery, hub: hub}
	if config.Qui != nil {
		if err := runtime.startQui(ctx, *config.Qui, now); err != nil {
			return nil, err
		}
	}
	closeDatabase = false
	return runtime, nil
}

// Close stops owned read-only workers before releasing the runtime database.
// Hub subscribers are closed by their request contexts.
func (runtime *Runtime) Close() error {
	if runtime == nil || runtime.Database == nil {
		return nil
	}
	if runtime.cancel != nil {
		runtime.cancel()
	}
	runtime.workers.Wait()
	if runtime.watcher != nil {
		runtime.watcher.Close()
	}
	return runtime.Database.Close()
}

// Ready performs the checks that must remain true while the service accepts traffic.
func (runtime *Runtime) Ready(ctx context.Context) error {
	if runtime == nil || runtime.Database == nil {
		return errors.New("application database is not initialized")
	}
	if err := runtime.Database.PingContext(ctx); err != nil {
		return err
	}
	return db.CheckStartupConsistency(ctx, runtime.Database)
}

// SubscriberCount is useful for lifecycle diagnostics and focused tests.
func (runtime *Runtime) SubscriberCount() int {
	if runtime == nil || runtime.hub == nil {
		return 0
	}
	return runtime.hub.SubscriberCount()
}

func (runtime *Runtime) startQui(ctx context.Context, config QuiConfig, now func() time.Time) error {
	if strings.TrimSpace(config.InstanceID) == "" {
		return errors.New("qui instance ID is required")
	}
	if strings.TrimSpace(config.SnapshotRoot) == "" {
		return errors.New("qui snapshot root is required")
	}
	if config.Store == nil {
		return errors.New("qui secret store is required")
	}
	controlClient, err := control.New(ctx, control.Config{BaseURL: config.BaseURL, APIKey: config.APIKey, Store: config.Store, HTTPClient: config.HTTPClient})
	if err != nil {
		return err
	}
	proxyClient, err := proxy.New(ctx, proxy.Config{BaseURL: config.BaseURL, ProxyKey: config.ProxyKey, Store: config.Store, HTTPClient: config.HTTPClient})
	if err != nil {
		return err
	}
	instances, err := controlClient.Instances(ctx)
	if err != nil {
		return fmt.Errorf("list qui instances: %w", err)
	}
	found := false
	for _, instance := range instances {
		if instance.ID == config.InstanceID {
			if !instance.Enabled {
				return fmt.Errorf("qui instance %s is disabled", config.InstanceID)
			}
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("qui instance %s was not found", config.InstanceID)
	}
	if _, err := controlClient.TransferInfo(ctx, config.InstanceID); err != nil {
		return fmt.Errorf("read qui transfer info: %w", err)
	}
	if _, err := controlClient.Capabilities(ctx, config.InstanceID); err != nil {
		return fmt.Errorf("read qui capabilities: %w", err)
	}
	profile, err := qbprofile.Probe(ctx, proxyClient)
	if err != nil {
		return err
	}
	if config.EnableDispatch {
		if profile.ProfileVerificationState != "VERIFIED" {
			return fmt.Errorf("qB profile %s is %s; dispatch remains fail closed", profile.ID, profile.ProfileVerificationState)
		}
		dispatch, err := dispatchflow.New(runtime.Database, proxyClient, now)
		if err != nil {
			return fmt.Errorf("initialize dispatch flow: %w", err)
		}
		runtime.Dispatch = dispatch
	}
	store, err := snapshot.New(runtime.Database, config.SnapshotRoot)
	if err != nil {
		return err
	}
	if _, err := runtime.Database.ExecContext(ctx, `INSERT INTO sync_instance_state(instance_id, sync_state, sync_generation, rid, snapshot_revision, snapshot_digest, updated_at) VALUES (?, 'RECONCILING', 0, 0, 0, '', ?) ON CONFLICT(instance_id) DO NOTHING`, config.InstanceID, now().UTC().Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("initialize qui sync state: %w", err)
	}
	watcher, err := syncwatcher.New(config.InstanceID, proxyClient, store)
	if err != nil {
		return err
	}
	if _, err := watcher.RunOnce(ctx); err != nil {
		return fmt.Errorf("initialize qui sync: %w", err)
	}
	if err := updateSyncState(ctx, runtime.Database, config.InstanceID, "READY", now()); err != nil {
		return err
	}
	runtime.Profile = &profile
	runtime.watcher = watcher
	workerContext, cancel := context.WithCancel(context.Background())
	runtime.cancel = cancel
	interval := config.SyncInterval
	if interval <= 0 {
		interval = 10 * time.Second
	}
	runtime.workers.Add(1)
	go func() {
		defer runtime.workers.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-workerContext.Done():
				return
			case <-ticker.C:
				state := "READY"
				if _, err := watcher.RunOnce(workerContext); err != nil {
					if errors.Is(err, context.Canceled) {
						return
					}
					state = "STALE"
				}
				_ = updateSyncState(workerContext, runtime.Database, config.InstanceID, state, now())
			}
		}
	}()
	return nil
}

func updateSyncState(ctx context.Context, database *sql.DB, instanceID, state string, updatedAt time.Time) error {
	if _, err := database.ExecContext(ctx, `UPDATE sync_instance_state SET sync_state = ?, updated_at = ? WHERE instance_id = ?`, state, updatedAt.UTC().Format(time.RFC3339Nano), instanceID); err != nil {
		return fmt.Errorf("update qui sync state: %w", err)
	}
	return nil
}
