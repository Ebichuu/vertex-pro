package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/adminauth"
	"github.com/vertex-pro/vertex-pro/internal/confirmcoordinator"
	"github.com/vertex-pro/vertex-pro/internal/db"
	"github.com/vertex-pro/vertex-pro/internal/dispatchflow"
	"github.com/vertex-pro/vertex-pro/internal/dispatchstage"
	"github.com/vertex-pro/vertex-pro/internal/httpapi"
	"github.com/vertex-pro/vertex-pro/internal/matchpipeline"
	"github.com/vertex-pro/vertex-pro/internal/profileverify"
	"github.com/vertex-pro/vertex-pro/internal/qbprofile"
	"github.com/vertex-pro/vertex-pro/internal/qui/control"
	"github.com/vertex-pro/vertex-pro/internal/qui/proxy"
	"github.com/vertex-pro/vertex-pro/internal/racejob"
	"github.com/vertex-pro/vertex-pro/internal/racejobstore"
	"github.com/vertex-pro/vertex-pro/internal/realtimecards"
	"github.com/vertex-pro/vertex-pro/internal/resourcesapi"
	"github.com/vertex-pro/vertex-pro/internal/rssfetch"
	"github.com/vertex-pro/vertex-pro/internal/rsspreview"
	"github.com/vertex-pro/vertex-pro/internal/rssservice"
	"github.com/vertex-pro/vertex-pro/internal/rsssource"
	"github.com/vertex-pro/vertex-pro/internal/rsssourcedb"
	"github.com/vertex-pro/vertex-pro/internal/ruleaggregate"
	"github.com/vertex-pro/vertex-pro/internal/ruleservice"
	"github.com/vertex-pro/vertex-pro/internal/schedulercycle"
	"github.com/vertex-pro/vertex-pro/internal/secrets"
	"github.com/vertex-pro/vertex-pro/internal/snapshot"
	"github.com/vertex-pro/vertex-pro/internal/sseserver"
	"github.com/vertex-pro/vertex-pro/internal/startuprecover"
	"github.com/vertex-pro/vertex-pro/internal/syncwatcher"
	"github.com/vertex-pro/vertex-pro/internal/torrentdownload"
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
	Scheduler                    *SchedulerConfig
	DispatchStage                *DispatchStageConfig
}

// DispatchStageConfig enables the runtime worker that advances prepared race
// jobs into dispatch through dispatchorchestrator. It only starts when
// dispatch itself is enabled, which requires a VERIFIED profile.
type DispatchStageConfig struct {
	Enabled              bool
	Storage              dispatchstage.StorageConfig
	Interval             time.Duration
	AllowedArtifactHosts []string
}

// SchedulerConfig enables the RSS poll cycle. The cycle stops at race job
// creation; dispatching those jobs requires the dispatch runtime, which stays
// gated on EnableDispatch and a VERIFIED profile.
type SchedulerConfig struct {
	Enabled              bool
	PollInterval         time.Duration
	AllowedArtifactHosts []string
	MatchFactory         matchpipeline.MatchFactory
}

// QuiConfig enables the qui control/profile/sync runtime. Dispatch remains
// disabled unless explicitly requested and an exact persisted verification
// campaign promotes the detected qB Profile to VERIFIED.
type QuiConfig struct {
	BaseURL                    string
	InstanceID                 string
	APIKey                     secrets.SecretRef
	ProxyKey                   secrets.SecretRef
	Store                      secrets.Store
	SnapshotRoot               string
	SyncInterval               time.Duration
	HTTPClient                 *http.Client
	ProfileVerificationBinding *profileverify.Binding
	VertexCommit               string
	VertexWorktreeClean        bool
	EnableDispatch             bool
	DispatchStage              *DispatchStageConfig
}

