package settings

import (
	"context"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/buildidentity"
	"github.com/vertex-pro/vertex-pro/internal/diagnostics"
)

// Diagnostics supplies an explicit allowlist, never environment variables,
// resource URLs, credentials, or arbitrary database rows.
func (service *Service) Diagnostics(ctx context.Context, active Values, version string) (diagnostics.Report, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	saved, err := service.Load(ctx)
	if err != nil {
		return diagnostics.Report{}, err
	}
	if version == "" {
		version = "development"
	}
	return diagnostics.BuildContext(ctx, version, buildidentity.Current(), map[string]any{
		"timezone":                    active.Timezone,
		"rss_poll_interval_minutes":   active.RSSPollIntervalMinutes,
		"rate_limit_per_minute":       active.RateLimitPerMinute,
		"max_concurrent_rss_fetches":  active.MaxConcurrentRSSFetches,
		"session_ttl_hours":           active.SessionTTLHours,
		"stats_raw_retention_days":    active.StatsRawRetentionDays,
		"stats_hourly_retention_days": active.StatsHourlyRetentionDays,
		"settings_revision":           saved.Revision,
		"restart_required":            saved.Values != active,
	}, service.database, service.now())
}
