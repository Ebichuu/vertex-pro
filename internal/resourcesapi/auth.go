package resourcesapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/adminauth"
	"github.com/vertex-pro/vertex-pro/internal/httpapi"
)

type authCredentials struct {
	Password string `json:"password"`
}

func (d Deps) authStatus(response http.ResponseWriter, request *http.Request) {
	if d.Auth == nil {
		httpapi.WriteError(response, request, "INTERNAL_ERROR", "authentication is not configured")
		return
	}
	initialized, err := d.Auth.Initialized(request.Context())
	if err != nil {
		httpapi.WriteError(response, request, "INTERNAL_ERROR", "read authentication status failed")
		return
	}
	authenticated := false
	if initialized {
		if cookie, cookieErr := request.Cookie(httpapi.SessionCookieName); cookieErr == nil {
			authenticated, _ = d.Auth.ValidateSession(request.Context(), cookie.Value, d.now())
		}
	}
	d.writeData(response, request, map[string]any{
		"initialized": initialized, "authenticated": authenticated,
	})
}

func (d Deps) initializeAuth(response http.ResponseWriter, request *http.Request) {
	if d.Auth == nil {
		httpapi.WriteError(response, request, "INTERNAL_ERROR", "authentication is not configured")
		return
	}
	credentials, ok := decodeAuthCredentials(response, request)
	if !ok {
		return
	}
	if err := d.Auth.Initialize(request.Context(), credentials.Password); err != nil {
		switch {
		case errors.Is(err, adminauth.ErrWeakPassword):
			httpapi.WriteError(response, request, "VALIDATION_FAILED", "password does not satisfy the policy")
		case errors.Is(err, adminauth.ErrAlreadyInitialized):
			httpapi.WriteError(response, request, "JOB_STATE_CONFLICT", "administrator is already initialized")
		default:
			httpapi.WriteError(response, request, "INTERNAL_ERROR", "initialize authentication failed")
		}
		return
	}
	d.writeData(response, request, map[string]any{"initialized": true, "authenticated": false})
}

func (d Deps) loginAuth(response http.ResponseWriter, request *http.Request) {
	if d.Auth == nil {
		httpapi.WriteError(response, request, "INTERNAL_ERROR", "authentication is not configured")
		return
	}
	credentials, ok := decodeAuthCredentials(response, request)
	if !ok {
		return
	}
	now := d.now()
	ttl := d.SessionTTL
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	session, err := d.Auth.Login(request.Context(), credentials.Password, now, ttl)
	if err != nil {
		switch {
		case errors.Is(err, adminauth.ErrLocked):
			httpapi.WriteError(response, request, "RATE_LIMITED", "login temporarily locked")
		case errors.Is(err, adminauth.ErrNotInitialized):
			httpapi.WriteError(response, request, "JOB_STATE_CONFLICT", "administrator is not initialized")
		case errors.Is(err, adminauth.ErrInvalidCredentials):
			httpapi.WriteError(response, request, "UNAUTHENTICATED", "invalid credentials")
		default:
			httpapi.WriteError(response, request, "INTERNAL_ERROR", "login failed")
		}
		return
	}
	csrfToken, err := httpapi.NewCSRFToken()
	if err != nil {
		httpapi.WriteError(response, request, "INTERNAL_ERROR", "create csrf token failed")
		return
	}
	for _, cookie := range httpapi.NewSessionCookies(session.Token, csrfToken, int(ttl/time.Second)) {
		http.SetCookie(response, cookie)
	}
	d.writeData(response, request, map[string]any{
		"initialized": true, "authenticated": true, "expires_at": session.ExpiresAt.Format(time.RFC3339),
	})
}

func (d Deps) logoutAuth(response http.ResponseWriter, request *http.Request) {
	if d.Auth == nil {
		httpapi.WriteError(response, request, "INTERNAL_ERROR", "authentication is not configured")
		return
	}
	cookie, err := request.Cookie(httpapi.SessionCookieName)
	if err != nil {
		httpapi.WriteError(response, request, "UNAUTHENTICATED", "session required")
		return
	}
	if err := d.Auth.Logout(request.Context(), cookie.Value); err != nil {
		httpapi.WriteError(response, request, "INTERNAL_ERROR", "logout failed")
		return
	}
	for _, expired := range httpapi.ClearSessionCookies() {
		http.SetCookie(response, expired)
	}
	d.writeData(response, request, map[string]any{"authenticated": false})
}

func decodeAuthCredentials(response http.ResponseWriter, request *http.Request) (authCredentials, bool) {
	var credentials authCredentials
	if err := httpapi.DecodeJSON(response, request, &credentials, 64<<10); err != nil {
		var typed *httpapi.TypedError
		if errors.As(err, &typed) {
			httpapi.WriteError(response, request, typed.Code, typed.Message)
		} else {
			httpapi.WriteError(response, request, "INVALID_REQUEST", "invalid authentication request")
		}
		return authCredentials{}, false
	}
	return credentials, true
}

func (d Deps) now() time.Time {
	if d.Now != nil {
		return d.Now().UTC()
	}
	return time.Now().UTC()
}
