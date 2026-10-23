package httpapi

import (
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func newProtectionTestHandler(protection *WebProtection, handler http.HandlerFunc) http.Handler {
	return protection.Middleware(handler)
}

func TestHostValidationRejectsMaliciousHost(t *testing.T) {
	protection := &WebProtection{AllowedHosts: []string{"vertex.local:8080", "127.0.0.1:8080"}}
	handler := newProtectionTestHandler(protection, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	tests := []struct {
		name  string
		host  string
		valid bool
	}{
		{"allowed hostname", "vertex.local:8080", true},
		{"allowed hostname without port", "vertex.local", true},
		{"allowed ip", "127.0.0.1:8080", true},
		{"evil host", "evil.example.com", false},
		{"spoofed suffix", "vertex.local.evil.com", false},
		{"case spoof", "VERTEX.LOCAL:8080", true},
		{"empty host", "", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/api/v1/jobs", nil)
			request.Host = test.host
			handler.ServeHTTP(response, request)
			if test.valid && response.Code != http.StatusOK {
				t.Fatalf("status = %d", response.Code)
			}
			if !test.valid && response.Code != http.StatusForbidden {
				t.Fatalf("status = %d", response.Code)
			}
		})
	}
}

func TestOriginValidationRejectsCrossSite(t *testing.T) {
	protection := &WebProtection{AllowedHosts: []string{"vertex.local:8080"}, AllowedOrigins: []string{"https://vertex.local:8080"}}
	handler := newProtectionTestHandler(protection, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	tests := []struct {
		name   string
		origin string
		valid  bool
	}{
		{"matching origin", "https://vertex.local:8080", true},
		{"no origin", "", true},
		{"cross site origin", "https://evil.example.com", false},
		{"scheme downgrade", "http://vertex.local:8080", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/api/v1/jobs", nil)
			request.Host = "vertex.local:8080"
			if test.origin != "" {
				request.Header.Set("Origin", test.origin)
			}
			handler.ServeHTTP(response, request)
			if test.valid && response.Code != http.StatusOK {
				t.Fatalf("status = %d", response.Code)
			}
			if !test.valid && response.Code != http.StatusForbidden {
				t.Fatalf("status = %d", response.Code)
			}
		})
	}
}

func TestCSRFDoubleSubmitRequiredOnUnsafeMethods(t *testing.T) {
	protection := &WebProtection{AllowedHosts: []string{"vertex.local"}}
	handler := newProtectionTestHandler(protection, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	valid := "csrf-token-value"
	tests := []struct {
		name       string
		method     string
		path       string
		cookie     string
		header     string
		wantStatus int
	}{
		{"post without token", http.MethodPost, "/api/v1/rules", "", "", http.StatusForbidden},
		{"post with header only", http.MethodPost, "/api/v1/rules", "", valid, http.StatusForbidden},
		{"post with cookie only", http.MethodPost, "/api/v1/rules", valid, "", http.StatusForbidden},
		{"post with mismatched token", http.MethodPost, "/api/v1/rules", valid, "other", http.StatusForbidden},
		{"post with matching token", http.MethodPost, "/api/v1/rules", valid, valid, http.StatusOK},
		{"login endpoint exempt", http.MethodPost, "/api/v1/auth/login", "", "", http.StatusOK},
		{"initialize endpoint exempt", http.MethodPost, "/api/v1/auth/initialize", "", "", http.StatusOK},
		{"get never requires csrf", http.MethodGet, "/api/v1/rules", "", "", http.StatusOK},
		{"delete requires token", http.MethodDelete, "/api/v1/rules/r1", "", "", http.StatusForbidden},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			request := httptest.NewRequest(test.method, test.path, nil)
			request.Host = "vertex.local"
			if test.cookie != "" {
				request.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: test.cookie})
			}
			if test.header != "" {
				request.Header.Set(CSRFHeaderName, test.header)
			}
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d want %d", response.Code, test.wantStatus)
			}
		})
	}
}

func TestSessionCookieAttributesAreHardened(t *testing.T) {
	cookies := NewSessionCookies("session-token", "csrf-token", 3600)
	if len(cookies) != 3 {
		t.Fatalf("cookie count = %d", len(cookies))
	}
	session := cookies[0]
	if session.Name != SessionCookieName || !session.HttpOnly || !session.Secure || session.SameSite != http.SameSiteStrictMode {
		t.Fatalf("session cookie = %+v", session)
	}
	if session.Path != "/api/v1" || session.MaxAge != 3600 {
		t.Fatalf("session cookie = %+v", session)
	}
	csrf := cookies[1]
	if csrf.Name != CSRFCookieName || csrf.HttpOnly {
		t.Fatalf("csrf cookie must be readable by the app: %+v", csrf)
	}
	if !csrf.Secure || csrf.SameSite != http.SameSiteStrictMode {
		t.Fatalf("csrf cookie = %+v", csrf)
	}
	serialized := session.String()
	if !strings.Contains(serialized, "HttpOnly") || !strings.Contains(serialized, "Secure") || !strings.Contains(serialized, "SameSite=Strict") {
		t.Fatalf("serialized cookie = %s", serialized)
	}
}

