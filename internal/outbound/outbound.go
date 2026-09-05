// Package outbound implements the HARD-04 outbound boundary: classification
// of loopback/private/link-local/unspecified/multicast targets, hostname
// resolution checks (a hostname that resolves into a blocked range is
// rejected, closing the DNS bypass), and redirect validation helpers shared
// by the RSS fetcher and the torrent downloader.
package outbound

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// Resolver abstracts DNS lookup so tests can inject deterministic answers.
type Resolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

// DefaultResolver resolves through the system resolver.
type DefaultResolver struct{}

func (DefaultResolver) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	return net.DefaultResolver.LookupIPAddr(ctx, host)
}

// IsBlockedIP reports whether the IP belongs to a range an outbound request
// must never target: loopback, RFC1918 and ULA private space, link-local,
// unspecified, and multicast.
func IsBlockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() || ip.IsInterfaceLocalMulticast() {
		return true
	}
	return false
}

// stripPort removes a numeric port from a host header value, handling
// bracketed IPv6 literals.
func stripPort(host string) string {
	host = strings.TrimSpace(host)
	if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil {
		return strings.Trim(host, "[]")
	}
	if hostname, _, err := net.SplitHostPort(host); err == nil {
		return strings.Trim(hostname, "[]")
	}
	return strings.Trim(host, "[]")
}

// ValidateHost rejects blocked IP literals and, for hostnames, rejects any
// DNS answer that resolves into a blocked range (DNS bypass). IPv6 answers
// are checked the same way as IPv4.
func ValidateHost(ctx context.Context, resolver Resolver, host string) error {
	host = stripPort(host)
	if host == "" {
		return fmt.Errorf("empty outbound host")
	}
	if literal := net.ParseIP(host); literal != nil {
		if IsBlockedIP(literal) {
			return fmt.Errorf("outbound target %s is a blocked IP range", host)
		}
		return nil
	}
	if resolver == nil {
		resolver = DefaultResolver{}
	}
	addresses, err := resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return fmt.Errorf("resolve outbound host %s: %w", host, err)
	}
	if len(addresses) == 0 {
		return fmt.Errorf("outbound host %s resolved to no addresses", host)
	}
	for _, address := range addresses {
		if IsBlockedIP(address.IP) {
			return fmt.Errorf("outbound host %s resolves into a blocked IP range", host)
		}
	}
	return nil
}

// ValidateURL combines the fetcher boundary rules for one request or redirect
// hop: http/https only, no userinfo, optional exact-host allowlist, and the
// blocked-IP checks above.
func ValidateURL(ctx context.Context, parsed *url.URL, resolver Resolver, allowedHosts map[string]struct{}, rejectPrivate bool) error {
	if parsed == nil {
		return fmt.Errorf("outbound URL is required")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("outbound scheme %q is not allowed", parsed.Scheme)
	}
	if parsed.User != nil {
		return fmt.Errorf("outbound URL must not carry userinfo")
	}
	host := stripPort(parsed.Host)
	if host == "" {
		return fmt.Errorf("outbound URL has no host")
	}
	if allowedHosts != nil {
		if _, ok := allowedHosts[strings.ToLower(host)]; !ok {
			return fmt.Errorf("outbound host %s is not allowlisted", host)
		}
	}
	if rejectPrivate {
		return ValidateHost(ctx, resolver, host)
	}
	return nil
}
