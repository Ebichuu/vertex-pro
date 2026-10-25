package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/adminauth"
	"github.com/vertex-pro/vertex-pro/internal/confirmcoordinator"
	"github.com/vertex-pro/vertex-pro/internal/db"
	"github.com/vertex-pro/vertex-pro/internal/dispatchflow"
	"github.com/vertex-pro/vertex-pro/internal/dispatchstage"
	"github.com/vertex-pro/vertex-pro/internal/httpapi"
	"github.com/vertex-pro/vertex-pro/internal/matchpipeline"
	"github.com/vertex-pro/vertex-pro/internal/metrics"
	"github.com/vertex-pro/vertex-pro/internal/profileverify"
	"github.com/vertex-pro/vertex-pro/internal/qbprofile"
	"github.com/vertex-pro/vertex-pro/internal/qui/control"
	"github.com/vertex-pro/vertex-pro/internal/qui/proxy"
	"github.com/vertex-pro/vertex-pro/internal/quipolicy"
	"github.com/vertex-pro/vertex-pro/internal/racejob"
	"github.com/vertex-pro/vertex-pro/internal/racejobstore"
	"github.com/vertex-pro/vertex-pro/internal/realtimecards"
	"github.com/vertex-pro/vertex-pro/internal/resourcesapi"
	"github.com/vertex-pro/vertex-pro/internal/retention"
	"github.com/vertex-pro/vertex-pro/internal/rssfetch"
	"github.com/vertex-pro/vertex-pro/internal/rsspreview"
	"github.com/vertex-pro/vertex-pro/internal/rssservice"
	"github.com/vertex-pro/vertex-pro/internal/rsssource"
	"github.com/vertex-pro/vertex-pro/internal/rsssourcedb"
	"github.com/vertex-pro/vertex-pro/internal/ruleaggregate"
	"github.com/vertex-pro/vertex-pro/internal/ruleservice"
	"github.com/vertex-pro/vertex-pro/internal/schedulercycle"
	"github.com/vertex-pro/vertex-pro/internal/secrets"
	"github.com/vertex-pro/vertex-pro/internal/settings"
	"github.com/vertex-pro/vertex-pro/internal/snapshot"
	"github.com/vertex-pro/vertex-pro/internal/sseserver"
	"github.com/vertex-pro/vertex-pro/internal/startuprecover"
	"github.com/vertex-pro/vertex-pro/internal/syncmerge"
	"github.com/vertex-pro/vertex-pro/internal/syncwatcher"
	"github.com/vertex-pro/vertex-pro/internal/torrentdownload"
	"github.com/vertex-pro/vertex-pro/internal/webembed"
)

// Config contains the dependencies needed to start the single-process service.
// Startup recovery is always run before HTTP traffic is exposed. External
// mutation workers remain opt-in and are not started by this constructor.
type Config struct {
	Version                      string
	Database                     db.Config
	Hosts                        []string
	Origins                      []string
	Now                          func() time.Time
	RecoveryConfirmationWindow   time.Duration
	RecoveryReconciliationWindow time.Duration
	Qui                          *QuiConfig
	QuiInstances                 []QuiConfig
	Scheduler                    *SchedulerConfig
	DispatchStage                *DispatchStageConfig
	Retention                    *RetentionConfig
	// HardenedOutbound 启用 HARD-04 出站边界：外发请求拒绝私网/环回/链路
	// 本地目标并做 DNS 解析校验。本地开发与测试保持关闭。
	HardenedOutbound bool
}

