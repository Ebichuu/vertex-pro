package proxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/qbresponse"
	"github.com/vertex-pro/vertex-pro/internal/secrets"
	"github.com/vertex-pro/vertex-pro/internal/syncmerge"
)

const (
	maxAddResponseBytes = 1 << 20
	proxyRouteContract  = "qui-client-proxy-v1|GET:/proxy/{key}/api/v2/{allowlisted-endpoint}|POST:/proxy/{key}/api/v2/torrents/add|no-direct-qb-origin"
)

// RouteFingerprint identifies the local qui Client Proxy routing contract used
// by both verification evidence and runtime activation decisions.
func RouteFingerprint() string {
	digest := sha256.Sum256([]byte(proxyRouteContract))
	return "sha256:" + hex.EncodeToString(digest[:])
}

type Config struct {
	BaseURL    string
	ProxyKey   secrets.SecretRef
	Store      secrets.Store
	HTTPClient *http.Client
}

type Client struct {
	baseURL    *url.URL
	proxyKey   string
	httpClient *http.Client
}

type HTTPError struct {
	StatusCode int
	Status     string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("qB proxy request failed: %s", e.Status)
}

func New(ctx context.Context, config Config) (*Client, error) {
	rawURL := strings.TrimSpace(config.BaseURL)
	if rawURL == "" {
		return nil, errors.New("qui base URL is required")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("invalid qui base URL %q", rawURL)
	}
	if strings.Contains(strings.ToLower(parsed.Hostname()), "qbittorrent") || strings.HasPrefix(strings.ToLower(parsed.Hostname()), "qb-") {
		return nil, errors.New("direct qB origin is forbidden")
	}

	if config.Store == nil {
		return nil, errors.New("secret store is required")
	}
	proxyKey, err := config.Store.Resolve(ctx, config.ProxyKey)
	if err != nil {
		return nil, fmt.Errorf("resolve proxy key: %w", err)
	}
	if strings.TrimSpace(proxyKey) == "" || strings.ContainsAny(proxyKey, "/?#") {
		return nil, errors.New("proxy key is invalid")
	}
	client := config.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &Client{baseURL: parsed, proxyKey: proxyKey, httpClient: client}, nil
}

func (client *Client) Version(ctx context.Context) (string, error) {
	var response string
	if err := client.getJSON(ctx, "app/version", &response); err != nil {
		return "", err
	}
	if strings.TrimSpace(response) == "" {
		return "", errors.New("qB app version is missing")
	}
	return response, nil
}

func (client *Client) WebAPIVersion(ctx context.Context) (string, error) {
	var response string
	if err := client.getJSON(ctx, "app/webapiVersion", &response); err != nil {
		return "", err
	}
	if strings.TrimSpace(response) == "" {
		return "", errors.New("qB WebAPI version is missing")
	}
	return response, nil
}

func (client *Client) BuildInfo(ctx context.Context) (map[string]any, error) {
	return client.object(ctx, "app/buildInfo", false)
}

// Preferences reads qB application preferences through qui. Callers must treat
// path values as sensitive operational data and must not persist them in
// evidence without redaction.
func (client *Client) Preferences(ctx context.Context) (map[string]any, error) {
	return client.object(ctx, "app/preferences", false)
}

func (client *Client) ProcessInfo(ctx context.Context) (map[string]any, error) {
	return client.object(ctx, "app/processInfo", true)
}

func (client *Client) SyncMainData(ctx context.Context, rid uint64) (map[string]any, error) {
	path := "sync/maindata?rid=" + fmt.Sprintf("%d", rid)
	return client.object(ctx, path, false)
}

// Fetch adapts qB's Sync API response to the SyncWatcher transport contract.
func (client *Client) Fetch(ctx context.Context, rid uint64) (syncmerge.Delta, error) {
	response, err := client.SyncMainData(ctx, rid)
	if err != nil {
		return syncmerge.Delta{}, err
	}
	payload, err := json.Marshal(response)
	if err != nil {
		return syncmerge.Delta{}, fmt.Errorf("marshal qB sync response: %w", err)
	}
	var delta struct {
		RID         uint64                    `json:"rid"`
		FullUpdate  bool                      `json:"full_update"`
		Torrents    map[string]map[string]any `json:"torrents"`
		Removed     []string                  `json:"torrents_removed"`
		ServerState map[string]any            `json:"server_state"`
	}
	if err := json.Unmarshal(payload, &delta); err != nil {
		return syncmerge.Delta{}, fmt.Errorf("decode qB sync response: %w", err)
	}
	return syncmerge.Delta{RID: delta.RID, FullUpdate: delta.FullUpdate, Torrents: delta.Torrents, Removed: delta.Removed, ServerState: delta.ServerState}, nil
}

func (client *Client) Trackers(ctx context.Context, torrentHash string) ([]map[string]any, error) {
	if strings.TrimSpace(torrentHash) == "" || strings.ContainsAny(torrentHash, "&?#") {
		return nil, errors.New("torrent hash is required")
	}
	var response []map[string]any
	if err := client.getJSON(ctx, "torrents/trackers?hash="+url.QueryEscape(torrentHash), &response); err != nil {
		return nil, err
	}
	if response == nil {
		return nil, errors.New("qB trackers response is missing")
	}
	return response, nil
}

