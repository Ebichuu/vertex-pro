package outbound

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"testing"
)

type fakeResolver struct {
	answers map[string][]net.IPAddr
}

func (resolver fakeResolver) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	if answer, ok := resolver.answers[host]; ok {
		return answer, nil
	}
	return nil, fmt.Errorf("no answer for %s", host)
}

func TestIsBlockedIPRanges(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "10.0.0.5", "172.16.1.1", "172.31.255.255", "192.168.1.1",
		"169.254.169.254", "0.0.0.0", "::1", "fe80::1", "fc00::1", "ff02::1",
	}
	for _, literal := range blocked {
		if !IsBlockedIP(net.ParseIP(literal)) {
			t.Fatalf("%s must be blocked", literal)
		}
	}
	allowed := []string{"8.8.8.8", "1.1.1.1", "93.184.216.34", "2606:2800:220:1:248:1893:25c8:1946"}
	for _, literal := range allowed {
		if IsBlockedIP(net.ParseIP(literal)) {
			t.Fatalf("%s must not be blocked", literal)
		}
	}
}

func TestValidateHostChecksLiteralsAndDNSAnswers(t *testing.T) {
	resolver := fakeResolver{answers: map[string][]net.IPAddr{
		"pt.example":      {{IP: net.ParseIP("93.184.216.34")}},
		"rebound.example": {{IP: net.ParseIP("10.0.0.9")}},
		"dual.example":    {{IP: net.ParseIP("93.184.216.34")}, {IP: net.ParseIP("127.0.0.2")}},
	}}
	ctx := context.Background()
	if err := ValidateHost(ctx, resolver, "127.0.0.1:8080"); err == nil {
		t.Fatal("loopback literal must be rejected")
	}
	if err := ValidateHost(ctx, resolver, "pt.example"); err != nil {
		t.Fatalf("public hostname rejected: %v", err)
	}
	if err := ValidateHost(ctx, resolver, "rebound.example"); err == nil {
		t.Fatal("DNS bypass to private range must be rejected")
	}
	// 双栈答案只要有一个地址落入封锁网段就必须整体拒绝。
	if err := ValidateHost(ctx, resolver, "dual.example"); err == nil {
		t.Fatal("dual-stack answer containing a blocked address must be rejected")
	}
	// 带端口的主机头必须剥掉端口后校验。
	if err := ValidateHost(ctx, resolver, "pt.example:443"); err != nil {
		t.Fatalf("host with port rejected: %v", err)
	}
}

func TestValidateURLRules(t *testing.T) {
	parse := func(raw string) (u *url.URL) {
		parsed, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		return parsed
	}
	allowlist := map[string]struct{}{"pt.example": {}}
	ctx := context.Background()
	if err := ValidateURL(ctx, parse("ftp://pt.example/feed"), nil, allowlist, false); err == nil {
		t.Fatal("non-http scheme must be rejected")
	}
	if err := ValidateURL(ctx, parse("https://user:pass@pt.example/feed"), nil, allowlist, false); err == nil {
		t.Fatal("userinfo must be rejected")
	}
	if err := ValidateURL(ctx, parse("https://other.example/feed"), nil, allowlist, false); err == nil {
		t.Fatal("non-allowlisted host must be rejected")
	}
	if err := ValidateURL(ctx, parse("https://pt.example:8443/feed"), nil, allowlist, false); err != nil {
		t.Fatalf("allowlisted host with port rejected: %v", err)
	}
	if err := ValidateURL(ctx, parse("http://127.0.0.1/feed"), nil, allowlist, true); err == nil {
		t.Fatal("private IP literal must be rejected when hardened")
	}
	if err := ValidateURL(ctx, parse("http://127.0.0.1/feed"), nil, nil, false); err != nil {
		t.Fatalf("unhardened mode without allowlist must tolerate loopback for local development: %v", err)
	}
}
