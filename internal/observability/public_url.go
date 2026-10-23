package observability

import "net/url"

// PublicURL exposes only the origin. Subscription paths, query strings and
// userinfo may all contain credentials; the original remains server-side.
func PublicURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	return (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: "/"}).String()
}
