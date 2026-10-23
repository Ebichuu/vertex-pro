package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/app"
	"github.com/vertex-pro/vertex-pro/internal/buildidentity"
	"github.com/vertex-pro/vertex-pro/internal/db"
	"github.com/vertex-pro/vertex-pro/internal/diagnostics"
	"github.com/vertex-pro/vertex-pro/internal/observability"
	"github.com/vertex-pro/vertex-pro/internal/profileverify"
	"github.com/vertex-pro/vertex-pro/internal/retention"
	"github.com/vertex-pro/vertex-pro/internal/secrets"
)

const version = "0.0.0-dev"

func main() {
	logger := observability.NewJSONLogger(os.Stdout)
	command := "serve"
	if len(os.Args) > 1 {
		command = os.Args[1]
	}

	switch command {
	case "serve":
		if err := runServe(logger, os.Args[2:]); err != nil {
			logger.Error("service stopped", "error", err)
			os.Exit(1)
		}
	case "doctor":
		if err := runDoctor(logger, os.Args[2:]); err != nil {
			logger.Error("doctor failed", "error", err)
			os.Exit(1)
		}
	case "verify":
		if err := runVerify(os.Args[2:]); err != nil {
			os.Exit(1)
		}
	case "readiness":
		if err := runReadiness(os.Args[2:]); err != nil {
			os.Exit(1)
		}
	case "support-bundle":
		if err := runSupportBundle(os.Args[2:]); err != nil {
			logger.Error("support bundle failed", "error", err)
			os.Exit(1)
		}
	case "backup":
		if err := runBackup(os.Args[2:]); err != nil {
			logger.Error("backup failed", "error", err)
			os.Exit(1)
		}
	case "restore":
		if err := runRestore(os.Args[2:]); err != nil {
			logger.Error("restore failed", "error", err)
			os.Exit(1)
		}
	case "restore-drill":
		if err := runRestoreDrill(os.Args[2:]); err != nil {
			logger.Error("restore drill failed", "error", err)
			os.Exit(1)
		}
	case "version":
		fmt.Println(version)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", command)
		fmt.Fprintln(os.Stderr, "usage: vertex-pro [serve|doctor|verify|readiness|support-bundle|backup|restore|restore-drill|version]")
		os.Exit(2)
	}
}

// runDoctor prints the OBS-04 diagnostics bundle: redacted config summary,
// build identity, and live status checks as one digest-stamped JSON document.
func runDoctor(logger *slog.Logger, arguments []string) error {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	databasePath := flags.String("database", envOrDefault("VERTEX_PRO_DATABASE", "vertex-pro.db"), "SQLite database path")
	allowedHosts := flags.String("allowed-hosts", "localhost,127.0.0.1,[::1]", "comma-separated allowed HTTP hosts")
	allowedOrigins := flags.String("allowed-origins", "http://localhost:8080,http://127.0.0.1:8080,http://[::1]:8080", "comma-separated allowed browser origins")
	quiURL := flags.String("qui-url", strings.TrimSpace(os.Getenv("VERTEX_PRO_QUI_URL")), "qui base URL; empty disables qui integration")
	enableDispatch := flags.Bool("enable-dispatch", envBool("VERTEX_PRO_ENABLE_DISPATCH"), "dispatch enablement flag")
	if err := flags.Parse(arguments); err != nil {
		return err
	}

	var database *sql.DB
	if path := strings.TrimSpace(*databasePath); path != "" {
		if _, err := os.Stat(path); err != nil {
			// 不存在的数据库不做创建——doctor 是只读诊断工具。
			logger.Error("doctor could not find the database file", "path", path)
		} else {
			opened, err := db.Open(context.Background(), db.Config{Path: path})
			if err != nil {
				logger.Error("doctor could not open the database", "error", err)
			} else {
				database = opened
				defer database.Close()
			}
		}
	}

	bundle, err := diagnostics.Build(version, buildidentity.Current(), map[string]any{
		"database_path":   *databasePath,
		"allowed_hosts":   *allowedHosts,
		"allowed_origins": *allowedOrigins,
		"qui_base_url":    *quiURL,
		"enable_dispatch": *enableDispatch,
	}, database, time.Now())
	if err != nil {
		return err
	}
	payload, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(payload))
	return nil
}