// Runtime owns resources initialized during service startup.
type Runtime struct {
	Database      *sql.DB
	Auth          *adminauth.Service
	Handler       http.Handler
	Profile       *qbprofile.Profile
	Dispatch      *dispatchflow.Runner
	Recovery      startuprecover.Result
	hub           *sseserver.Hub
	watcher       *syncwatcher.Watcher
	confirmations *confirmcoordinator.Coordinator
	scheduler     *schedulercycle.Cycle
	dispatchStage *dispatchstage.Stage
	cancel        context.CancelFunc
	workers       sync.WaitGroup
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
		if err := runtime.startQui(ctx, *config.Qui, config.Scheduler, config.Qui.DispatchStage, now); err != nil {
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

func (runtime *Runtime) startQui(ctx context.Context, config QuiConfig, schedulerConfig *SchedulerConfig, dispatchStageConfig *DispatchStageConfig, now func() time.Time) error {
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
	if config.ProfileVerificationBinding != nil {
		binding := *config.ProfileVerificationBinding
		binding.CurrentVertexCommit = strings.TrimSpace(config.VertexCommit)
		binding.CurrentWorktreeClean = config.VertexWorktreeClean
		if config.VertexWorktreeClean && strings.TrimSpace(config.VertexCommit) != "" &&
			binding.VertexCommit == strings.TrimSpace(config.VertexCommit) &&
			binding.TestSuiteRevision == profileverify.CurrentTestSuiteRevision &&
			binding.ProxyRouteFingerprint == proxy.RouteFingerprint() {
			profile, _, err = profileverify.Apply(ctx, runtime.Database, profile, binding, now().UTC())
			if err != nil {
				return fmt.Errorf("resolve persisted qB profile verification: %w", err)
			}
		}
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
		if dispatchStageConfig != nil && dispatchStageConfig.Enabled {
			stage, err := newDispatchStage(runtime.Database, dispatchStageConfig, proxyClient, profile, now)
			if err != nil {
				return fmt.Errorf("initialize dispatch stage: %w", err)
			}
			runtime.dispatchStage = stage
		}
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
	confirmations, err := confirmcoordinator.New(runtime.Database)
	if err != nil {
		return fmt.Errorf("initialize confirmation coordinator: %w", err)
	}
	runtime.confirmations = confirmations
	runtime.Profile = &profile
	runtime.watcher = watcher
	workerContext, cancel := context.WithCancel(context.Background())
	runtime.cancel = cancel
	if runtime.dispatchStage != nil {
		stageInterval := 30 * time.Second
		if dispatchStageConfig.Interval > 0 {
			stageInterval = dispatchStageConfig.Interval
		}
		runtime.workers.Add(1)
		go func() {
			defer runtime.workers.Done()
			ticker := time.NewTicker(stageInterval)
			defer ticker.Stop()
			for {
				select {
				case <-workerContext.Done():
					return
				case <-ticker.C:
					_, _, _ = runtime.dispatchStage.Run(workerContext)
				}
			}
		}()
	}
	if schedulerConfig != nil && schedulerConfig.Enabled {
		scheduler, err := newSchedulerCycle(runtime.Database, schedulerConfig, config.InstanceID)
		if err != nil {
			return fmt.Errorf("initialize scheduler: %w", err)
		}
		runtime.scheduler = scheduler
		pollInterval := schedulerConfig.PollInterval
		if pollInterval <= 0 {
			pollInterval = 5 * time.Minute
		}
		runtime.workers.Add(1)
		go func() {
			defer runtime.workers.Done()
			ticker := time.NewTicker(pollInterval)
			defer ticker.Stop()
			for {
				select {
				case <-workerContext.Done():
					return
				case <-ticker.C:
					_ = runtime.RunSchedulerCycle(workerContext)
				}
			}
		}()
	}
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
				runtime.tickSync(workerContext, now)
			}
		}
	}()
	return nil
}