func TestSessionCookiesAvailableToSPAAndAPI(t *testing.T) {
	for _, test := range []struct {
		name   string
		legacy bool
	}{
		{name: "new session"},
		{name: "legacy cookie migration", legacy: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			jar, err := cookiejar.New(nil)
			if err != nil {
				t.Fatal(err)
			}
			loginURL, _ := url.Parse("https://vertex.local/api/v1/auth/login")
			pageURL, _ := url.Parse("https://vertex.local/settings")
			apiURL, _ := url.Parse("https://vertex.local/api/v1/settings")
			if test.legacy {
				jar.SetCookies(loginURL, []*http.Cookie{{Name: CSRFCookieName, Value: "old-csrf", Path: "/api/v1", Secure: true}})
			}
			response := httptest.NewRecorder()
			for _, cookie := range NewSessionCookies("new-session", "new-csrf", 3600) {
				http.SetCookie(response, cookie)
			}
			jar.SetCookies(loginURL, response.Result().Cookies())
			pageCookies := jar.Cookies(pageURL)
			if len(pageCookies) != 1 || pageCookies[0].Name != CSRFCookieName || pageCookies[0].Value != "new-csrf" {
				t.Fatalf("SPA cannot read the new CSRF token: %+v", pageCookies)
			}
			request := httptest.NewRequest(http.MethodPut, apiURL.String(), nil)
			for _, cookie := range jar.Cookies(apiURL) {
				request.AddCookie(cookie)
			}
			request.Header.Set(CSRFHeaderName, pageCookies[0].Value)
			session, err := request.Cookie(SessionCookieName)
			if err != nil || session.Value != "new-session" {
				t.Fatalf("API session cookie = %v, %v", session, err)
			}
			if len(request.Cookies()) != 2 || !verifyCSRF(request) {
				t.Fatalf("SPA token does not authorize API request: %s", request.Header.Get("Cookie"))
			}
			request.Header.Set(CSRFHeaderName, "old-csrf")
			if verifyCSRF(request) {
				t.Fatal("old CSRF token still authorizes API requests")
			}
			httpURL, _ := url.Parse("http://vertex.local/api/v1/settings")
			if cookies := jar.Cookies(httpURL); len(cookies) != 0 {
				t.Fatalf("secure cookies sent over HTTP: %+v", cookies)
			}
		})
	}
}

func TestClearSessionCookiesRemovesCurrentAndLegacyPaths(t *testing.T) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	logoutURL, _ := url.Parse("https://vertex.local/api/v1/auth/logout")
	jar.SetCookies(logoutURL, NewSessionCookies("session", "csrf", 3600))
	jar.SetCookies(logoutURL, []*http.Cookie{{Name: CSRFCookieName, Value: "legacy", Path: "/api/v1", Secure: true}})
	response := httptest.NewRecorder()
	for _, cookie := range ClearSessionCookies() {
		http.SetCookie(response, cookie)
	}
	jar.SetCookies(logoutURL, response.Result().Cookies())
	for _, path := range []string{"/settings", "/api/v1/settings"} {
		t.Run(path, func(t *testing.T) {
			target, _ := url.Parse("https://vertex.local" + path)
			if cookies := jar.Cookies(target); len(cookies) != 0 {
				t.Fatalf("cookies survived logout: %+v", cookies)
			}
		})
	}
}

func TestSecurityHeadersApplied(t *testing.T) {
	protection := &WebProtection{AllowedHosts: []string{"vertex.local"}}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/jobs", nil)
	request.Host = "vertex.local"
	protection.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(response, request)
	if !strings.Contains(response.Header().Get("Content-Security-Policy"), "default-src 'self'") {
		t.Fatal("CSP missing")
	}
	if response.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("nosniff missing")
	}
	if response.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatal("frame options missing")
	}
	if response.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatal("referrer policy missing")
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("Cache-Control no-store missing")
	}
	if response.Header().Get("Pragma") != "no-cache" {
		t.Fatal("Pragma no-cache missing")
	}
}
