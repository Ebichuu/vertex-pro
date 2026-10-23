package resourcesapi

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/vertex-pro/vertex-pro/internal/httpapi"
	"github.com/vertex-pro/vertex-pro/internal/rssservice"
)

type createSiteRequest struct {
	CredentialRef string `json:"credential_ref"`
	ID            string `json:"id"`
	Name          string `json:"name"`
	BaseURL       string `json:"base_url"`
	Enabled       *bool  `json:"enabled"`
}

func (d Deps) createSite(response http.ResponseWriter, request *http.Request) {
	var input createSiteRequest
	if err := httpapi.DecodeJSON(response, request, &input, 64<<10); err != nil {
		var typed *httpapi.TypedError
		if errors.As(err, &typed) {
			httpapi.WriteError(response, request, typed.Code, typed.Message)
			return
		}
		httpapi.WriteError(response, request, "INVALID_REQUEST", "invalid site request")
		return
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	service, err := d.rssService()
	if err != nil {
		httpapi.WriteError(response, request, "INTERNAL_ERROR", "site service is not configured")
		return
	}
	site, err := service.CreateSite(request.Context(), rssservice.CreateSiteInput{
		ID: input.ID, Name: input.Name, BaseURL: input.BaseURL, Enabled: enabled, CredentialRef: input.CredentialRef,
	})
	if err != nil {
		writeSiteCreateError(response, request, err)
		return
	}
	location := "/api/v1/sites/" + url.PathEscape(site.ID)
	response.Header().Set("Location", location)
	response.Header().Set("ETag", fmt.Sprintf("%q", site.UpdatedAt))
	httpapi.WriteJSON(response, http.StatusCreated, map[string]any{
		"credential_ref": site.CredentialRef, "updated_at": site.UpdatedAt, "id": site.ID, "name": site.Name, "base_url": site.BaseURL, "enabled": site.Enabled,
	})
}

func writeSiteCreateError(response http.ResponseWriter, request *http.Request, err error) {
	message := err.Error()
	switch {
	case strings.HasPrefix(message, "invalid "):
		httpapi.WriteError(response, request, "VALIDATION_FAILED", message)
	case strings.Contains(strings.ToUpper(message), "UNIQUE"):
		httpapi.WriteError(response, request, "REVISION_CONFLICT", "site already exists")
	default:
		httpapi.WriteError(response, request, "INTERNAL_ERROR", "create site failed")
	}
}