// Send forwards exactly one validated Add request through this instance's qui
// Client Proxy. It never accepts an absolute qB origin or another qB action.
func (client *Client) Send(ctx context.Context, input *http.Request) (qbresponse.RawResponse, error) {
	if client == nil || client.baseURL == nil || client.httpClient == nil {
		return qbresponse.RawResponse{}, errors.New("qB proxy client is not initialized")
	}
	if ctx == nil {
		return qbresponse.RawResponse{}, errors.New("context is nil")
	}
	if input == nil || input.URL == nil || input.Body == nil {
		return qbresponse.RawResponse{}, errors.New("Add request is required")
	}
	if input.Method != http.MethodPost || input.URL.Scheme != "" || input.URL.Host != "" || input.URL.User != nil || input.URL.Path != "/api/v2/torrents/add" || input.URL.RawQuery != "" || input.URL.Fragment != "" {
		return qbresponse.RawResponse{}, errors.New("only a relative qB Add endpoint is allowed")
	}
	contentType := input.Header.Get("Content-Type")
	if !strings.HasPrefix(strings.ToLower(contentType), "multipart/form-data;") {
		return qbresponse.RawResponse{}, errors.New("Add request content type is invalid")
	}

	requestURL := *client.baseURL
	requestURL.Path = strings.TrimRight(requestURL.Path, "/") + "/proxy/" + url.PathEscape(client.proxyKey) + input.URL.Path
	requestURL.RawQuery = ""
	outgoing, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL.String(), input.Body)
	if err != nil {
		return qbresponse.RawResponse{}, fmt.Errorf("build qB proxy Add request: %w", err)
	}
	outgoing.ContentLength = input.ContentLength
	outgoing.Header.Set("Content-Type", contentType)
	outgoing.Header.Set("Accept", "application/json, text/plain")
	response, err := client.httpClient.Do(outgoing)
	if err != nil {
		return qbresponse.RawResponse{}, &transportError{cause: err}
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxAddResponseBytes+1))
	if err != nil {
		return qbresponse.RawResponse{StatusCode: response.StatusCode}, fmt.Errorf("read qB proxy Add response: %w", err)
	}
	if len(body) > maxAddResponseBytes {
		return qbresponse.RawResponse{StatusCode: response.StatusCode}, errors.New("qB proxy Add response exceeds limit")
	}
	return qbresponse.RawResponse{StatusCode: response.StatusCode, Body: body}, nil
}

func (client *Client) object(ctx context.Context, endpoint string, optional bool) (map[string]any, error) {
	var response map[string]any
	if err := client.getJSON(ctx, endpoint, &response); err != nil {
		var httpErr *HTTPError
		if optional && errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusNotFound {
			return nil, nil
		}
		return nil, err
	}
	if response == nil {
		return nil, errors.New("qB response is missing")
	}
	return response, nil
}

func (client *Client) getJSON(ctx context.Context, endpoint string, target any) error {
	if !allowedEndpoint(endpoint) {
		return fmt.Errorf("qB proxy endpoint is not allowlisted: %s", endpoint)
	}
	requestURL := *client.baseURL
	requestURL.Path = strings.TrimRight(requestURL.Path, "/") + "/proxy/" + url.PathEscape(client.proxyKey) + "/api/v2/" + strings.SplitN(endpoint, "?", 2)[0]
	if query := strings.IndexByte(endpoint, '?'); query >= 0 {
		requestURL.RawQuery = endpoint[query+1:]
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return fmt.Errorf("build qB proxy request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	response, err := client.httpClient.Do(request)
	if err != nil {
		return &transportError{cause: err}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &HTTPError{StatusCode: response.StatusCode, Status: response.Status}
	}
	if response.StatusCode == http.StatusNoContent {
		return errors.New("qB proxy response has no content")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxAddResponseBytes+1))
	if err != nil {
		return fmt.Errorf("read qB proxy response: %w", err)
	}
	if len(body) > maxAddResponseBytes {
		return errors.New("qB proxy response exceeds limit")
	}
	if textTarget, ok := target.(*string); ok {
		if err := json.Unmarshal(body, textTarget); err == nil {
			return nil
		}
		text := strings.TrimSpace(string(body))
		if text == "" {
			return errors.New("qB proxy text response is empty")
		}
		*textTarget = text
		return nil
	}
	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("decode qB proxy response: %w", err)
	}
	return nil
}

// net/http includes the request URL in transport errors. Proxy credentials
// live in the path and must never reach logs or callers through Error().
type transportError struct{ cause error }

func (err *transportError) Error() string { return "qB proxy transport failed" }
func (err *transportError) Unwrap() error { return err.cause }

func allowedEndpoint(endpoint string) bool {
	switch strings.SplitN(endpoint, "?", 2)[0] {
	case "app/version", "app/webapiVersion", "app/buildInfo", "app/preferences", "app/processInfo", "sync/maindata", "sync/torrentPeers", "torrents/trackers":
		return true
	default:
		return false
	}
}
