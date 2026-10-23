package control

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// These fields follow qui bfe94b5e4d63f213af747fab39719d47aae99d22.
// Vertex only creates its own narrowly tagged rules; existing rules are untouched.
type Automation struct {
	ID              int            `json:"id,omitempty"`
	Name            string         `json:"name"`
	TrackerPattern  string         `json:"trackerPattern"`
	TrackerDomains  []string       `json:"trackerDomains,omitempty"`
	Enabled         bool           `json:"enabled"`
	DryRun          bool           `json:"dryRun"`
	Notify          bool           `json:"notify"`
	SortOrder       int            `json:"sortOrder"`
	IntervalSeconds int            `json:"intervalSeconds"`
	Conditions      map[string]any `json:"conditions"`
	FreeSpaceSource map[string]any `json:"freeSpaceSource,omitempty"`
	SortingConfig   map[string]any `json:"sortingConfig,omitempty"`
}

func (c *Client) Automations(ctx context.Context, instance string) ([]Automation, error) {
	var result []Automation
	err := c.getJSON(ctx, "/api/instances/"+url.PathEscape(instance)+"/automations", &result)
	return result, err
}
func (c *Client) SaveAutomation(ctx context.Context, instance string, a Automation) (Automation, error) {
	if instance == "" || a.Name == "" || a.Conditions == nil {
		return Automation{}, fmt.Errorf("invalid automation")
	}
	method := http.MethodPost
	endpoint := "/api/instances/" + url.PathEscape(instance) + "/automations"
	if a.ID > 0 {
		method = http.MethodPut
		endpoint += "/" + strconv.Itoa(a.ID)
	}
	body, err := json.Marshal(a)
	if err != nil {
		return Automation{}, err
	}
	target := *c.baseURL
	target.Path = strings.TrimRight(target.Path, "/") + endpoint
	req, err := http.NewRequestWithContext(ctx, method, target.String(), bytes.NewReader(body))
	if err != nil {
		return Automation{}, fmt.Errorf("build automation request failed")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", c.apiKey)
	response, err := c.httpClient.Do(req)
	if err != nil {
		return Automation{}, fmt.Errorf("qui automation transport failed")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return Automation{}, &HTTPError{StatusCode: response.StatusCode, Status: response.Status}
	}
	var saved Automation
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&saved); err != nil {
		return saved, fmt.Errorf("invalid automation response")
	}
	if saved.ID <= 0 || saved.Name != a.Name {
		return saved, fmt.Errorf("automation response identity mismatch")
	}
	return saved, nil
}
