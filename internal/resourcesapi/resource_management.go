package resourcesapi

import (
	"errors"
	"github.com/vertex-pro/vertex-pro/internal/httpapi"
	"github.com/vertex-pro/vertex-pro/internal/resourceconfig"
	"github.com/vertex-pro/vertex-pro/internal/ruleservice"
	"github.com/vertex-pro/vertex-pro/internal/sitecredentials"
	"net/http"
	"strconv"
	"strings"
)

const policyDisablePendingMessage = "本地配置已停用，但 qui 删除策略尚未确认停用；请恢复连接后重试"

func (d Deps) configService() resourceconfig.Service {
	return resourceconfig.Service{DB: d.Database, Now: d.now}
}
func (d Deps) configError(w http.ResponseWriter, r *http.Request, err error) {
	code, message := "INTERNAL_ERROR", "configuration update failed"
	if errors.Is(err, resourceconfig.ErrConflict) {
		code, message = "REVISION_CONFLICT", "configuration changed; reload before editing"
	} else if errors.Is(err, resourceconfig.ErrNotFound) {
		code, message = "NOT_FOUND", "configuration not found"
	} else if errors.Is(err, resourceconfig.ErrInvalid) || strings.HasPrefix(err.Error(), "invalid ") {
		code, message = "VALIDATION_FAILED", "invalid configuration; check fields and instance members"
	}
	httpapi.WriteError(w, r, httpapi.ErrorCode(code), message)
}
func (d Deps) updateSite(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Enabled       *bool   `json:"enabled"`
		CredentialRef *string `json:"credential_ref"`
	}
	if err := httpapi.DecodeStrictJSON(w, r, &input, 4096); err != nil {
		httpapi.WriteError(w, r, "INVALID_REQUEST", "invalid site update")
		return
	}
	expected, err := resourceconfig.ParseExpected(r.Header.Get("If-Match"))
	if err != nil {
		d.configError(w, r, err)
		return
	}
	if input.CredentialRef != nil {
		if err := sitecredentials.Validate(*input.CredentialRef); err != nil {
			d.configError(w, r, err)
			return
		}
	}
	stamp, err := d.configService().UpdateSite(r.Context(), r.PathValue("id"), expected, input.Enabled, input.CredentialRef)
	if err != nil {
		d.configError(w, r, err)
		return
	}
	if input.Enabled != nil && !*input.Enabled && !d.confirmPolicyDisable(w, r) {
		return
	}
	d.writeData(w, r, map[string]any{"id": r.PathValue("id"), "updated_at": stamp})
}
func (d Deps) archiveResource(w http.ResponseWriter, r *http.Request) {
	expected, err := resourceconfig.ParseExpected(r.Header.Get("If-Match"))
	if err != nil {
		d.configError(w, r, err)
		return
	}
	parts := strings.Split(r.URL.Path, "/")
	kind, id := parts[len(parts)-2], r.PathValue("id")
	switch kind {
	case "sites":
		disabled := false
		expected, err = d.configService().UpdateSite(r.Context(), id, expected, &disabled, nil)
		if err != nil {
			d.configError(w, r, err)
			return
		}
	case "rules":
		revision, parseErr := strconv.Atoi(expected)
		if parseErr != nil || revision <= 0 {
			d.configError(w, r, resourceconfig.ErrInvalid)
			return
		}
		service, serviceErr := d.ruleService()
		if serviceErr != nil {
			httpapi.WriteError(w, r, "INTERNAL_ERROR", "rule service is not configured")
			return
		}
		disabled := false
		rule, updateErr := service.Update(r.Context(), id, revision, ruleservice.UpdateInput{Enabled: &disabled})
		if updateErr != nil {
			writeRuleError(w, r, updateErr)
			return
		}
		expected = strconv.Itoa(rule.Revision)
	}
	if (kind == "sites" || kind == "rules") && !d.confirmPolicyDisable(w, r) {
		return
	}
	if err := d.configService().Archive(r.Context(), kind, id, expected); err != nil {
		d.configError(w, r, err)
		return
	}
	d.writeData(w, r, map[string]any{"id": id, "archived": true})
}

func (d Deps) confirmPolicyDisable(w http.ResponseWriter, r *http.Request) bool {
	if d.SyncPolicies != nil {
		if err := d.SyncPolicies(r.Context()); err != nil {
			httpapi.WriteError(w, r, "QUI_UNREACHABLE", policyDisablePendingMessage)
			return false
		}
		return true
	}
	var pending int
	err := d.Database.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM qui_policy_bindings b JOIN rules rule ON rule.rule_id=b.rule_id LEFT JOIN sites site ON site.site_id=rule.site_id WHERE b.enabled=1 AND (rule.enabled=0 OR rule.archived=1 OR site.enabled=0 OR site.archived=1)`).Scan(&pending)
	if err != nil {
		httpapi.WriteError(w, r, "INTERNAL_ERROR", "check managed qui policies failed")
		return false
	}
	if pending > 0 {
		httpapi.WriteError(w, r, "QUI_UNREACHABLE", policyDisablePendingMessage)
		return false
	}
	return true
}
func (d Deps) listGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := d.configService().Groups(r.Context())
	if err != nil {
		d.configError(w, r, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"items": groups})
}
func (d Deps) saveGroup(w http.ResponseWriter, r *http.Request) {
	var input resourceconfig.Group
	if err := httpapi.DecodeStrictJSON(w, r, &input, 16<<10); err != nil {
		httpapi.WriteError(w, r, "INVALID_REQUEST", "invalid group")
		return
	}
	saved, err := d.configService().SaveGroup(r.Context(), input, input.Revision)
	if err != nil {
		d.configError(w, r, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, saved)
}
