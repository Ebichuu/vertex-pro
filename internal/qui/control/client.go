package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/secrets"
)

type Config struct {
	BaseURL    string
	APIKey     secrets.SecretRef
	Store      secrets.Store
	HTTPClient *http.Client
}

type Client struct {
	baseURL    *url.URL
	apiKey     string
	httpClient *http.Client
}

type Instance struct {
	ID      string
	Name    string
	Status  string
	Enabled bool
}

func (instance *Instance) UnmarshalJSON(data []byte) error {
	var raw struct {
		ID               json.RawMessage `json:"id"`
		Name             string          `json:"name"`
		Status           string          `json:"status"`
		ConnectionStatus string          `json:"connectionStatus"`
		Enabled          *bool           `json:"enabled"`
		IsActive         *bool           `json:"isActive"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	var id string
	if err := json.Unmarshal(raw.ID, &id); err != nil {
		var numericID json.Number
		if numericErr := json.Unmarshal(raw.ID, &numericID); numericErr != nil {
			return errors.New("qui instance response has invalid id")
		}
		id = numericID.String()
	}
	instance.ID = id
	instance.Name = raw.Name
	instance.Status = raw.Status
	if instance.Status == "" {
		instance.Status = raw.ConnectionStatus
	}
	if raw.Enabled != nil {
		instance.Enabled = *raw.Enabled
	} else if raw.IsActive != nil {
		instance.Enabled = *raw.IsActive
	}
	return nil
}

type TransferInfo struct {
	DownloadSpeed int64 `json:"dl_info_speed"`
	UploadSpeed   int64 `json:"up_info_speed"`
	Downloaded    int64 `json:"dl_info_data"`
	Uploaded      int64 `json:"up_info_data"`
}

// Missing counters are unknown, not a valid zero-rate sample.
func (info *TransferInfo) UnmarshalJSON(data []byte) error {
	var fields struct {
		DownloadSpeed *int64 `json:"dl_info_speed"`
		UploadSpeed   *int64 `json:"up_info_speed"`
		Downloaded    *int64 `json:"dl_info_data"`
		Uploaded      *int64 `json:"up_info_data"`
	}
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if fields.DownloadSpeed == nil || fields.UploadSpeed == nil || fields.Downloaded == nil || fields.Uploaded == nil {
		return errors.New("qui transfer-info is missing counters")
	}
	*info = TransferInfo{DownloadSpeed: *fields.DownloadSpeed, UploadSpeed: *fields.UploadSpeed, Downloaded: *fields.Downloaded, Uploaded: *fields.Uploaded}
	return nil
}

type HTTPError struct {
	StatusCode int
	Status     string
	// ValidationMessage is qui's bounded error text for a 400/422 rejection of
	// a request payload. It is never part of Error(), so response bodies do not
	// reach logs; callers that show it to an operator read it explicitly.
	ValidationMessage string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("qui control request failed: %s", e.Status)
}

const (
	maxErrorBodyBytes    = 4 << 10
	maxResponseBodyBytes = 16 << 20
)

func New(ctx context.Context, config Config) (*Client, error) {
	rawURL := strings.TrimSpace(config.BaseURL)
	if rawURL == "" {
		return nil, errors.New("qui base URL is required")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "http" && parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("invalid qui base URL %q", rawURL)
	}
	if config.Store == nil {
		return nil, errors.New("secret store is required")
	}
	apiKey, err := config.Store.Resolve(ctx, config.APIKey)
	if err != nil {
		return nil, fmt.Errorf("resolve qui API key: %w", err)
	}
	client := config.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &Client{baseURL: parsed, apiKey: apiKey, httpClient: client}, nil
}

func (client *Client) Instances(ctx context.Context) ([]Instance, error) {
	var response []Instance
	if err := client.getJSON(ctx, "/api/instances", &response); err != nil {
		return nil, err
	}
	if response == nil {
		return nil, errors.New("qui instances response is missing")
	}
	for _, instance := range response {
		if strings.TrimSpace(instance.ID) == "" {
			return nil, errors.New("qui instance response has missing id")
		}
	}
	return response, nil
}

func (client *Client) TransferInfo(ctx context.Context, instanceID string) (TransferInfo, error) {
	if strings.TrimSpace(instanceID) == "" {
		return TransferInfo{}, errors.New("instance ID is required")
	}
	var response TransferInfo
	if err := client.getJSON(ctx, "/api/instances/"+url.PathEscape(instanceID)+"/transfer-info", &response); err != nil {
		return TransferInfo{}, err
	}
	if response.DownloadSpeed < 0 || response.UploadSpeed < 0 || response.Downloaded < 0 || response.Uploaded < 0 {
		return TransferInfo{}, errors.New("qui transfer-info contains negative counters")
	}
	return response, nil
}

func (client *Client) Capabilities(ctx context.Context, instanceID string) (map[string]any, error) {
	if strings.TrimSpace(instanceID) == "" {
		return nil, errors.New("instance ID is required")
	}
	var response map[string]any
	err := client.getJSON(ctx, "/api/instances/"+url.PathEscape(instanceID)+"/capabilities", &response)
	if err != nil {
		var httpErr *HTTPError
		if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusNotFound {
			return nil, nil
		}
		return nil, err
	}
	if response == nil {
		return nil, errors.New("qui capabilities response is missing")
	}
	return response, nil
}

func (client *Client) getJSON(ctx context.Context, path string, target any) error {
	return client.doJSON(ctx, http.MethodGet, path, nil, target)
}

// doJSON sends an optional JSON body and decodes a bounded JSON response.
// path may carry a query string.
func (client *Client) doJSON(ctx context.Context, method, path string, body any, target any) error {
	requestURL := *client.baseURL
	route, query, _ := strings.Cut(path, "?")
	requestURL.Path = strings.TrimRight(requestURL.Path, "/") + route
	requestURL.RawQuery = query
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode qui request: %w", err)
		}
		payload = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, requestURL.String(), payload)
	if err != nil {
		return fmt.Errorf("build qui request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("X-API-Key", client.apiKey)
	response, err := client.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("qui request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		httpErr := &HTTPError{StatusCode: response.StatusCode, Status: response.Status}
		if response.StatusCode == http.StatusBadRequest || response.StatusCode == http.StatusUnprocessableEntity {
			httpErr.ValidationMessage = errorMessage(response.Body)
		}
		return httpErr
	}
	if response.StatusCode == http.StatusNoContent {
		return errors.New("qui response has no content")
	}
	limited := io.LimitReader(response.Body, maxResponseBodyBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return fmt.Errorf("read qui response: %w", err)
	}
	if len(data) > maxResponseBodyBytes {
		return errors.New("qui response exceeds limit")
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("decode qui response: %w", err)
	}
	return nil
}

// errorMessage extracts qui's {"error": "..."} text, or a trimmed plain body.
func errorMessage(body io.Reader) string {
	data, _ := io.ReadAll(io.LimitReader(body, maxErrorBodyBytes))
	var envelope struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(data, &envelope) == nil {
		if envelope.Error != "" {
			return envelope.Error
		}
		if envelope.Message != "" {
			return envelope.Message
		}
	}
	return strings.TrimSpace(string(data))
}
