package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/qbprofile"
	"github.com/vertex-pro/vertex-pro/internal/qui/control"
	"github.com/vertex-pro/vertex-pro/internal/qui/proxy"
	"github.com/vertex-pro/vertex-pro/internal/secrets"
	"github.com/vertex-pro/vertex-pro/internal/syncmerge"
)

type report struct {
	Status      string             `json:"status"`
	Safety      safetyReport       `json:"safety"`
	Instance    instanceReport     `json:"instance"`
	Profile     qbprofile.Profile  `json:"profile"`
	Sync        syncReport         `json:"sync"`
	Automations *automationsReport `json:"automations,omitempty"`
}

type safetyReport struct {
	ReadOnly             bool `json:"read_only"`
	WriteEndpointsCalled bool `json:"write_endpoints_called"`
	SecretsRedacted      bool `json:"secrets_redacted"`
	// A qui dry-run changes no torrent but appends a "dry-run" entry to
	// qui's activity log; it only runs when explicitly requested.
	ActivityLogWritten bool `json:"activity_log_written"`
}

// automationsReport checks the D1 deletion contract against a live qui with a
// delete rule that cannot match any torrent.
type automationsReport struct {
	ActivityEntries   int    `json:"activity_entries"`
	ActivityDeletions int    `json:"activity_deletions"`
	ActivityDryRuns   int    `json:"activity_dry_runs"`
	PreviewMatches    int    `json:"preview_matches"`
	DryRunStatus      string `json:"dry_run_status,omitempty"`
	DryRunActivities  int    `json:"dry_run_activities,omitempty"`
}

type instanceReport struct {
	ID                string `json:"id"`
	Status            string `json:"status"`
	Enabled           bool   `json:"enabled"`
	TransferInfoValid bool   `json:"transfer_info_valid"`
	CapabilityCount   int    `json:"capability_count"`
}

type syncReport struct {
	InitialRID       uint64 `json:"initial_rid"`
	NextRID          uint64 `json:"next_rid"`
	FullUpdate       bool   `json:"full_update"`
	TorrentCount     int    `json:"torrent_count"`
	RemovedCount     int    `json:"removed_count"`
	TaskSetUnchanged bool   `json:"task_set_unchanged"`
}