func runServe(logger *slog.Logger, arguments []string) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	address := flags.String("address", ":8080", "HTTP listen address")
	databasePath := flags.String("database", envOrDefault("VERTEX_PRO_DATABASE", "vertex-pro.db"), "SQLite database path")
	allowedHosts := flags.String("allowed-hosts", "localhost,127.0.0.1,[::1]", "comma-separated allowed HTTP hosts")
	allowedOrigins := flags.String("allowed-origins", "http://localhost:8080,http://127.0.0.1:8080,http://[::1]:8080", "comma-separated allowed browser origins")
	quiURL := flags.String("qui-url", strings.TrimSpace(os.Getenv("VERTEX_PRO_QUI_URL")), "qui base URL; empty disables qui integration")
	quiInstanceID := flags.String("qui-instance-id", strings.TrimSpace(os.Getenv("VERTEX_PRO_QUI_INSTANCE_ID")), "qui instance ID")
	snapshotRoot := flags.String("snapshot-root", envOrDefault("VERTEX_PRO_SNAPSHOT_ROOT", "vertex-pro-snapshots"), "sync snapshot directory")
	syncInterval := flags.Duration("sync-interval", 10*time.Second, "read-only qui sync interval")
	profileVerificationBindingPath := flags.String("profile-verification-binding", strings.TrimSpace(os.Getenv("VERTEX_PRO_PROFILE_VERIFICATION_BINDING_FILE")), "path to the exact persisted G1 verification binding JSON")
	enableDispatch := flags.Bool("enable-dispatch", envBool("VERTEX_PRO_ENABLE_DISPATCH"), "enable qB Add dispatch; fails closed unless the probed Profile is VERIFIED")
	enableRSS := flags.Bool("enable-rss", envBool("VERTEX_PRO_ENABLE_RSS"), "enable automatic RSS polling; first poll establishes a baseline")
	artifactHosts := flags.String("artifact-hosts", strings.TrimSpace(os.Getenv("VERTEX_PRO_ARTIFACT_HOSTS")), "comma-separated allowed torrent download hosts")
	workerInterval := flags.Duration("worker-interval", 10*time.Second, "automatic worker wake interval; feeds retain their own polling cadence")
	storageConfigPath := flags.String("storage-config", strings.TrimSpace(os.Getenv("VERTEX_PRO_STORAGE_CONFIG_FILE")), "storage target JSON; capacity is always read live")
	instancesConfigPath := flags.String("instances-config", strings.TrimSpace(os.Getenv("VERTEX_PRO_INSTANCES_CONFIG_FILE")), "multi-instance runtime JSON with secret file references")
	if err := flags.Parse(arguments); err != nil {
		return err
	}

	config := app.Config{
		Version:          version,
		Database:         db.Config{Path: *databasePath},
		Hosts:            splitList(*allowedHosts),
		Origins:          splitList(*allowedOrigins),
		Retention:        &app.RetentionConfig{Interval: time.Hour, SnapshotRoot: *snapshotRoot, Policy: retention.DefaultPolicy()},
		HardenedOutbound: true,
	}
	if *workerInterval <= 0 {
		return errors.New("worker interval must be positive")
	}
	if *enableRSS || *enableDispatch {
		if len(splitList(*artifactHosts)) == 0 {
			return errors.New("automatic workers require -artifact-hosts")
		}
		config.Scheduler = &app.SchedulerConfig{Enabled: true, PollInterval: *workerInterval, AllowedArtifactHosts: splitList(*artifactHosts)}
	}
	if strings.TrimSpace(*instancesConfigPath) != "" {
		if strings.TrimSpace(*quiURL) != "" {
			return errors.New("use either -instances-config or -qui-url")
		}
		instances, err := readRuntimeInstances(*instancesConfigPath, *snapshotRoot, *syncInterval, *workerInterval, *enableDispatch, splitList(*artifactHosts))
		if err != nil {
			return err
		}
		config.QuiInstances = instances
	}
	if *enableDispatch && strings.TrimSpace(*quiURL) == "" && len(config.QuiInstances) == 0 {
		return errors.New("dispatch requires qui configuration")
	}
	if strings.TrimSpace(*quiURL) != "" {
		build := buildidentity.Current()
		var verificationBinding *profileverify.Binding
		if strings.TrimSpace(*profileVerificationBindingPath) != "" {
			binding, err := readProfileVerificationBinding(*profileVerificationBindingPath)
			if err != nil {
				return err
			}
			verificationBinding = &binding
		}
		config.Qui = &app.QuiConfig{
			BaseURL:                    *quiURL,
			InstanceID:                 *quiInstanceID,
			APIKey:                     secrets.SecretRef{Provider: "env", Key: "VERTEX_PRO_QUI_API_KEY"},
			ProxyKey:                   secrets.SecretRef{Provider: "env", Key: "VERTEX_PRO_QUI_PROXY_KEY"},
			Store:                      secrets.EnvStore{},
			SnapshotRoot:               *snapshotRoot,
			SyncInterval:               *syncInterval,
			HTTPClient:                 &http.Client{Timeout: 30 * time.Second},
			ProfileVerificationBinding: verificationBinding,
			VertexCommit:               build.Revision,
			VertexWorktreeClean:        build.Available && build.Clean,
			EnableDispatch:             *enableDispatch,
		}
		if *enableDispatch {
			storage, err := readRuntimeStorage(*storageConfigPath, *quiInstanceID)
			if err != nil {
				return err
			}
			config.Qui.DispatchStage = &app.DispatchStageConfig{Enabled: true, Storage: storage, Interval: *workerInterval, AllowedArtifactHosts: splitList(*artifactHosts)}
		}
	}
	runtime, err := app.New(context.Background(), config)
	if err != nil {
		return fmt.Errorf("initialize application: %w", err)
	}
	defer runtime.Close()

	mux := http.NewServeMux()
	mux.Handle("/", requestLogger(logger, runtime.Handler))
	mux.Handle("/healthz", requestLogger(logger, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusOK)
		_, _ = response.Write([]byte("ok\n"))
	})))
	mux.Handle("/readyz", requestLogger(logger, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if err := runtime.Ready(request.Context()); err != nil {
			logger.ErrorContext(request.Context(), "readiness check failed", "error", err)
			response.WriteHeader(http.StatusServiceUnavailable)
			_, _ = response.Write([]byte("not ready\n"))
			return
		}
		response.WriteHeader(http.StatusOK)
		_, _ = response.Write([]byte("ready\n"))
	})))
	mux.Handle("/metrics", requestLogger(logger, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		// Operational counters only (bounded label sets, no secrets), exposed
		// like healthz/readyz for local scraper access.
		response.Header().Set("Content-Type", "text/plain; charset=utf-8")
		response.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(response, runtime.Metrics.Render())
	})))

	server := &http.Server{Addr: *address, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	signalContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serverErrors := make(chan error, 1)
	go func() {
		logger.Info("server listening", "address", *address, "version", version)
		serverErrors <- server.ListenAndServe()
	}()

	select {
	case err := <-serverErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-signalContext.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdownContext)
	}
}

