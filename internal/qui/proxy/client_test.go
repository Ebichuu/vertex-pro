package proxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/vertex-pro/vertex-pro/internal/secrets"
)

type staticStore struct{ value string }

func (store staticStore) Resolve(context.Context, secrets.SecretRef) (string, error) {
	return store.value, nil
}

func TestClientUsesPerInstanceProxyPathAndAllowlistedEndpoints(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/proxy/proxy-key/api/v2/sync/maindata" || request.URL.RawQuery != "rid=4" {
			t.Fatalf("request path/query = %s?%s", request.URL.Path, request.URL.RawQuery)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"rid":4,"full_update":true}`))
	}))
	defer server.Close()
	client, err := New(context.Background(), Config{BaseURL: server.URL + "/", ProxyKey: secrets.SecretRef{Provider: "env", Key: "PROXY_KEY"}, Store: staticStore{value: "proxy-key"}})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	response, err := client.SyncMainData(context.Background(), 4)
	if err != nil || response["rid"] != float64(4) {
		t.Fatalf("SyncMainData() = %+v, %v", response, err)
	}
	delta, err := client.Fetch(context.Background(), 4)
	if err != nil || delta.RID != 4 || !delta.FullUpdate {
		t.Fatalf("Fetch() = %+v, %v", delta, err)
	}
}

func TestClientSupportsProbeEndpointsAndOptionalProcessInfo(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/proxy/key/api/v2/app/version", "/proxy/key/api/v2/app/webapiVersion":
			_, _ = writer.Write([]byte(`2.15.1`))
		case "/proxy/key/api/v2/app/buildInfo":
			_, _ = writer.Write([]byte(`{"libtorrent":"2.0.9"}`))
		case "/proxy/key/api/v2/app/preferences":
			_, _ = writer.Write([]byte(`{"save_path":"/downloads"}`))
		case "/proxy/key/api/v2/app/processInfo":
			http.NotFound(writer, request)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	client, err := New(context.Background(), Config{BaseURL: server.URL, ProxyKey: secrets.SecretRef{Provider: "env", Key: "PROXY_KEY"}, Store: staticStore{value: "key"}})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	version, err := client.Version(context.Background())
	if err != nil || version != "2.15.1" {
		t.Fatalf("Version() = %q, %v", version, err)
	}
	webAPI, err := client.WebAPIVersion(context.Background())
	if err != nil || webAPI != "2.15.1" {
		t.Fatalf("WebAPIVersion() = %q, %v", webAPI, err)
	}
	build, err := client.BuildInfo(context.Background())
	if err != nil || build["libtorrent"] != "2.0.9" {
		t.Fatalf("BuildInfo() = %+v, %v", build, err)
	}
	preferences, err := client.Preferences(context.Background())
	if err != nil || preferences["save_path"] != "/downloads" {
		t.Fatalf("Preferences() = %+v, %v", preferences, err)
	}
	process, err := client.ProcessInfo(context.Background())
	if err != nil || process != nil {
		t.Fatalf("ProcessInfo() = %+v, %v", process, err)
	}
}

func TestClientForwardsOnlyAddRequestsThroughProxy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/proxy/key/api/v2/torrents/add" {
			t.Fatalf("request = %s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get("Content-Type") != "multipart/form-data; boundary=test" {
			t.Fatalf("content type = %q", request.Header.Get("Content-Type"))
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != "torrent-body" {
			t.Fatalf("body = %q", body)
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	client, err := New(context.Background(), Config{BaseURL: server.URL, ProxyKey: secrets.SecretRef{Provider: "env", Key: "PROXY_KEY"}, Store: staticStore{value: "key"}})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, "/api/v2/torrents/add", io.NopCloser(strings.NewReader("torrent-body")))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "multipart/form-data; boundary=test")
	result, err := client.Send(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.StatusCode != http.StatusNoContent || len(result.Body) != 0 {
		t.Fatalf("result = %+v", result)
	}
}

func TestClientRejectsNonAddSendRequests(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	client, err := New(context.Background(), Config{BaseURL: server.URL, ProxyKey: secrets.SecretRef{Provider: "env", Key: "PROXY_KEY"}, Store: staticStore{value: "key"}})
	if err != nil {
		t.Fatal(err)
	}
	cases := []*http.Request{
		{Method: http.MethodGet, URL: &url.URL{Path: "/api/v2/torrents/add"}, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("body"))},
		{Method: http.MethodPost, URL: &url.URL{Path: "/api/v2/torrents/delete"}, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("body"))},
		{Method: http.MethodPost, URL: &url.URL{Scheme: "http", Host: "qb.internal", Path: "/api/v2/torrents/add"}, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("body"))},
	}
	for _, request := range cases {
		request.Header.Set("Content-Type", "multipart/form-data; boundary=test")
		if _, err := client.Send(context.Background(), request); err == nil {
			t.Fatal("invalid Send request was accepted")
		}
	}
}

func TestClientRejectsDirectQbAndNonAllowlistedEndpoint(t *testing.T) {
	for _, baseURL := range []string{"http://qbittorrent:8080", "http://qb-main:8080", "https://example.test?token=secret"} {
		if _, err := New(context.Background(), Config{BaseURL: baseURL, ProxyKey: secrets.SecretRef{Provider: "env", Key: "PROXY_KEY"}, Store: staticStore{value: "key"}}); err == nil {
			t.Fatal("direct qB origin accepted")
		}
	}
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	client, err := New(context.Background(), Config{BaseURL: server.URL, ProxyKey: secrets.SecretRef{Provider: "env", Key: "PROXY_KEY"}, Store: staticStore{value: "key"}})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := client.getJSON(context.Background(), "torrents/delete", &map[string]any{}); err == nil {
		t.Fatal("non-allowlisted endpoint error = nil")
	}
	if _, err := client.Trackers(context.Background(), "hash&token=secret"); err == nil {
		t.Fatal("unsafe torrent hash error = nil")
	}
}

func TestClientClassifiesProxyHTTPFailuresWithoutBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Error(writer, "secret-body", http.StatusUnauthorized)
	}))
	defer server.Close()
	client, err := New(context.Background(), Config{BaseURL: server.URL, ProxyKey: secrets.SecretRef{Provider: "env", Key: "PROXY_KEY"}, Store: staticStore{value: "key"}})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	_, err = client.Version(context.Background())
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusUnauthorized {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(err.Error(), "secret-body") {
		t.Fatal("proxy error leaked body")
	}
}