func main() {
	if err := run(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "qui read-only probe failed:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	baseURL := strings.TrimSpace(os.Getenv("VERTEX_PRO_QUI_URL"))
	instanceID := strings.TrimSpace(os.Getenv("VERTEX_PRO_QUI_INSTANCE_ID"))
	if baseURL == "" || instanceID == "" {
		return errors.New("VERTEX_PRO_QUI_URL and VERTEX_PRO_QUI_INSTANCE_ID are required")
	}
	store := secrets.EnvStore{}
	httpClient := &http.Client{Timeout: 30 * time.Second}
	controlClient, err := control.New(ctx, control.Config{
		BaseURL:    baseURL,
		APIKey:     secrets.SecretRef{Provider: "env", Key: "VERTEX_PRO_QUI_API_KEY"},
		Store:      store,
		HTTPClient: httpClient,
	})
	if err != nil {
		return err
	}
	proxyClient, err := proxy.New(ctx, proxy.Config{
		BaseURL:    baseURL,
		ProxyKey:   secrets.SecretRef{Provider: "env", Key: "VERTEX_PRO_QUI_PROXY_KEY"},
		Store:      store,
		HTTPClient: httpClient,
	})
	if err != nil {
		return err
	}
	instances, err := controlClient.Instances(ctx)
	if err != nil {
		return fmt.Errorf("list qui instances: %w", err)
	}
	var selected *control.Instance
	for index := range instances {
		if instances[index].ID == instanceID {
			selected = &instances[index]
			break
		}
	}
	if selected == nil {
		return fmt.Errorf("qui instance %s was not found", instanceID)
	}
	transfer, err := controlClient.TransferInfo(ctx, instanceID)
	if err != nil {
		return fmt.Errorf("read transfer info: %w", err)
	}
	capabilities, err := controlClient.Capabilities(ctx, instanceID)
	if err != nil {
		return fmt.Errorf("read capabilities: %w", err)
	}
	profile, err := qbprofile.Probe(ctx, proxyClient)
	if err != nil {
		return err
	}
	delta, err := proxyClient.Fetch(ctx, 0)
	if err != nil {
		return fmt.Errorf("read initial sync snapshot: %w", err)
	}
	merged, err := syncmerge.Merge(syncmerge.Snapshot{}, delta)
	if err != nil {
		return fmt.Errorf("merge initial sync snapshot: %w", err)
	}
	nextDelta, err := proxyClient.Fetch(ctx, delta.RID)
	if err != nil {
		return fmt.Errorf("read incremental sync snapshot: %w", err)
	}
	next, err := syncmerge.Merge(merged.Snapshot, nextDelta)
	if err != nil {
		return fmt.Errorf("merge incremental sync snapshot: %w", err)
	}

	result := report{
		Status: "PASS",
		Safety: safetyReport{ReadOnly: true, WriteEndpointsCalled: false, SecretsRedacted: true},
		Instance: instanceReport{
			ID:                selected.ID,
			Status:            selected.Status,
			Enabled:           selected.Enabled,
			TransferInfoValid: transfer.DownloadSpeed >= 0 && transfer.UploadSpeed >= 0 && transfer.Downloaded >= 0 && transfer.Uploaded >= 0,
			CapabilityCount:   len(capabilities),
		},
		Profile: profile,
		Sync: syncReport{
			InitialRID:       delta.RID,
			NextRID:          nextDelta.RID,
			FullUpdate:       delta.FullUpdate,
			TorrentCount:     len(merged.Snapshot.Torrents),
			RemovedCount:     len(nextDelta.Removed),
			TaskSetUnchanged: sameTorrentSet(merged.Snapshot, next.Snapshot),
		},
	}
	if os.Getenv("VERTEX_PRO_QUI_PROBE_AUTOMATIONS") == "1" {
		dryRun := os.Getenv("VERTEX_PRO_QUI_PROBE_DRYRUN") == "1"
		automations, err := probeAutomations(ctx, controlClient, instanceID, dryRun)
		if err != nil {
			return err
		}
		result.Automations = automations
		result.Safety.ActivityLogWritten = dryRun
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(result)
}

// probeAutomations reads qui's activity log and previews (optionally
// dry-runs) a delete rule scoped to a random tag no torrent carries. It never
// saves an automation and never calls qui's forced-execution endpoint.
func probeAutomations(ctx context.Context, client *control.Client, instanceID string, dryRun bool) (*automationsReport, error) {
	activities, err := client.Activity(ctx, instanceID, 100)
	if err != nil {
		return nil, fmt.Errorf("read qui automation activity: %w", err)
	}
	report := &automationsReport{ActivityEntries: len(activities)}
	for _, activity := range activities {
		if activity.IsDeletion() {
			report.ActivityDeletions++
		}
		if activity.IsDryRun() {
			report.ActivityDryRuns++
		}
	}
	nonce := make([]byte, 8)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	tag := "vertex-probe-" + hex.EncodeToString(nonce)
	rule := control.Automation{Name: tag, TrackerPattern: "*", Enabled: true, IntervalSeconds: 60, Conditions: map[string]any{
		"schemaVersion": "1",
		"delete": map[string]any{"enabled": true, "mode": "delete", "condition": map[string]any{
			"operator": "AND", "conditions": []any{map[string]any{"field": "TAGS", "operator": "EQUAL", "value": tag}},
		}},
	}}
	preview, err := client.PreviewDelete(ctx, instanceID, rule, 10, 0, control.PreviewViewEligible)
	if err != nil {
		return nil, fmt.Errorf("preview probe delete rule: %w", err)
	}
	if preview.TotalMatches != 0 {
		return nil, fmt.Errorf("probe delete rule unexpectedly matched %d torrents", preview.TotalMatches)
	}
	report.PreviewMatches = preview.TotalMatches
	if dryRun {
		result, err := client.DryRun(ctx, instanceID, rule)
		if err != nil {
			return nil, fmt.Errorf("dry-run probe delete rule: %w", err)
		}
		report.DryRunStatus, report.DryRunActivities = result.Status, len(result.Activities)
		for _, activity := range result.Activities {
			if activity.IsDeletion() {
				return nil, errors.New("probe dry-run reported a deletion for a rule that matches nothing")
			}
		}
	}
	return report, nil
}

func sameTorrentSet(left, right syncmerge.Snapshot) bool {
	leftIDs := make([]string, 0, len(left.Torrents))
	rightIDs := make([]string, 0, len(right.Torrents))
	for id := range left.Torrents {
		leftIDs = append(leftIDs, id)
	}
	for id := range right.Torrents {
		rightIDs = append(rightIDs, id)
	}
	sort.Strings(leftIDs)
	sort.Strings(rightIDs)
	if len(leftIDs) != len(rightIDs) {
		return false
	}
	for index := range leftIDs {
		if leftIDs[index] != rightIDs[index] {
			return false
		}
	}
	return true
}