// RetentionConfig enables the OBS-03 cleanup worker. An empty snapshot root
// skips snapshot pruning (events, commands, idempotency, and stats are swept
// regardless); the sweep enforces the expires_at facts writers persist.
type RetentionConfig struct {
	Interval     time.Duration
	SnapshotRoot string
	Policy       retention.Policy
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
	Settings      settings.Settings
	rssFetcher    rssfetch.Fetcher
	Database      *sql.DB
	Auth          *adminauth.Service
	Handler       http.Handler
	Metrics       *metrics.Registry
	Profile       *qbprofile.Profile
	Dispatch      *dispatchflow.Runner
	Recovery      startuprecover.Result
	hub           *sseserver.Hub
	watcher       *syncwatcher.Watcher
	controlClient *control.Client
	realtime      *realtimecards.Registry
	instanceID    string
	syncMu        sync.Mutex
	observationMu sync.RWMutex
	// Protected by observationMu. Published evidence is immutable; capacity
	// and final-send authorization are always revalidated in the database.
	selectionEvidence *dispatchstage.Evidence
	monitorSnapshot   atomic.Pointer[syncmerge.Snapshot]
	schedulerMu       sync.Mutex
	now               func() time.Time
	confirmations     *confirmcoordinator.Coordinator
	scheduler         *schedulercycle.Cycle
	dispatchStage     *dispatchstage.Stage
	snapshotRoot      string
	hardenedOutbound  bool
	cancel            context.CancelFunc
	// retentionCancel 停止保留策略 worker；与 qui 的 cancel 独立，
	// 因为保留清理不依赖 qui 集成是否启用。
	retentionCancel context.CancelFunc
	workers         sync.WaitGroup
	children        []*Runtime
	proxyClient     *proxy.Client
	startWorkers    func()
	allocateWake    chan struct{}
	prepareWake     chan struct{}
	onObservation   func()
	preparationMu   sync.Mutex
	preparing       map[string]int
	polling         map[string]bool
	prepareCursor   int
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
	settingsService, err := settings.New(database, now)
	if err != nil {
		return nil, err
	}
	activeSettings, err := settingsService.Load(ctx)
	if err != nil {
		return nil, fmt.Errorf("load runtime settings: %w", err)
	}
	if config.Scheduler != nil {
		schedulerConfig := *config.Scheduler
		if schedulerConfig.PollInterval <= 0 {
			schedulerConfig.PollInterval = time.Duration(activeSettings.RSSPollIntervalMinutes) * time.Minute
		}
		config.Scheduler = &schedulerConfig
	}
	if config.Retention != nil {
		retentionConfig := *config.Retention
		retentionConfig.Policy.StatsRawRetention = time.Duration(activeSettings.StatsRawRetentionDays) * 24 * time.Hour
		retentionConfig.Policy.StatsHourly = time.Duration(activeSettings.StatsHourlyRetentionDays) * 24 * time.Hour
		config.Retention = &retentionConfig
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
	boundary := rssfetch.Boundary{RejectPrivateIPs: config.HardenedOutbound}
	feedFetcher, err := rssfetch.New(nil, rssfetch.DefaultMaxBodyBytes)
	if err != nil {
		return nil, err
	}
	feedFetcher = feedFetcher.WithBoundary(boundary)
	limits, err := rssfetch.NewLimitsWithPreparationLimit(activeSettings.RateLimitPerMinute, activeSettings.MaxConcurrentRSSFetches, activeSettings.RequestBurst, activeSettings.MaxPreparationsPerHost)
	if err != nil {
		return nil, err
	}
	feedFetcher = feedFetcher.WithLimits(limits)

	rule, err := ruleservice.New(database, ruleservice.ClockFunc(now))
	if err != nil {
		return nil, err
	}

	hub := sseserver.NewHub()
	realtime := realtimecards.NewRegistry(hub, 30*time.Second)
	events := &sseserver.Server{Database: database, Hub: hub}
	web := &httpapi.WebProtection{AllowedHosts: config.Hosts, AllowedOrigins: config.Origins}
	var runtime *Runtime
	api := resourcesapi.Router(resourcesapi.Deps{
		SyncPolicies:   func(ctx context.Context) error { return runtime.syncDisabledPolicies(ctx) },
		Settings:       settingsService,
		ActiveSettings: activeSettings.Values,
		SessionTTL:     time.Duration(activeSettings.SessionTTLHours) * time.Hour,
		Version:        config.Version,
		SnapshotRoot: func() string {
			if config.Qui != nil {
				return config.Qui.SnapshotRoot
			}
			if len(config.QuiInstances) > 0 {
				return config.QuiInstances[0].SnapshotRoot
			}
			return ""
		}(),
		Database: database,
		Auth:     auth,
		Web:      web,
		Realtime: realtime,
		RSS:      &rss,
		FeedTest: func(ctx context.Context, feed rssservice.Feed) (rsspreview.Preview, error) {
			var credential, origin string
			if err := database.QueryRowContext(ctx, `SELECT credential_ref,base_url FROM sites WHERE site_id=?`, feed.SiteID).Scan(&credential, &origin); err != nil {
				return rsspreview.Preview{}, err
			}
			result, err := feedFetcher.Fetch(ctx, rssfetch.Request{URL: feed.FeedURL, CredentialRef: credential, CredentialOrigin: origin})
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
	snapshotRoot := ""
	if config.Retention != nil {
		snapshotRoot = config.Retention.SnapshotRoot
	}
	runtime = &Runtime{Database: database, Auth: auth, Handler: mux, Metrics: metrics.New(), Recovery: recovery, hub: hub, snapshotRoot: snapshotRoot, hardenedOutbound: config.HardenedOutbound, Settings: activeSettings, rssFetcher: feedFetcher, allocateWake: make(chan struct{}, 1), prepareWake: make(chan struct{}, 8), preparing: map[string]int{}, polling: map[string]bool{}}
	runtime.realtime, runtime.now = realtime, now
	initialized := false
	defer func() {
		if !initialized {
			runtime.stopWorkers()
		}
	}()
	quiConfigs := append([]QuiConfig{}, config.QuiInstances...)
	if config.Qui != nil {
		quiConfigs = append([]QuiConfig{*config.Qui}, quiConfigs...)
	}
	seenInstances := map[string]bool{}
	for index, quiConfig := range quiConfigs {
		if seenInstances[quiConfig.InstanceID] {
			return nil, fmt.Errorf("duplicate qui instance %s", quiConfig.InstanceID)
		}
		seenInstances[quiConfig.InstanceID] = true
		worker := runtime
		if index > 0 {
			worker = &Runtime{Database: database, Metrics: runtime.Metrics, hub: hub, realtime: realtime, now: now, Settings: activeSettings, rssFetcher: feedFetcher, hardenedOutbound: config.HardenedOutbound, allocateWake: make(chan struct{}, 1)}
			runtime.children = append(runtime.children, worker)
		}
		stageConfig := quiConfig.DispatchStage
		if index == 0 && stageConfig == nil {
			stageConfig = config.DispatchStage
		}
		if err := worker.startQui(ctx, quiConfig, stageConfig, now); err != nil {
			return nil, err
		}
	}
	var stages []*dispatchstage.Stage
	for _, worker := range append([]*Runtime{runtime}, runtime.children...) {
		if worker.dispatchStage != nil {
			stages = append(stages, worker.dispatchStage)
		}
	}
	dispatchstage.Connect(stages...)
	for _, worker := range append([]*Runtime{runtime}, runtime.children...) {
		worker.onObservation = runtime.wakeDispatch
		if worker.dispatchStage != nil {
			w := worker
			cache := newPolicyCache(quipolicy.Service{DB: database, Client: w.controlClient, Instance: w.instanceID, Now: now})
			worker.dispatchStage.SetPolicyCache(cache.ready, cache.warm)
		}
	}

	if config.Scheduler != nil && config.Scheduler.Enabled {
		if err := runtime.startScheduler(config.Scheduler); err != nil {
			return nil, err
		}
	}
	// No external mutation worker starts until every configured instance and
	// the scheduler have initialized successfully.
	if runtime.startWorkers != nil {
		runtime.startWorkers()
	}
	for _, child := range runtime.children {
		if child.startWorkers != nil {
			child.startWorkers()
		}
	}
	if config.Retention != nil {
		runtime.startRetention(config.Retention, now)
	}
	closeDatabase = false
	initialized = true
	return runtime, nil
}

// startRetention runs one cleanup sweep immediately (clearing any backlog from
// previous runs) and then on the configured interval. It runs independently of
// the qui integration: events, commands, idempotency, and stats exist whether
// or not a downloader is configured.
func (runtime *Runtime) startRetention(config *RetentionConfig, now func() time.Time) {
	workerContext, cancel := context.WithCancel(context.Background())
	runtime.retentionCancel = cancel
	interval := config.Interval
	if interval <= 0 {
		interval = time.Hour
	}
	policy := config.Policy
	runtime.workers.Add(1)
	go func() {
		defer runtime.workers.Done()
		sweep := func() {
			if _, err := runtime.RunRetentionSweep(workerContext, policy, now); err != nil {
				return
			}
		}
		sweep()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-workerContext.Done():
				return
			case <-ticker.C:
				sweep()
			}
		}
	}()
}

// RunRetentionSweep performs one OBS-03 cleanup pass and counts what it
// removed. Label values are fixed kinds, keeping the metrics family bounded.
func (runtime *Runtime) RunRetentionSweep(ctx context.Context, policy retention.Policy, now func() time.Time) (retention.Result, error) {
	result, err := retention.Sweep(ctx, runtime.Database, runtime.snapshotRoot, policy, now())
	if err != nil {
		return result, err
	}
	for kind, count := range map[string]int64{
		"events":       result.Events,
		"system":       result.SystemEvents,
		"commands":     result.Commands,
		"idempotency":  result.IdempotencyRecords,
		"snapshots":    result.Snapshots,
		"stats_raw":    result.StatsRaw,
		"stats_hourly": result.StatsHourly,
	} {
		if count > 0 {
			runtime.Metrics.Add("vertex_retention_deleted_total", map[string]string{"kind": kind}, uint64(count))
		}
	}
	return result, nil
}

// Close stops all owned workers before releasing the runtime database.
// Hub subscribers are closed by their request contexts.
func (runtime *Runtime) Close() error {
	if runtime == nil || runtime.Database == nil {
		return nil
	}
	runtime.stopWorkers()
	return runtime.Database.Close()
}

func (runtime *Runtime) stopWorkers() {
	for _, child := range runtime.children {
		child.stopWorkers()
	}
	if runtime.cancel != nil {
		runtime.cancel()
	}
	if runtime.retentionCancel != nil {
		runtime.retentionCancel()
	}
	runtime.workers.Wait()
	if runtime.scheduler != nil {
		runtime.scheduler.Close()
	}
	if runtime.watcher != nil {
		runtime.watcher.Close()
	}
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

func (runtime *Runtime) startQui(ctx context.Context, config QuiConfig, dispatchStageConfig *DispatchStageConfig, now func() time.Time) error {
	if dispatchStageConfig != nil && dispatchStageConfig.Enabled && dispatchStageConfig.Storage.InstanceID != config.InstanceID {
		return errors.New("dispatch storage instance does not match the configured qui instance")
	}
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
	runtime.controlClient, runtime.instanceID = controlClient, config.InstanceID
	proxyClient, err := proxy.New(ctx, proxy.Config{BaseURL: config.BaseURL, ProxyKey: config.ProxyKey, Store: config.Store, HTTPClient: config.HTTPClient})
	if err != nil {
		return err
	}
	runtime.proxyClient = proxyClient
	runtime.snapshotRoot = config.SnapshotRoot
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
			stage, err := newDispatchStage(runtime.Database, dispatchStageConfig, proxyClient, profile, now, runtime.hardenedOutbound, runtime.collectCachedEvidence, runtime.rssFetcher)
			if err != nil {
				return fmt.Errorf("initialize dispatch stage: %w", err)
			}
			runtime.dispatchStage = stage
			stage.SetSelectionCollector(runtime.collectCachedEvidence)
			if config.ProfileVerificationBinding != nil {
				var raw string
				if err := runtime.Database.QueryRowContext(ctx, `SELECT MIN(expires_at) FROM profile_verification_runs WHERE campaign_id=? AND profile_id=? AND verification_state='VERIFIED'`, config.ProfileVerificationBinding.CampaignID, profile.ID).Scan(&raw); err != nil {
					return fmt.Errorf("load verification expiry: %w", err)
				}
				at, err := time.Parse(time.RFC3339Nano, raw)
				if err != nil {
					return err
				}
				stage.SetProfileExpiry(at)
			}
		}
	}
	if config.ProfileVerificationBinding != nil {
		// The profile decision is a startup fact; count it so /metrics shows
		// whether the deployed build consumed a verification campaign and
		// with what fail-closed outcome. Labels stay in fixed sets.
		decision := profile.ProfileVerificationState
		if profile.ProfileVerificationState != "VERIFIED" {
			decision = "NOT_VERIFIED"
		}
		runtime.Metrics.Inc("vertex_profile_verifications_total", map[string]string{"profile": profile.ID, "decision": decision})
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
	initialEvent, err := watcher.RunOnce(ctx)
	if err != nil {
		return fmt.Errorf("initialize qui sync: %w", err)
	}
	runtime.monitorSnapshot.Store(&initialEvent.Snapshot)
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
	if err := runtime.refreshInstanceMetadata(ctx, now()); err != nil {
		return fmt.Errorf("initialize instance availability: %w", err)
	}
	if err := runtime.refreshCapacityAt(ctx, now()); err != nil {
		runtime.Metrics.Inc("vertex_capacity_refresh_errors_total", map[string]string{"instance": runtime.instanceID})
	}
	runtime.startWorkers = func() { runtime.startQuiWorkers(config, dispatchStageConfig, now) }
	return nil
}

func (runtime *Runtime) startQuiWorkers(config QuiConfig, dispatchStageConfig *DispatchStageConfig, now func() time.Time) {
	workerContext, cancel := context.WithCancel(context.Background())
	previousCancel := runtime.cancel
	runtime.cancel = func() {
		cancel()
		if previousCancel != nil {
			previousCancel()
		}
	}
	if runtime.dispatchStage != nil {
		stageInterval := 30 * time.Second
		if dispatchStageConfig.Interval > 0 {
			stageInterval = dispatchStageConfig.Interval
		}
		runtime.startPipelineWorkers(workerContext, stageInterval)
	}
	interval := config.SyncInterval
	if interval <= 0 {
		interval = 10 * time.Second
	}
	runtime.startMonitoringWorker(workerContext, interval)
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
}

// RunSchedulerCycle runs due, unclaimed feeds through discovery and preparation.
// Background workers may already own a poll or consumer, so returning does not
// imply all discovered work has finished. It never sends an Add request.
func (runtime *Runtime) RunSchedulerCycle(ctx context.Context) error {
	runtime.schedulerMu.Lock()
	defer runtime.schedulerMu.Unlock()
	if runtime.scheduler == nil {
		return fmt.Errorf("scheduler is not enabled")
	}
	feeds, err := runtime.databaseEnabledFeeds(ctx)
	if err != nil {
		return err
	}
	return runFeedGroups(ctx, feeds, 4, func(ctx context.Context, feed schedulercycle.FeedRef) error {
		// Manual cycles share cadence ownership with background discovery.
		runtime.preparationMu.Lock()
		fresh, err := runtime.databaseEnabledFeeds(ctx)
		found := false
		for _, current := range fresh {
			if current.FeedID == feed.FeedID {
				feed = current
				found = true
				break
			}
		}
		if err != nil || !found {
			runtime.preparationMu.Unlock()
			return err
		}
		keys := feedBudgetKeys(feed)
		for _, key := range keys {
			if runtime.polling[key] {
				runtime.preparationMu.Unlock()
				return nil
			}
		}
		for _, key := range keys {
			runtime.polling[key] = true
		}
		runtime.preparationMu.Unlock()
		defer func() {
			runtime.preparationMu.Lock()
			for _, key := range keys {
				delete(runtime.polling, key)
			}
			runtime.preparationMu.Unlock()
		}()
		result, err := runtime.scheduler.Run(ctx, feed)
		if !feed.RecoverOnly {
			if saveErr := runtime.recordFeedPoll(ctx, feed, result, err); saveErr != nil {
				return saveErr
			}
		}
		if err != nil {
			if !feed.RecoverOnly {
				runtime.Metrics.Inc("vertex_rss_feeds_total", map[string]string{"result": "error"})
			}
			return err
		}
		if !feed.RecoverOnly {
			runtime.Metrics.Inc("vertex_rss_feeds_total", map[string]string{"result": "ok"})
		}
		for outcome, count := range map[string]uint64{
			"new":       uint64(result.New),
			"updated":   uint64(result.Updated),
			"duplicate": uint64(result.Duplicates),
			"matched":   uint64(result.Matched),
			"skipped":   uint64(result.Skipped),
			"failed":    uint64(result.Failed),
		} {
			if count > 0 {
				runtime.Metrics.Add("vertex_rss_items_total", map[string]string{"outcome": outcome}, count)
			}
		}
		return nil
	})
}

func (runtime *Runtime) databaseEnabledFeeds(ctx context.Context) ([]schedulercycle.FeedRef, error) {
	now := runtime.now()
	// Consumer timestamps have fixed nanosecond precision; preserve it for
	// indexed lexical comparisons, including the exact retry/deadline instant.
	stamp := now.UTC().Format("2006-01-02T15:04:05.000000000Z")
	rows, err := runtime.Database.QueryContext(ctx, `SELECT f.site_id, f.feed_id, f.feed_url,COALESCE(f.etag,''),COALESCE(f.last_modified,''),COALESCE(r.initialized,0),COALESCE(r.last_polled_at,''),f.poll_minutes,s.credential_ref,s.base_url,
		EXISTS(SELECT 1 FROM rss_source_consumers c WHERE c.site_id=f.site_id AND c.feed_id=f.feed_id AND c.state='PENDING' AND (c.next_attempt_at<=? OR c.deadline_at<=?))
		FROM feeds f JOIN sites s ON s.site_id=f.site_id LEFT JOIN feed_runtime_state r ON r.feed_id=f.feed_id WHERE f.enabled = 1 AND s.enabled = 1`, stamp, stamp)
	if err != nil {
		return nil, fmt.Errorf("list enabled feeds: %w", err)
	}
	defer rows.Close()
	feeds := make([]schedulercycle.FeedRef, 0, 8)
	for rows.Next() {
		var feed schedulercycle.FeedRef
		var initialized, pendingDue bool
		var lastPolled string
		var interval int
		if err := rows.Scan(&feed.SiteID, &feed.FeedID, &feed.URL, &feed.ETag, &feed.LastModified, &initialized, &lastPolled, &interval, &feed.CredentialRef, &feed.CredentialOrigin, &pendingDue); err != nil {
			return nil, err
		}
		if lastPolled != "" {
			at, err := time.Parse(time.RFC3339Nano, lastPolled)
			if err != nil {
				return nil, fmt.Errorf("invalid feed polling timestamp: %w", err)
			}
			if now.Sub(at) < time.Duration(interval)*time.Minute {
				if !initialized || !pendingDue {
					continue
				}
				feed.RecoverOnly = true
			}
		}
		feed.BaselineOnly = !initialized
		if !initialized {
			feed.ETag, feed.LastModified = "", ""
		}
		feeds = append(feeds, feed)
	}
	return feeds, rows.Err()
}

func newDispatchStage(database *sql.DB, config *DispatchStageConfig, proxyClient *proxy.Client, profile qbprofile.Profile, now func() time.Time, hardened bool, collect func(context.Context) (dispatchstage.Evidence, func(), error), fetcher rssfetch.Fetcher) (*dispatchstage.Stage, error) {
	downloader, err := torrentdownload.New(torrentdownload.Config{Client: fetcher.ArtifactClient(30 * time.Second), AllowedHosts: config.AllowedArtifactHosts, RejectPrivateIPs: hardened})
	if err != nil {
		return nil, err
	}
	return dispatchstage.New(database, dispatchstage.Config{
		DurableQueue: true,
		Storage:      config.Storage,
		Downloader:   downloader,
		Profile:      profile,
		Sender:       proxyClient,
		Now:          now,
		Collect:      collect,
	})
}

func newSchedulerCycle(database *sql.DB, config *SchedulerConfig, instanceID string, fetcher rssfetch.Fetcher, hardened bool) (*schedulercycle.Cycle, error) {
	downloader, err := torrentdownload.New(torrentdownload.Config{Client: fetcher.ArtifactClient(30 * time.Second), AllowedHosts: config.AllowedArtifactHosts, RejectPrivateIPs: hardened})
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
		return ruleservice.MatchFor(rule, instanceID)
	}
}

// TickSync performs one sync cycle synchronously: fetch the delta, drive the
// confirmation coordinator over the resulting snapshot, and refresh the
// instance state. Exposed for tests and diagnostic tooling.
func (runtime *Runtime) TickSync(ctx context.Context) error {
	if runtime.watcher == nil {
		return errors.New("qui sync is not enabled")
	}
	return runtime.tickSync(ctx, runtime.now)
}

// tickSyncNow adapts an absolute time into the now-func shape.
func tickSyncNow(at time.Time) func() time.Time {
	return func() time.Time { return at }
}

func (runtime *Runtime) tickSync(ctx context.Context, now func() time.Time) error {
	runtime.syncMu.Lock()
	defer runtime.syncMu.Unlock()
	return runtime.tickSyncLocked(ctx, now)
}

// tickSyncLocked requires syncMu; durable senders read published observations.
func (runtime *Runtime) tickSyncLocked(ctx context.Context, now func() time.Time) error {
	return runtime.syncEvidenceLocked(ctx, now, true)
}

// The pre-Add path retains fresh snapshots, confirmation and capacity facts,
// but leaves policy shutdown, history and transfer sampling to regular ticks.
func (runtime *Runtime) syncEvidenceLocked(ctx context.Context, now func() time.Time, background bool) error {
	// Keep a validated observation until replacement or failure. Readers still
	// verify its TTL and exact persisted revision without waiting on syncMu.
	observedAt := now().UTC()
	// Fetch availability before publishing a new snapshot revision. Optional
	// monitoring and policy shutdown run independently of this sync worker.
	if background && runtime.controlClient != nil {
		if err := runtime.refreshInstanceMetadata(ctx, observedAt); err != nil {
			runtime.storeObservation(nil)
			_, deadlineErr := runtime.confirmations.OnSyncUnavailable(ctx, runtime.instanceID, now())
			return errors.Join(err, deadlineErr)
		}
	}
	var preferences map[string]any
	var preferenceErr error
	if background && runtime.dispatchStage != nil {
		preferences, preferenceErr = runtime.proxyClient.Preferences(ctx)
	}
	state := "READY"
	event, err := runtime.watcher.RunOnce(ctx)
	if err != nil {
		runtime.storeObservation(nil)
		if errors.Is(err, context.Canceled) {
			return err
		}
		runtime.Metrics.Inc("vertex_sync_ticks_total", map[string]string{"result": "stale"})
		_ = updateSyncState(ctx, runtime.Database, runtime.instanceID, "STALE", now())
		// A missing snapshot cannot confirm absence, but confirmation and
		// reconciliation deadlines must still advance without releasing debt.
		outcomes, deadlineErr := runtime.confirmations.OnSyncUnavailable(ctx, runtime.instanceID, now())
		for _, outcome := range outcomes {
			runtime.Metrics.Inc("vertex_unknown_confirmations_total", map[string]string{"action": outcome.Action})
		}
		return errors.Join(err, deadlineErr)
	}
	runtime.monitorSnapshot.Store(&event.Snapshot)
	// The confirmation coordinator only touches chains created by the
	// dispatcher; a failed cycle is retried on the next tick because every
	// operation it performs is idempotent.
	outcomes, confirmErr := runtime.confirmations.OnSyncEvent(ctx, confirmcoordinator.Event{
		InstanceID: event.InstanceID,
		Generation: event.Generation,
		Revision:   event.Revision,
		Snapshot:   event.Snapshot,
	}, now())
	if confirmErr != nil {
		runtime.Metrics.Inc("vertex_sync_ticks_total", map[string]string{"result": "error"})
	} else {
		for _, outcome := range outcomes {
			runtime.Metrics.Inc("vertex_unknown_confirmations_total", map[string]string{"action": outcome.Action})
		}
		runtime.Metrics.Inc("vertex_sync_ticks_total", map[string]string{"result": "ok"})
	}
	_ = updateSyncState(ctx, runtime.Database, event.InstanceID, state, now())
	if background {
		if err := runtime.observeJobs(ctx, event, now()); err != nil {
			runtime.Metrics.Inc("vertex_job_observation_errors_total", nil)
		}
	}
	if background {
		evidence, err := runtime.capacityFromPreferences(ctx, observedAt, preferences)
		if preferenceErr != nil {
			evidence, err = nil, preferenceErr
		}
		if err == nil && evidence != nil {
			err = runtime.dispatchStage.RefreshCapacity(ctx, *evidence)
		}
		if err != nil {
			runtime.Metrics.Add("vertex_capacity_refresh_errors_total", map[string]string{"instance": runtime.instanceID}, 1)
		}
		// The control plane already confirmed the instance is enabled.
		// Capacity can remain uncertain while positive identity evidence is useful.
		runtime.storeObservation(evidence)
	}
	if background && runtime.onObservation != nil {
		runtime.onObservation()
	}
	return confirmErr
}

func updateSyncState(ctx context.Context, database *sql.DB, instanceID, state string, updatedAt time.Time) error {
	if _, err := database.ExecContext(ctx, `UPDATE sync_instance_state SET sync_state = ?, updated_at = ? WHERE instance_id = ?`, state, updatedAt.UTC().Format(time.RFC3339Nano), instanceID); err != nil {
		return fmt.Errorf("update qui sync state: %w", err)
	}
	return nil
}
