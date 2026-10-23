package resourcesapi

import (
	"errors"
	"net/http"

	"github.com/vertex-pro/vertex-pro/internal/httpapi"
	"github.com/vertex-pro/vertex-pro/internal/settings"
)

// Unlike historical resource test seams, settings fail closed without auth or CSRF wiring.
func settingsGuard(deps Deps, handler http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if deps.Auth == nil {
			httpapi.WriteError(response, request, "UNAUTHENTICATED", "administrator session required")
			return
		}
		requireSession(deps, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			if deps.Settings == nil || deps.Web == nil {
				httpapi.WriteError(response, request, "INTERNAL_ERROR", "settings service is not configured")
				return
			}
			handler(response, request)
		})).ServeHTTP(response, request)
	})
}

func (d Deps) getSettings(response http.ResponseWriter, request *http.Request) {
	saved, err := d.Settings.Load(request.Context())
	if err != nil {
		writeSettingsError(response, request, err)
		return
	}
	response.Header().Set("ETag", ruleETag(saved.Revision))
	d.writeData(response, request, d.settingsPayload(saved))
}

func (d Deps) updateSettings(response http.ResponseWriter, request *http.Request) {
	var input settings.Values
	if err := httpapi.DecodeStrictJSON(response, request, &input, 4<<10); err != nil {
		httpapi.WriteError(response, request, "INVALID_REQUEST", "invalid settings request")
		return
	}
	if err := settings.Validate(input); err != nil {
		writeSettingsError(response, request, err)
		return
	}
	revision, err := parseRuleRevision(request.Header.Get("If-Match"))
	if err != nil {
		httpapi.WriteError(response, request, "INVALID_REQUEST", "If-Match must contain one quoted positive revision")
		return
	}
	saved, err := d.Settings.Update(request.Context(), revision, input)
	if err != nil {
		writeSettingsError(response, request, err)
		return
	}
	response.Header().Set("ETag", ruleETag(saved.Revision))
	d.writeData(response, request, d.settingsPayload(saved))
}

func (d Deps) downloadDiagnostics(response http.ResponseWriter, request *http.Request) {
	report, err := d.Settings.Diagnostics(request.Context(), d.ActiveSettings, d.Version)
	if err != nil {
		httpapi.WriteError(response, request, "INTERNAL_ERROR", "diagnostics unavailable")
		return
	}
	response.Header().Set("Content-Disposition", `attachment; filename="vertex-pro-diagnostics.json"`)
	httpapi.WriteJSON(response, http.StatusOK, report)
}

func valuesPayload(value settings.Values) map[string]any {
	return map[string]any{
		"timezone":                    value.Timezone,
		"rss_poll_interval_minutes":   value.RSSPollIntervalMinutes,
		"rate_limit_per_minute":       value.RateLimitPerMinute,
		"max_concurrent_rss_fetches":  value.MaxConcurrentRSSFetches,
		"session_ttl_hours":           value.SessionTTLHours,
		"stats_raw_retention_days":    value.StatsRawRetentionDays,
		"stats_hourly_retention_days": value.StatsHourlyRetentionDays,
	}
}

func (d Deps) settingsPayload(saved settings.Settings) map[string]any {
	return map[string]any{
		"values": valuesPayload(saved.Values), "active": valuesPayload(d.ActiveSettings),
		"revision": saved.Revision, "updated_at": saved.UpdatedAt,
		"restart_required": saved.Values != d.ActiveSettings,
		"security":         map[string]any{"csrf_protection": true, "secure_cookies": true, "diagnostics_redacted": true},
	}
}

func writeSettingsError(response http.ResponseWriter, request *http.Request, err error) {
	switch {
	case errors.Is(err, settings.ErrConflict):
		httpapi.WriteError(response, request, "REVISION_CONFLICT", "settings revision changed; reload before saving")
	case errors.Is(err, settings.ErrInvalid):
		httpapi.WriteError(response, request, "VALIDATION_FAILED", "settings values are outside the allowed range")
	default:
		httpapi.WriteError(response, request, "INTERNAL_ERROR", "settings operation failed")
	}
}