func readProfileVerificationBinding(path string) (profileverify.Binding, error) {
	file, err := os.Open(strings.TrimSpace(path))
	if err != nil {
		return profileverify.Binding{}, fmt.Errorf("open profile verification binding: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var binding profileverify.Binding
	if err := decoder.Decode(&binding); err != nil {
		return profileverify.Binding{}, fmt.Errorf("decode profile verification binding: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return profileverify.Binding{}, errors.New("profile verification binding contains trailing JSON")
	}
	return binding, nil
}

func envOrDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func envBool(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func splitList(value string) []string {
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if item := strings.TrimSpace(part); item != "" {
			result = append(result, item)
		}
	}
	return result
}

func requestLogger(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requestID := request.Header.Get("X-Request-ID")
		if requestID == "" {
			requestID = observability.NewID()
		}
		ctx := observability.WithRequestID(request.Context(), requestID)
		logger.InfoContext(ctx, "http request", observability.IDs(ctx)...)
		next.ServeHTTP(response, request.WithContext(ctx))
	})
}

// opsDatabase 打开运维命令的数据库：文件不存在时直接报错，不静默创建。
func opsDatabase(path string) (*sql.DB, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		path = envOrDefault("VERTEX_PRO_DATABASE", "vertex-pro.db")
	}
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("database file %s does not exist", path)
	}
	return db.Open(context.Background(), db.Config{Path: path})
}

func statusOf(checks []diagnostics.Check) string {
	if diagnostics.AllOK(checks) {
		return "PASS"
	}
	return "FAIL"
}

// runVerify 执行 HARD-10 的离线安装校验：迁移版本、启动一致性与外键
// 完整性。全部通过时退出码为 0，任一失败为 1。
func runVerify(arguments []string) error {
	flags := flag.NewFlagSet("verify", flag.ContinueOnError)
	databasePath := flags.String("database", envOrDefault("VERTEX_PRO_DATABASE", "vertex-pro.db"), "SQLite database path")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	database, err := opsDatabase(*databasePath)
	if err != nil {
		return err
	}
	defer database.Close()
	runner, err := db.DefaultRunner()
	if err != nil {
		return err
	}
	checks := diagnostics.VerifyChecks(context.Background(), database, runner, len(runner.Migrations))
	payload, err := json.MarshalIndent(map[string]any{"command": "verify", "status": statusOf(checks), "checks": checks}, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(payload))
	if !diagnostics.AllOK(checks) {
		return fmt.Errorf("verification failed")
	}
	return nil
}

