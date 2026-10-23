// Package sitecredentials resolves site-scoped HTTP credentials at request
// time. Only dedicated VERTEX_SITE_* environment entries are accessible.
package sitecredentials

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
)

var keyPattern = regexp.MustCompile(`^VERTEX_SITE_[A-Z0-9_]{1,100}$`)

func key(ref string) string { return strings.TrimPrefix(ref, "env:") }
func Validate(ref string) error {
	if ref == "" {
		return nil
	}
	if !strings.HasPrefix(ref, "env:") || !keyPattern.MatchString(key(ref)) {
		return fmt.Errorf("invalid credential reference: use env:VERTEX_SITE_NAME")
	}
	return nil
}

func Apply(request *http.Request, client *http.Client, ref, origin string) (*http.Client, error) {
	if ref == "" {
		return client, nil
	}
	if err := Validate(ref); err != nil {
		return nil, err
	}
	if request == nil || request.URL == nil || client == nil {
		return nil, fmt.Errorf("credential request is unavailable")
	}
	base, err := url.Parse(origin)
	if err != nil || base.Host == "" || base.User != nil || (base.Scheme != "http" && base.Scheme != "https") {
		return nil, fmt.Errorf("invalid credential origin")
	}
	if base.Scheme == "http" && !isLoopback(base.Hostname()) {
		return nil, fmt.Errorf("site credentials require HTTPS")
	}
	if !sameOrigin(base, request.URL) {
		return nil, fmt.Errorf("site credentials cannot be sent to a different origin")
	}
	raw, exists := os.LookupEnv(key(ref))
	if !exists || len(raw) == 0 || len(raw) > 16384 {
		return nil, fmt.Errorf("site credential is unavailable")
	}
	var values map[string]string
	if err := json.Unmarshal([]byte(raw), &values); err != nil || len(values) == 0 {
		return nil, fmt.Errorf("site credential must contain HTTP header JSON")
	}
	for name, value := range values {
		canonical := http.CanonicalHeaderKey(name)
		if (canonical != "Cookie" && canonical != "Authorization") || strings.ContainsAny(value, "\r\n\x00") {
			return nil, fmt.Errorf("site credential header is invalid")
		}
	}
	if request.Header == nil {
		request.Header = make(http.Header)
	}
	for name, value := range values {
		canonical := http.CanonicalHeaderKey(name)
		request.Header.Set(canonical, value)
	}
	clone := *client
	previous := clone.CheckRedirect
	clone.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("too many redirects")
		}
		if previous != nil {
			if err := previous(next, via); err != nil {
				return err
			}
		}
		if !sameOrigin(base, next.URL) {
			return fmt.Errorf("credential redirect origin mismatch")
		}
		return nil
	}
	return &clone, nil
}
func sameOrigin(a, b *url.URL) bool {
	if a == nil || b == nil {
		return false
	}
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Host, b.Host)
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
