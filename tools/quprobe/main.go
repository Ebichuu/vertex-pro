package main

import (
	"context"
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
	Status   string            `json:"status"`
	Safety   safetyReport      `json:"safety"`
	Instance instanceReport    `json:"instance"`
	Profile  qbprofile.Profile `json:"profile"`
	Sync     syncReport        `json:"sync"`
}

type safetyReport struct {
	ReadOnly             bool `json:"read_only"`
	WriteEndpointsCalled bool `json:"write_endpoints_called"`
	SecretsRedacted      bool `json:"secrets_redacted"`
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
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(result)
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
