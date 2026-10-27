package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/vertex-pro/vertex-pro/internal/secrets"
)

const hashA = "0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c"

func TestAddTagsSendsOnlyVertexTagsThroughProxy(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		if request.Method != http.MethodPost || request.URL.Path != "/proxy/key/api/v2/torrents/addTags" || request.URL.RawQuery != "" {
			t.Errorf("request = %s %s?%s", request.Method, request.URL.Path, request.URL.RawQuery)
		}
		if request.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			t.Errorf("content type = %q", request.Header.Get("Content-Type"))
		}
		if err := request.ParseForm(); err != nil {
			t.Error(err)
		}
		if request.PostForm.Get("hashes") != hashA+"|"+strings.Repeat("a", 64) || request.PostForm.Get("tags") != "vertex-managed,vertex-site-hdsky" {
			t.Errorf("form = %v", request.PostForm)
		}
	}))
	defer server.Close()
	client, err := New(context.Background(), Config{BaseURL: server.URL, ProxyKey: secrets.SecretRef{Provider: "env", Key: "K"}, Store: staticStore{value: "key"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.AddTags(context.Background(), []string{hashA, strings.Repeat("a", 64)}, []string{"vertex-managed", "vertex-site-hdsky"}); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d", calls.Load())
	}

	for name, call := range map[string]func() error{
		"user tag":       func() error { return client.AddTags(context.Background(), []string{hashA}, []string{"seeding"}) },
		"tag with comma": func() error { return client.AddTags(context.Background(), []string{hashA}, []string{"vertex-a,b"}) },
		"bare prefix":    func() error { return client.AddTags(context.Background(), []string{hashA}, []string{"vertex-"}) },
		"uppercase hash": func() error {
			return client.AddTags(context.Background(), []string{strings.ToUpper(hashA)}, []string{"vertex-managed"})
		},
		"pipe in hash": func() error {
			return client.AddTags(context.Background(), []string{hashA + "|x"}, []string{"vertex-managed"})
		},
		"no hashes": func() error { return client.AddTags(context.Background(), nil, []string{"vertex-managed"}) },
		"too many hashes": func() error {
			return client.AddTags(context.Background(), make([]string, MaxTagHashes+1), []string{"vertex-managed"})
		},
		"no tags": func() error { return client.AddTags(context.Background(), []string{hashA}, nil) },
	} {
		if err := call(); err == nil {
			t.Errorf("%s: error = nil", name)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("rejected requests reached qB: calls = %d", calls.Load())
	}
}

func TestAddTagsErrorDoesNotLeakProxyKey(t *testing.T) {
	client, err := New(context.Background(), Config{BaseURL: "http://qui.example", ProxyKey: secrets.SecretRef{Provider: "env", Key: "K"}, Store: staticStore{value: "private-proxy-value"}, HTTPClient: &http.Client{Transport: failingTransport{}}})
	if err != nil {
		t.Fatal(err)
	}
	err = client.AddTags(context.Background(), []string{hashA}, []string{"vertex-managed"})
	if err == nil || strings.Contains(err.Error(), "private-proxy-value") {
		t.Fatalf("error = %v", err)
	}
}
