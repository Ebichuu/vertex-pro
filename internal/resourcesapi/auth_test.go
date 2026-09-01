package resourcesapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/adminauth"
	"github.com/vertex-pro/vertex-pro/internal/httpapi"
)

func TestAuthLifecycleEndpoints(t *testing.T) {
	database := openMigrated(t)
	now := time.Date(2026, 8, 31, 13, 0, 0, 0, time.UTC)
	auth, err := adminauth.New(database)
	if err != nil {
		t.Fatal(err)
	}
	router := Router(Deps{
		Database: database,
		Auth:     auth,
		Web:      &httpapi.WebProtection{AllowedHosts: []string{"vertex.local"}},
		Now:      func() time.Time { return now },
	})

	status := callAuth(t, router, http.MethodGet, "/api/v1/auth/status", "", nil, nil)
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), `"initialized":false`) {
		t.Fatalf("initial status = %d %s", status.Code, status.Body.String())
	}

	weak := callAuth(t, router, http.MethodPost, "/api/v1/auth/initialize", `{"password":"short"}`, nil, nil)
	if weak.Code != http.StatusUnprocessableEntity || !strings.Contains(weak.Body.String(), "VALIDATION_FAILED") {
		t.Fatalf("weak initialize = %d %s", weak.Code, weak.Body.String())
	}

	initialized := callAuth(t, router, http.MethodPost, "/api/v1/auth/initialize", `{"password":"correct-horse-battery"}`, nil, nil)
	if initialized.Code != http.StatusOK {
		t.Fatalf("initialize = %d %s", initialized.Code, initialized.Body.String())
	}
	duplicate := callAuth(t, router, http.MethodPost, "/api/v1/auth/initialize", `{"password":"correct-horse-battery"}`, nil, nil)
	if duplicate.Code != http.StatusConflict || !strings.Contains(duplicate.Body.String(), "JOB_STATE_CONFLICT") {
		t.Fatalf("duplicate initialize = %d %s", duplicate.Code, duplicate.Body.String())
	}

	wrong := callAuth(t, router, http.MethodPost, "/api/v1/auth/login", `{"password":"wrong-password"}`, nil, nil)
	if wrong.Code != http.StatusUnauthorized || !strings.Contains(wrong.Body.String(), "UNAUTHENTICATED") {
		t.Fatalf("wrong login = %d %s", wrong.Code, wrong.Body.String())
	}
	login := callAuth(t, router, http.MethodPost, "/api/v1/auth/login", `{"password":"correct-horse-battery"}`, nil, nil)
	if login.Code != http.StatusOK {
		t.Fatalf("login = %d %s", login.Code, login.Body.String())
	}
	cookies := login.Result().Cookies()
	if len(cookies) != 2 || cookies[0].Name != httpapi.SessionCookieName || cookies[1].Name != httpapi.CSRFCookieName {
		t.Fatalf("login cookies = %#v", cookies)
	}

	status = callAuth(t, router, http.MethodGet, "/api/v1/auth/status", "", cookies, nil)
	var statusPayload struct {
		Authenticated bool `json:"authenticated"`
	}
	if status.Code != http.StatusOK || json.Unmarshal(status.Body.Bytes(), &statusPayload) != nil || !statusPayload.Authenticated {
		t.Fatalf("authenticated status = %d %s", status.Code, status.Body.String())
	}

	logout := callAuth(t, router, http.MethodPost, "/api/v1/auth/logout", "{}", cookies, map[string]string{httpapi.CSRFHeaderName: cookies[1].Value})
	if logout.Code != http.StatusOK || !strings.Contains(logout.Body.String(), `"authenticated":false`) {
		t.Fatalf("logout = %d %s", logout.Code, logout.Body.String())
	}
}

func TestAuthLogoutRequiresCSRF(t *testing.T) {
	database := openMigrated(t)
	auth := newAuth(t, database)
	session := login(t, auth)
	router := Router(Deps{Database: database, Auth: auth, Web: &httpapi.WebProtection{AllowedHosts: []string{"vertex.local"}}})
	response := callAuth(t, router, http.MethodPost, "/api/v1/auth/logout", "{}", []*http.Cookie{{Name: httpapi.SessionCookieName, Value: session}}, nil)
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "FORBIDDEN") {
		t.Fatalf("logout without csrf = %d %s", response.Code, response.Body.String())
	}
}

func callAuth(t *testing.T, router http.Handler, method, path, body string, cookies []*http.Cookie, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequest(method, path, reader)
	request.Host = "vertex.local"
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}