// runReadiness 输出就绪原因清单：与 verify 共用检查集，面向监控探针
// 的可读诊断输出。
func runReadiness(arguments []string) error {
	flags := flag.NewFlagSet("readiness", flag.ContinueOnError)
	databasePath := flags.String("database", envOrDefault("VERTEX_PRO_DATABASE", "vertex-pro.db"), "SQLite database path")
	minFree := flags.Uint64("min-free-bytes", 1<<30, "disk watermark in bytes; readiness reports a reason when free space drops below it")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	database, err := opsDatabase(*databasePath)
	if err != nil {
		return err
	}
	defer database.Close()
	runner, err := db.DefaultRunner()
	if err != nil {
		return err
	}
	checks := diagnostics.VerifyChecks(context.Background(), database, runner, len(runner.Migrations))
	checks = append(checks, diagnostics.DiskFreeCheck(*databasePath, *minFree))
	payload, err := json.MarshalIndent(map[string]any{"command": "readiness", "ready": diagnostics.AllOK(checks), "reasons": checks}, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(payload))
	return nil
}

// runSupportBundle 生成 OBS-04/HARD-10 的支持包：诊断报告加数据库行数
// 摘要，写入 0600 权限的文件并输出其摘要。
func runSupportBundle(arguments []string) error {
	flags := flag.NewFlagSet("support-bundle", flag.ContinueOnError)
	databasePath := flags.String("database", envOrDefault("VERTEX_PRO_DATABASE", "vertex-pro.db"), "SQLite database path")
	outPath := flags.String("out", "vertex-pro-support-bundle.json", "support bundle output path")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	database, err := opsDatabase(*databasePath)
	if err != nil {
		return err
	}
	defer database.Close()
	report, err := diagnostics.Build(version, buildidentity.Current(), map[string]any{
		"database_path": *databasePath,
	}, database, time.Now())
	if err != nil {
		return err
	}
	diskCheck := diagnostics.DiskFreeCheck(*databasePath, 0)
	bundle, err := diagnostics.BuildSupportBundle(context.Background(), report, database, nil, []diagnostics.Check{diskCheck})

	if err != nil {
		return err
	}
	payload, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(strings.TrimSpace(*outPath), payload, 0o600); err != nil {
		return fmt.Errorf("write support bundle: %w", err)
	}
	fmt.Printf("support bundle written to %s (%s)\n", strings.TrimSpace(*outPath), bundle.Digest)
	return nil
}

// HARD-08：备份、恢复与定期 restore drill。密钥经环境变量传入
// （默认 VERTEX_PRO_BACKUP_KEY，64 位十六进制字符 = 32 字节），
// 备份文件以 AES-256-GCM 加密，认证失败不解密。
func backupKey(envName string) ([]byte, error) {
	if strings.TrimSpace(envName) == "" {
		envName = "VERTEX_PRO_BACKUP_KEY"
	}
	value := strings.TrimSpace(os.Getenv(envName))
	if value == "" {
		return nil, fmt.Errorf("backup key environment variable %s is empty", envName)
	}
	return db.LoadKey(value)
}

func runBackup(arguments []string) error {
	flags := flag.NewFlagSet("backup", flag.ContinueOnError)
	databasePath := flags.String("database", envOrDefault("VERTEX_PRO_DATABASE", "vertex-pro.db"), "SQLite database path")
	outPath := flags.String("out", "", "backup output path (required)")
	keyEnv := flags.String("key-env", "VERTEX_PRO_BACKUP_KEY", "environment variable holding the 64-hex backup key; encryption is skipped when the variable is empty")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if strings.TrimSpace(*outPath) == "" {
		return errors.New("-out is required")
	}
	plaintext := *outPath + ".plain.tmp"
	defer os.Remove(plaintext)
	if err := db.BackupDatabase(context.Background(), db.Config{Path: *databasePath}, plaintext); err != nil {
		return err
	}
	if key := strings.TrimSpace(os.Getenv(*keyEnv)); key != "" {
		keyBytes, err := db.LoadKey(key)
		if err != nil {
			return err
		}
		if err := db.EncryptFile(plaintext, *outPath, keyBytes); err != nil {
			return err
		}
		fmt.Printf("encrypted backup written to %s\n", *outPath)
		return nil
	}
	if err := os.Rename(plaintext, *outPath); err != nil {
		return err
	}
	fmt.Printf("plaintext backup written to %s\n", *outPath)
	return nil
}

