package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/identitymatcher"
	"github.com/vertex-pro/vertex-pro/internal/qbadd"
	"github.com/vertex-pro/vertex-pro/internal/qbprofile"
	"github.com/vertex-pro/vertex-pro/internal/qui/proxy"
	"github.com/vertex-pro/vertex-pro/internal/syncmerge"
	"github.com/vertex-pro/vertex-pro/internal/torrentidentity"
)

type protocolCase struct {
	Name   string         `json:"name"`
	Status string         `json:"status"`
	Facts  map[string]any `json:"facts"`
}

// runProtocolSuite requires the isolated-lab opt-in and empty baseline checked
// by run. All writes target one trackerless identity created by this invocation.
func runProtocolSuite(ctx context.Context, client *proxy.Client, profile qbprofile.Profile, identity torrentidentity.Identity, artifact []byte, name string, baseline syncmerge.Snapshot) error {
	cases := []protocolCase{}
	pass := func(name string, facts map[string]any) { cases = append(cases, protocolCase{name, "PASS", facts}) }
	pass("profile_and_empty_full_sync", map[string]any{"rid": baseline.RID, "task_count": len(baseline.Torrents)})
	preferences, err := client.Preferences(ctx)
	if err != nil {
		return err
	}
	path, ok := preferences["save_path"].(string)
	if !ok || path == "" {
		return errors.New("protocol: default save path missing")
	}
	if _, ok := preferences["temp_path_enabled"].(bool); !ok {
		return errors.New("protocol: temporary path setting missing")
	}
	pass("preferences", map[string]any{"default_path_present": true, "temporary_path_setting_present": true})
	add := func(content []byte) (int, string, error) {
		req, err := qbadd.BuildRequest(profile, qbadd.TorrentArtifact{Filename: name + ".torrent", Content: content}, qbadd.AddParams{StartInactive: true})
		if err != nil {
			return 0, "", err
		}
		raw, err := client.Send(ctx, req)
		return raw.StatusCode, strings.TrimSpace(string(raw.Body)), err
	}
	status, _, err := add(artifact)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("protocol: Add HTTP %d", status)
	}
	current := baseline
	var matched string
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); {
		delta, err := client.Fetch(ctx, current.RID)
		if err != nil {
			return err
		}
		merged, err := syncmerge.Merge(current, delta)
		if err != nil {
			return err
		}
		current = merged.Snapshot
		match, err := identitymatcher.Match(identitymatcher.Request{Identity: identity}, snapshotCandidates(current))
		if err != nil {
			return err
		}
		if match.Status == identitymatcher.StatusMatched && inactiveState(stateOf(current, match.CandidateID)) {
			matched = match.CandidateID
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if matched == "" || len(current.Torrents) != 1 {
		return errors.New("protocol: Add did not yield exactly one inactive identity")
	}
	pass("inactive_add_and_identity_confirmation", map[string]any{"http_status": status, "state": stateOf(current, matched), "task_count": 1})
	status, body, err := add(artifact)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("protocol: duplicate Add HTTP %d", status)
	}
	duplicateDelta, err := client.Fetch(ctx, 0)
	if err != nil {
		return err
	}
	duplicate, err := syncmerge.Merge(syncmerge.Snapshot{}, duplicateDelta)
	if err != nil {
		return err
	}
	if _, ok := duplicate.Snapshot.Torrents[matched]; !ok || len(duplicate.Snapshot.Torrents) != 1 {
		return errors.New("protocol: duplicate changed task set")
	}
	pass("duplicate_add_preserves_identity_set", map[string]any{"http_status": status, "legacy_failure": body == "Fails.", "task_count": 1})
	status, body, err = add([]byte("not a torrent"))
	if err != nil {
		return err
	}
	if status < 400 && body != "Fails." {
		return fmt.Errorf("protocol: malformed torrent was not rejected, HTTP %d", status)
	}
	invalidDelta, err := client.Fetch(ctx, 0)
	if err != nil {
		return err
	}
	invalid, err := syncmerge.Merge(syncmerge.Snapshot{}, invalidDelta)
	if err != nil {
		return err
	}
	if _, ok := invalid.Snapshot.Torrents[matched]; !ok || len(invalid.Snapshot.Torrents) != 1 {
		return errors.New("protocol: invalid torrent changed task set")
	}
	pass("invalid_add_rejected_without_side_effect", map[string]any{"http_status": status, "task_count": 1})
	rows, err := client.Trackers(ctx, matched)
	if err != nil {
		return err
	}
	pass("tracker_read", map[string]any{"row_count": len(rows)})
	task := invalid.Snapshot.Torrents[matched]
	if task.AmountLeftState != "KNOWN" || task.AmountLeft == nil || *task.AmountLeft != 1 {
		return errors.New("protocol: amount_left is not the one-byte fixture liability")
	}
	for _, field := range []string{"dlspeed", "upspeed", "completed", "progress"} {
		if _, ok := task.Value(field); !ok {
			return fmt.Errorf("protocol: transfer field missing: %s", field)
		}
	}
	pass("sync_transfer_and_capacity_fields", map[string]any{"amount_left": *task.AmountLeft, "source": "QBITTORRENT_SYNC_MAINDATA"})
	// Keep one RID chain after the full reads above, then observe its actual
	// torrents_removed delta; a full reset does not count as incremental removal.
	current = invalid.Snapshot
	status, err = deleteIdentity(ctx, os.Getenv("VERTEX_PRO_QUI_URL"), os.Getenv("VERTEX_PRO_QUI_PROXY_KEY"), matched)
	if err != nil {
		return err
	}
	if status != http.StatusOK && status != http.StatusNoContent {
		return fmt.Errorf("protocol: delete HTTP %d", status)
	}
	removed := false
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); {
		delta, err := client.Fetch(ctx, current.RID)
		if err != nil {
			return err
		}
		for _, id := range delta.Removed {
			if id == matched && !delta.FullUpdate {
				removed = true
			}
		}
		merged, err := syncmerge.Merge(current, delta)
		if err != nil {
			return err
		}
		current = merged.Snapshot
		if removed && len(current.Torrents) == 0 {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if !removed || len(current.Torrents) != 0 {
		return errors.New("protocol: incremental removal and empty cleanup not observed")
	}
	pass("delete_incremental_removed_and_empty_cleanup", map[string]any{"http_status": status, "incremental_removed": true, "task_count": 0})
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"status": "PASS", "profile": profile.ID, "detected_profile": profile, "proxy_route_fingerprint": proxy.RouteFingerprint(), "cases": cases, "safety": map[string]bool{"explicit_isolated_lab_opt_in": true, "baseline_empty": true, "start_inactive": true, "trackerless": true, "delete_only_added_identity": true, "lab_returned_empty": true, "secrets_redacted": true, "production_services_called": false}})
}
