package httpapi

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"strings"
)

const (
	SessionCookieName = "vertex_session"
	CSRFCookieName    = "vertex_csrf"
	CSRFHeaderName    = "X-CSRF-Token"
)

// WebProtection 配置第 20.3 节的 Web 防线。AllowedHosts 与 AllowedOrigins
// 为显式允许清单；未配置即拒绝所有 Host（fail closed）。
type WebProtection struct {
	AllowedHosts   []string
	AllowedOrigins []string
}

var unsafeMethods = map[string]bool{http.MethodPost: true, http.MethodPut: true, http.MethodPatch: true, http.MethodDelete: true}

// 认证前端点不依赖会话 cookie，由 Origin/Host 校验与 SameSite=Strict 防
// 跨站；其余非安全方法一律要求双提交 CSRF 令牌。
var csrfExemptPrefixes = []string{"/api/v1/auth/login", "/api/v1/auth/initialize"}

// Middleware 对每个请求执行 Host 与 Origin 校验（fail closed），并在所有
// 响应上附加安全头。
func (p *WebProtection) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if !p.hostAllowed(request.Host) {
			WriteError(response, request, "FORBIDDEN", "host is not allowed")
			return
		}
		if !p.originAllowed(request) {
			WriteError(response, request, "FORBIDDEN", "origin is not allowed")
			return
		}
		if unsafeMethods[request.Method] && !p.csrfExempt(request.URL.Path) {
			if !verifyCSRF(request) {
				WriteError(response, request, "FORBIDDEN", "csrf token missing or invalid")
				return
			}
		}
		applySecurityHeaders(response)
		next.ServeHTTP(response, request)
	})
}

func (p *WebProtection) hostAllowed(host string) bool {
	hostname := stripPort(strings.ToLower(strings.TrimSpace(host)))
	for _, allowed := range p.AllowedHosts {
		if hostname == stripPort(strings.ToLower(strings.TrimSpace(allowed))) {
			return true
		}
	}
	return false
}

func stripPort(host string) string {
	if index := strings.LastIndex(host, ":"); index > strings.LastIndex(host, "]") {
		return host[:index]
	}
	return host
}

// originAllowed 仅在请求携带 Origin 时校验（同源工具与 curl 可不带 Origin）。
func (p *WebProtection) originAllowed(request *http.Request) bool {
	origin := strings.TrimSpace(request.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	for _, allowed := range p.AllowedOrigins {
		if strings.EqualFold(origin, strings.TrimSpace(allowed)) {
			return true
		}
	}
	return false
}

func (p *WebProtection) csrfExempt(path string) bool {
	for _, prefix := range csrfExemptPrefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

// verifyCSRF 实现双提交令牌：cookie 与 header 必须一致且非空。
func verifyCSRF(request *http.Request) bool {
	cookie, err := request.Cookie(CSRFCookieName)
	if err != nil || strings.TrimSpace(cookie.Value) == "" {
		return false
	}
	header := strings.TrimSpace(request.Header.Get(CSRFHeaderName))
	if header == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(header)) == 1
}

func applySecurityHeaders(response http.ResponseWriter) {
	header := response.Header()
	if header.Get("Content-Security-Policy") == "" {
		header.Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'; form-action 'self'; base-uri 'none'")
	}
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("X-Frame-Options", "DENY")
	// API 响应携带会话相关数据，禁止任何层级的缓存（OWASP 安全响应头）。
	header.Set("Cache-Control", "no-store")
	header.Set("Pragma", "no-cache")
}

// NewSessionCookies 为已认证会话签发 cookie 对：HttpOnly 会话令牌与
// 前端可读的 CSRF 令牌。CSRF 使用根路径以供 SPA 读取，会话限定 API 路径。
func NewSessionCookies(sessionToken, csrfToken string, maxAge int) []*http.Cookie {
	return []*http.Cookie{
		{
			Name:     SessionCookieName,
			Value:    sessionToken,
			Path:     "/api/v1",
			MaxAge:   maxAge,
			Secure:   true,
			HttpOnly: true,
			SameSite: http.SameSiteStrictMode,
		},
		{
			Name:     CSRFCookieName,
			Value:    csrfToken,
			Path:     "/",
			MaxAge:   maxAge,
			Secure:   true,
			HttpOnly: false,
			SameSite: http.SameSiteStrictMode,
		},
		{Name: CSRFCookieName, Path: "/api/v1", MaxAge: -1, Secure: true, SameSite: http.SameSiteStrictMode},
	}
}

// ClearSessionCookies 使会话与 CSRF cookie 过期。
func ClearSessionCookies() []*http.Cookie {
	expired := NewSessionCookies("", "", -1)
	return expired
}

// NewCSRFToken 返回 32 字节随机 CSRF 令牌（base64url）。
func NewCSRFToken() (string, error) {
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(token), nil
}