func runRestore(arguments []string) error {
	flags := flag.NewFlagSet("restore", flag.ContinueOnError)
	databasePath := flags.String("database", envOrDefault("VERTEX_PRO_DATABASE", "vertex-pro.db"), "SQLite database path to replace")
	backupPath := flags.String("backup", "", "backup file path (required)")
	keyEnv := flags.String("key-env", "VERTEX_PRO_BACKUP_KEY", "environment variable holding the 64-hex backup key; decryption is skipped when the variable is empty")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if strings.TrimSpace(*backupPath) == "" {
		return errors.New("-backup is required")
	}
	if key := strings.TrimSpace(os.Getenv(*keyEnv)); key != "" {
		keyBytes, err := db.LoadKey(key)
		if err != nil {
			return err
		}
		decrypted := *backupPath + ".plain.tmp"
		defer os.Remove(decrypted)
		if err := db.DecryptFile(*backupPath, decrypted, keyBytes); err != nil {
			return err
		}
		if err := db.Restore(db.Config{Path: *databasePath}, decrypted); err != nil {
			return err
		}
		fmt.Printf("decrypted backup restored to %s\n", *databasePath)
		return nil
	}
	if err := db.Restore(db.Config{Path: *databasePath}, *backupPath); err != nil {
		return err
	}
	fmt.Printf("backup restored to %s\n", *databasePath)
	return nil
}

// runRestoreDrill 是 HARD-08 的定期恢复演练：备份 → （可选解密）→ 还原
// 到临时目录 → 打开并跑启动一致性检查 → 报告与清理。全程不触碰生产库。
func runRestoreDrill(arguments []string) error {
	flags := flag.NewFlagSet("restore-drill", flag.ContinueOnError)
	databasePath := flags.String("database", envOrDefault("VERTEX_PRO_DATABASE", "vertex-pro.db"), "SQLite database path to drill against")
	keyEnv := flags.String("key-env", "VERTEX_PRO_BACKUP_KEY", "environment variable holding the 64-hex backup key; encryption is skipped when the variable is empty")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	drill := filepath.Join(os.TempDir(), "vertex-pro-restore-drill")
	_ = os.RemoveAll(drill)
	if err := os.MkdirAll(drill, 0o750); err != nil {
		return err
	}
	defer os.RemoveAll(drill)

	backupPath := filepath.Join(drill, "drill.db.bak")
	if err := db.BackupDatabase(context.Background(), db.Config{Path: *databasePath}, backupPath); err != nil {
		return fmt.Errorf("drill backup: %w", err)
	}
	restoredPath := filepath.Join(drill, "drill.db")
	if key := strings.TrimSpace(os.Getenv(*keyEnv)); key != "" {
		keyBytes, err := db.LoadKey(key)
		if err != nil {
			return err
		}
		encryptedPath := filepath.Join(drill, "drill.db.enc")
		if err := db.EncryptFile(backupPath, encryptedPath, keyBytes); err != nil {
			return fmt.Errorf("drill encryption: %w", err)
		}
		if err := db.DecryptFile(encryptedPath, restoredPath, keyBytes); err != nil {
			return fmt.Errorf("drill decryption: %w", err)
		}
	} else if err := copyDatabaseFileForDrill(backupPath, restoredPath); err != nil {
		return err
	}
	database, err := db.Open(context.Background(), db.Config{Path: restoredPath})
	if err != nil {
		return fmt.Errorf("drill open: %w", err)
	}
	defer database.Close()
	if err := db.CheckStartupConsistency(context.Background(), database); err != nil {
		return fmt.Errorf("drill consistency: %w", err)
	}
	var tables int
	if err := database.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table'`).Scan(&tables); err != nil {
		return fmt.Errorf("drill table count: %w", err)
	}
	fmt.Printf("restore drill PASS: %d tables verified from a %s backup\n", tables, map[bool]string{true: "encrypted", false: "plaintext"}[strings.TrimSpace(os.Getenv(*keyEnv)) != ""])
	return nil
}

// copyDatabaseFileForDrill 在未加密场景下把备份复制为演练库。
func copyDatabaseFileForDrill(sourcePath, destinationPath string) error {
	source, err := os.Open(sourcePath)
	if err != nil {
		return fmt.Errorf("open drill backup: %w", err)
	}
	defer source.Close()
	destination, err := os.Create(destinationPath)
	if err != nil {
		return fmt.Errorf("create drill database: %w", err)
	}
	defer destination.Close()
	if _, err := io.Copy(destination, source); err != nil {
		return fmt.Errorf("copy drill database: %w", err)
	}
	return nil
}