// RunSchedulerCycle polls every enabled feed once: fetch, dedupe, download,
// identify, and match. Job creation is the end of the cycle; dispatching is
// owned by the dispatch runtime and never happens here.
func (runtime *Runtime) RunSchedulerCycle(ctx context.Context) error {
	if runtime.scheduler == nil {
		return fmt.Errorf("scheduler is not enabled")
	}
	feeds, err := runtime.databaseEnabledFeeds(ctx)
	if err != nil {
		return err
	}
	var firstErr error
	for _, feed := range feeds {
		if _, err := runtime.scheduler.Run(ctx, feed); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (runtime *Runtime) databaseEnabledFeeds(ctx context.Context) ([]schedulercycle.FeedRef, error) {
	rows, err := runtime.Database.QueryContext(ctx, `SELECT site_id, feed_id, feed_url FROM feeds WHERE enabled = 1`)
	if err != nil {
		return nil, fmt.Errorf("list enabled feeds: %w", err)
	}
	defer rows.Close()
	feeds := make([]schedulercycle.FeedRef, 0, 8)
	for rows.Next() {
		var feed schedulercycle.FeedRef
		if err := rows.Scan(&feed.SiteID, &feed.FeedID, &feed.URL); err != nil {
			return nil, err
		}
		feeds = append(feeds, feed)
	}
	return feeds, rows.Err()
}

func newDispatchStage(database *sql.DB, config *DispatchStageConfig, proxyClient *proxy.Client, profile qbprofile.Profile, now func() time.Time) (*dispatchstage.Stage, error) {
	downloader, err := torrentdownload.New(torrentdownload.Config{AllowedHosts: config.AllowedArtifactHosts})
	if err != nil {
		return nil, err
	}
	return dispatchstage.New(database, dispatchstage.Config{
		Storage:    config.Storage,
		Downloader: downloader,
		Profile:    profile,
		Sender:     proxyClient,
		Now:        now,
	})
}

func newSchedulerCycle(database *sql.DB, config *SchedulerConfig, instanceID string) (*schedulercycle.Cycle, error) {
	fetcher, err := rssfetch.New(nil, 1<<20)
	if err != nil {
		return nil, err
	}
	downloader, err := torrentdownload.New(torrentdownload.Config{AllowedHosts: config.AllowedArtifactHosts})
	if err != nil {
		return nil, err
	}
	sourceStore, err := rsssourcedb.New(database)
	if err != nil {
		return nil, err
	}
	dedupe, err := rsssource.New(sourceStore)
	if err != nil {
		return nil, err
	}
	rulesService, err := ruleservice.New(database, nil)
	if err != nil {
		return nil, err
	}
	jobStore, err := racejobstore.New(database)
	if err != nil {
		return nil, err
	}
	creator, err := racejob.New(jobStore, nil)
	if err != nil {
		return nil, err
	}
	factory := config.MatchFactory
	if factory == nil {
		factory = defaultMatchFactory(instanceID)
	}
	return schedulercycle.New(fetcher, downloader, dedupe, matchpipeline.Deps{
		Rules: &rulesService, JobCreator: creator, MatchFactory: factory,
	}, 1<<20)
}

// defaultMatchFactory targets the configured qui instance unless the rule
// definition carries its own action set.
func defaultMatchFactory(instanceID string) matchpipeline.MatchFactory {
	return func(rule ruleservice.Rule) (ruleaggregate.Match, error) {
		definition := struct {
			ActionSetJSON string `json:"action_set_json"`
		}{}
		if err := json.Unmarshal(rule.Definition, &definition); err != nil {
			return ruleaggregate.Match{}, fmt.Errorf("decode rule %s definition: %w", rule.ID, err)
		}
		if definition.ActionSetJSON == "" {
			return ruleaggregate.Match{TargetMode: "FIXED_INSTANCE", TargetInstanceID: instanceID, ParticipationWindow: time.Hour}, nil
		}
		actions := struct {
			TargetMode       string `json:"target_mode"`
			TargetInstanceID string `json:"target_instance_id"`
			Window           int    `json:"participation_window_minutes"`
		}{}
		if err := json.Unmarshal([]byte(definition.ActionSetJSON), &actions); err != nil {
			return ruleaggregate.Match{}, fmt.Errorf("decode rule %s action set: %w", rule.ID, err)
		}
		window := time.Hour
		if actions.Window > 0 {
			window = time.Duration(actions.Window) * time.Minute
		}
		match := ruleaggregate.Match{TargetMode: actions.TargetMode, TargetInstanceID: actions.TargetInstanceID, ParticipationWindow: window}
		return match, nil
	}
}

// TickSync performs one sync cycle synchronously: fetch the delta, drive the
// confirmation coordinator over the resulting snapshot, and refresh the
// instance state. Exposed for tests and diagnostic tooling.
func (runtime *Runtime) TickSync(ctx context.Context) error {
	runtime.tickSync(ctx, tickSyncNow(time.Now()))
	return nil
}

// tickSyncNow adapts an absolute time into the now-func shape.
func tickSyncNow(at time.Time) func() time.Time {
	return func() time.Time { return at }
}

func (runtime *Runtime) tickSync(ctx context.Context, now func() time.Time) {
	state := "READY"
	event, err := runtime.watcher.RunOnce(ctx)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		_ = updateSyncState(ctx, runtime.Database, event.InstanceID, "STALE", now())
		return
	}
	// The confirmation coordinator only touches chains created by the
	// dispatcher; a failed cycle is retried on the next tick because every
	// operation it performs is idempotent.
	_, _ = runtime.confirmations.OnSyncEvent(ctx, confirmcoordinator.Event{
		InstanceID: event.InstanceID,
		Generation: event.Generation,
		Revision:   event.Revision,
		Snapshot:   event.Snapshot,
	}, now())
	_ = updateSyncState(ctx, runtime.Database, event.InstanceID, state, now())
}

func updateSyncState(ctx context.Context, database *sql.DB, instanceID, state string, updatedAt time.Time) error {
	if _, err := database.ExecContext(ctx, `UPDATE sync_instance_state SET sync_state = ?, updated_at = ? WHERE instance_id = ?`, state, updatedAt.UTC().Format(time.RFC3339Nano), instanceID); err != nil {
		return fmt.Errorf("update qui sync state: %w", err)
	}
	return nil
}
