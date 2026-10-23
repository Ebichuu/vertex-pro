package resourcesapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/vertex-pro/vertex-pro/internal/httpapi"
	"github.com/vertex-pro/vertex-pro/internal/ruleservice"
)

type createRuleRequest struct {
	ID         string          `json:"id"`
	SiteID     string          `json:"site_id"`
	Name       string          `json:"name"`
	Enabled    *bool           `json:"enabled"`
	Priority   int             `json:"priority"`
	Definition json.RawMessage `json:"definition"`
}

type updateRuleRequest struct {
	Name       *string         `json:"name"`
	Enabled    *bool           `json:"enabled"`
	Priority   *int            `json:"priority"`
	Definition json.RawMessage `json:"definition"`
}

func (d Deps) getRule(response http.ResponseWriter, request *http.Request) {
	service, err := d.ruleService()
	if err != nil {
		httpapi.WriteError(response, request, "INTERNAL_ERROR", "rule service is not configured")
		return
	}
	rule, err := service.Get(request.Context(), request.PathValue("id"))
	if err != nil {
		writeRuleError(response, request, err)
		return
	}
	response.Header().Set("ETag", ruleETag(rule.Revision))
	httpapi.WriteJSON(response, http.StatusOK, rulePayload(rule))
}

func (d Deps) createRule(response http.ResponseWriter, request *http.Request) {
	var input createRuleRequest
	if err := httpapi.DecodeJSON(response, request, &input, 128<<10); err != nil {
		writeRuleRequestError(response, request, err)
		return
	}
	service, err := d.ruleService()
	if err != nil {
		httpapi.WriteError(response, request, "INTERNAL_ERROR", "rule service is not configured")
		return
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	rule, err := service.Create(request.Context(), ruleservice.CreateInput{
		ID: input.ID, SiteID: input.SiteID, Name: input.Name, Enabled: enabled,
		Priority: input.Priority, Definition: input.Definition,
	})
	if err != nil {
		writeRuleError(response, request, err)
		return
	}
	response.Header().Set("Location", "/api/v1/rules/"+rule.ID)
	response.Header().Set("ETag", ruleETag(rule.Revision))
	httpapi.WriteJSON(response, http.StatusCreated, rulePayload(rule))
}

func (d Deps) updateRule(response http.ResponseWriter, request *http.Request) {
	ruleID := request.PathValue("id")
	expectedRevision, err := parseRuleRevision(request.Header.Get("If-Match"))
	if err != nil {
		httpapi.WriteError(response, request, "INVALID_REQUEST", "If-Match must contain a positive rule revision")
		return
	}
	var input updateRuleRequest
	if err := httpapi.DecodeJSON(response, request, &input, 128<<10); err != nil {
		writeRuleRequestError(response, request, err)
		return
	}
	service, err := d.ruleService()
	if err != nil {
		httpapi.WriteError(response, request, "INTERNAL_ERROR", "rule service is not configured")
		return
	}
	rule, err := service.Update(request.Context(), ruleID, expectedRevision, ruleservice.UpdateInput{
		Name: input.Name, Enabled: input.Enabled, Priority: input.Priority, Definition: input.Definition,
	})
	if err != nil {
		writeRuleError(response, request, err)
		return
	}
	if input.Enabled != nil && !*input.Enabled && !d.confirmPolicyDisable(response, request) {
		return
	}
	response.Header().Set("ETag", ruleETag(rule.Revision))
	httpapi.WriteJSON(response, http.StatusOK, rulePayload(rule))
}

func parseRuleRevision(value string) (int, error) {
	value = strings.TrimSpace(value)
	if len(value) < 2 || value[0] != '"' || value[len(value)-1] != '"' {
		return 0, errors.New("revision is not an ETag")
	}
	inner := value[1 : len(value)-1]
	if inner == "" || strings.Contains(inner, `"`) {
		return 0, errors.New("revision is invalid")
	}
	revision, err := strconv.Atoi(inner)
	if err != nil || revision <= 0 {
		return 0, errors.New("revision is invalid")
	}
	return revision, nil
}

func ruleETag(revision int) string { return fmt.Sprintf("%q", strconv.Itoa(revision)) }

func writeRuleRequestError(response http.ResponseWriter, request *http.Request, err error) {
	var typed *httpapi.TypedError
	if errors.As(err, &typed) {
		httpapi.WriteError(response, request, typed.Code, typed.Message)
		return
	}
	httpapi.WriteError(response, request, "INVALID_REQUEST", "invalid rule request")
}

func writeRuleError(response http.ResponseWriter, request *http.Request, err error) {
	switch {
	case errors.Is(err, ruleservice.ErrSiteNotFound):
		httpapi.WriteError(response, request, "NOT_FOUND", "rule site not found")
	case errors.Is(err, ruleservice.ErrNotFound):
		httpapi.WriteError(response, request, "NOT_FOUND", "rule not found")
	case errors.Is(err, ruleservice.ErrConflict):
		httpapi.WriteError(response, request, "REVISION_CONFLICT", "rule revision is stale")
	case strings.HasPrefix(err.Error(), "invalid "):
		httpapi.WriteError(response, request, "VALIDATION_FAILED", err.Error())
	case strings.Contains(strings.ToUpper(err.Error()), "UNIQUE"):
		httpapi.WriteError(response, request, "REVISION_CONFLICT", "rule already exists")
	case strings.Contains(strings.ToUpper(err.Error()), "FOREIGN KEY"):
		httpapi.WriteError(response, request, "NOT_FOUND", "rule site not found")
	default:
		httpapi.WriteError(response, request, "INTERNAL_ERROR", "rule operation failed")
	}
}
