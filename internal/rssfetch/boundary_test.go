package rssfetch

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// HARD-04 负测：私网/环回目标与跨主机重定向必须在边界被拒绝。
func TestBoundaryBlocksPrivateTargetsAndRedirectHops(t *testing.T) {
	outer := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/redirect" {
			http.Redirect(response, request, "http://rebound.example/leak", http.StatusFound)
			return
		}
		_, _ = response.Write([]byte("feed"))
	}))
	defer outer.Close()

	resolver := fakeDNS{answers: map[string][]net.IPAddr{
		"public.example":  {{IP: net.ParseIP("93.184.216.34")}},
		"rebound.example": {{IP: net.ParseIP("10.0.0.9")}},
	}}

	hardened := Boundary{RejectPrivateIPs: true, Resolver: resolver}
	fetcher, err := New(nil, DefaultMaxBodyBytes)
	if err != nil {
		t.Fatal(err)
	}

	// 非加固模式保持本地开发行为：环回地址可拉取。
	if _, err := fetcher.Fetch(context.Background(), Request{URL: outer.URL}); err != nil {
		t.Fatalf("unhardened fetch failed: %v", err)
	}

	// 加固模式：环回地址直接拒绝。
	hardenedFetcher := fetcher.WithBoundary(hardened)
	if _, err := hardenedFetcher.Fetch(context.Background(), Request{URL: outer.URL}); err == nil {
		t.Fatal("hardened fetch to loopback must be rejected")
	}

	// DNS 绕过：解析到私网段的主机名被拒绝。
	if _, err := hardenedFetcher.Fetch(context.Background(), Request{URL: "http://rebound.example/feed"}); err == nil {
		t.Fatal("DNS bypass to private range must be rejected")
	}
	if _, err := hardenedFetcher.Fetch(context.Background(), Request{URL: "http://public.example/feed"}); err == nil {
		t.Fatal("cannot reach fake public host in this environment")
	} else if !strings.Contains(err.Error(), "fetch feed") {
		t.Fatalf("public host should pass the boundary and fail on dial: %v", err)
	}

	// 白名单 + 重定向：允许的主机把请求重定向到清单外/私网目标时拒绝。
	outerHost := func() string {
		parsed, err := url.Parse(outer.URL)
		if err != nil {
			t.Fatal(err)
		}
		return parsed.Hostname()
	}()
	allowlisted := Boundary{
		AllowedHosts:     map[string]struct{}{outerHost: {}},
		RejectPrivateIPs: false,
		Resolver:         resolver,
	}
	listed := fetcher.WithBoundary(allowlisted)
	if _, err := listed.Fetch(context.Background(), Request{URL: outer.URL}); err != nil {
		t.Fatalf("allowlisted direct fetch failed: %v", err)
	}
	if _, err := listed.Fetch(context.Background(), Request{URL: outer.URL + "/redirect"}); err == nil {
		t.Fatal("redirect to a target outside the allowlist must be rejected")
	}
}

type fakeDNS struct {
	answers map[string][]net.IPAddr
}

func (resolver fakeDNS) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	if answer, ok := resolver.answers[host]; ok {
		return answer, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: host}
}
