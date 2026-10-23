package control

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/secrets"
)

func TestTransferInfoRejectsMissingCounters(t *testing.T) {
	for _, raw := range []string{`null`, `{}`, `{"dl_info_speed":0,"up_info_speed":0,"dl_info_data":0,"up_info_data":null}`} {
		var value TransferInfo
		if err := json.Unmarshal([]byte(raw), &value); err == nil {
			t.Fatalf("accepted incomplete transfer %s", raw)
		}
	}
}

type staticStore struct{ value string }

func (store staticStore) Resolve(context.Context, secrets.SecretRef) (string, error) {
	return store.value, nil
}

func TestClientRequestsInstancesAndTransferInfoWithFixedOriginAndAuth(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		paths = append(paths, request.URL.Path)
		if request.Header.Get("X-API-Key") != "test-key" {
			t.Fatalf("X-API-Key = %q", request.Header.Get("X-API-Key"))
		}
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/api/instances":
			_, _ = writer.Write([]byte(`[{"id":13,"name":"qB 5.0.4","connectionStatus":"connected","isActive":true}]`))
		case "/api/instances/qb-1/transfer-info":
			_, _ = writer.Write([]byte(`{"dl_info_speed":12,"up_info_speed":34,"dl_info_data":56,"up_info_data":78}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	client, err := New(context.Background(), Config{BaseURL: server.URL + "/", APIKey: secrets.SecretRef{Provider: "env", Key: "X"}, Store: staticStore{value: "test-key"}})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	instances, err := client.Instances(context.Background())
	if err != nil || len(instances) != 1 || instances[0].ID != "13" || instances[0].Status != "connected" || !instances[0].Enabled {
		t.Fatalf("Instances() = %+v, %v", instances, err)
	}
	transfer, err := client.TransferInfo(context.Background(), "qb-1")
	if err != nil || transfer.Uploaded != 78 {
		t.Fatalf("TransferInfo() = %+v, %v", transfer, err)
	}
	if strings.Join(paths, ",") != "/api/instances,/api/instances/qb-1/transfer-info" {
		t.Fatalf("paths = %v", paths)
	}
}

func TestClientMapsHTTPFailuresAndOptionalCapabilitiesFallback(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests, http.StatusInternalServerError} {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) { http.Error(writer, "secret-body", status) }))
		client, err := New(context.Background(), Config{BaseURL: server.URL, APIKey: secrets.SecretRef{Provider: "env", Key: "X"}, Store: staticStore{value: "key"}})
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}
		_, err = client.Instances(context.Background())
		var httpErr *HTTPError
		if !errors.As(err, &httpErr) || httpErr.StatusCode != status {
			t.Fatalf("status %d error = %v", status, err)
		}
		if strings.Contains(err.Error(), "secret-body") {
			t.Fatal("HTTP error leaked response body")
		}
		server.Close()
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) { http.NotFound(writer, request) }))
	defer server.Close()
	client, err := New(context.Background(), Config{BaseURL: server.URL, APIKey: secrets.SecretRef{Provider: "env", Key: "X"}, Store: staticStore{value: "key"}})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	capabilities, err := client.Capabilities(context.Background(), "qb-1")
	if err != nil || capabilities != nil {
		t.Fatalf("Capabilities() = %+v, %v", capabilities, err)
	}
}

func TestClientRejectsInvalidResponsesAndHonorsTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) { time.Sleep(100 * time.Millisecond) }))
	defer server.Close()
	client, err := New(context.Background(), Config{BaseURL: server.URL, APIKey: secrets.SecretRef{Provider: "env", Key: "X"}, Store: staticStore{value: "key"}, HTTPClient: &http.Client{Timeout: 10 * time.Millisecond}})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, err := client.Instances(context.Background()); err == nil {
		t.Fatal("timeout error = nil")
	}
	invalid := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`[{"name":"missing-id"}]`))
	}))
	defer invalid.Close()
	client, err = New(context.Background(), Config{BaseURL: invalid.URL, APIKey: secrets.SecretRef{Provider: "env", Key: "X"}, Store: staticStore{value: "key"}})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, err := client.Instances(context.Background()); err == nil {
		t.Fatal("missing ID error = nil")
	}
}

func TestClientRejectsUnsafeBaseURLAndEmptyInstanceID(t *testing.T) {
	for _, baseURL := range []string{"", "not a URL", "file:///tmp/qui", "https://user:pass@example.test", "https://example.test?token=secret"} {
		if _, err := New(context.Background(), Config{BaseURL: baseURL, APIKey: secrets.SecretRef{Provider: "env", Key: "X"}, Store: staticStore{value: "key"}}); err == nil {
			t.Fatalf("New(%q) error = nil", baseURL)
		}
	}
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	client, err := New(context.Background(), Config{BaseURL: server.URL, APIKey: secrets.SecretRef{Provider: "env", Key: "X"}, Store: staticStore{value: "key"}})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, err := client.TransferInfo(context.Background(), ""); err == nil {
		t.Fatal("empty instance ID error = nil")
	}
}
