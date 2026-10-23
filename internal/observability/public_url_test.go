package observability

import "testing"

func TestPublicURLNeverExposesSubscriptionCredentials(t *testing.T) {
	for _, raw := range []string{"https://user:password@example.test/private-token/rss?passkey=secret#secret", "https://example.test/token-in-path", "https://example.test/rss?unrecognized_auth=secret"} {
		if got := PublicURL(raw); got != "https://example.test/" {
			t.Fatalf("unsafe display URL: %q", got)
		}
	}
	if PublicURL("not a URL") != "" {
		t.Fatal("invalid URL accepted")
	}
}
