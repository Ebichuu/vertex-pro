// Package diagnostics builds the OBS-04 diagnostics bundle: a redacted,
// digest-stamped JSON report of version, configuration summary, and live
// status checks. Every free-text value passes through the observability
// redaction so announce URLs, passkeys, and credentials never enter the
// bundle; Secret-bearing values must not be supplied in the first place and
// are additionally redacted by key.
package diagnostics

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/buildidentity"
	"github.com/vertex-pro/vertex-pro/internal/db"
	"github.com/vertex-pro/vertex-pro/internal/observability"
)

// Check is one named status probe result.
type Check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// Report is the full diagnostics bundle. Digest covers every other field so a
// recipient can verify the bundle was not edited after generation.
type Report struct {
	GeneratedAt string                 `json:"generated_at"`
	Version     string                 `json:"version"`
	Build       buildidentity.Identity `json:"build"`
	Config      map[string]any         `json:"config"`
	Checks      []Check                `json:"checks"`
	Digest      string                 `json:"digest"`
}

// Build assembles the bundle: config values are redacted by key and value,
// database probes run read-only, and the digest is computed over the
// canonical JSON of the report without the digest field.
func Build(version string, build buildidentity.Identity, config map[string]any, database *sql.DB, now time.Time) (Report, error) {
	redactedConfig := make(map[string]any, len(config))
	for key, value := range config {
		if observability.SensitiveKey(key) {
			redactedConfig[key] = observability.Redacted
			continue
		}
		if text, ok := value.(string); ok {
			redactedConfig[key] = observability.RedactString(text)
			continue
		}
		redactedConfig[key] = value
	}

	checks := make([]Check, 0, 3)
	databaseCheck := Check{Name: "database"}
	if database == nil {
		databaseCheck.Detail = "not configured"
	} else if err := database.PingContext(context.Background()); err != nil {
		databaseCheck.Detail = observability.RedactString(err.Error())
	} else {
		databaseCheck.OK = true
		databaseCheck.Detail = "ping ok"
		if err := db.CheckStartupConsistency(context.Background(), database); err != nil {
			databaseCheck.OK = false
			databaseCheck.Detail = observability.RedactString(err.Error())
		}
	}
	checks = append(checks, databaseCheck)
	checks = append(checks, Check{Name: "build_identity", OK: build.Available, Detail: func() string {
		if !build.Available {
			return "vcs identity unavailable"
		}
		if !build.Clean {
			return "worktree dirty"
		}
		return "clean"
	}()})

	report := Report{
		GeneratedAt: now.UTC().Format(time.RFC3339),
		Version:     version,
		Build:       build,
		Config:      redactedConfig,
		Checks:      checks,
	}
	payload, err := json.Marshal(report)
	if err != nil {
		return Report{}, fmt.Errorf("marshal diagnostics report: %w", err)
	}
	digest := sha256.Sum256(payload)
	report.Digest = "sha256:" + hex.EncodeToString(digest[:])
	return report, nil
}
