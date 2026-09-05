package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/app"
	"github.com/vertex-pro/vertex-pro/internal/buildidentity"
	"github.com/vertex-pro/vertex-pro/internal/db"
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
		fmt.Println("vertex-pro doctor: OK")
	case "version":
		fmt.Println(version)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", command)
		fmt.Fprintln(os.Stderr, "usage: vertex-pro [serve|doctor|version]")
		os.Exit(2)
	}
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
	if err := flags.Parse(arguments); err != nil {
		return err
	}

	config := app.Config{
		Database:  db.Config{Path: *databasePath},
		Hosts:     splitList(*allowedHosts),
		Origins:   splitList(*allowedOrigins),
		Retention: &app.RetentionConfig{Interval: time.Hour, SnapshotRoot: *snapshotRoot, Policy: retention.DefaultPolicy()},
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
