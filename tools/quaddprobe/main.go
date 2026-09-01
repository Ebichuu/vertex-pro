package main

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/dispatchworker"
	"github.com/vertex-pro/vertex-pro/internal/identitymatcher"
	"github.com/vertex-pro/vertex-pro/internal/qbadd"
	"github.com/vertex-pro/vertex-pro/internal/qbprofile"
	"github.com/vertex-pro/vertex-pro/internal/qui/proxy"
	"github.com/vertex-pro/vertex-pro/internal/secrets"
	"github.com/vertex-pro/vertex-pro/internal/syncmerge"
	"github.com/vertex-pro/vertex-pro/internal/torrentidentity"
	"github.com/vertex-pro/vertex-pro/internal/torrentparse"
)

const liveAddConfirmation = "I_UNDERSTAND_ADD_ONE_INACTIVE_TASK"

type report struct {
	Status string `json:"status"`
	Safety struct {
		ExplicitOptIn          bool `json:"explicit_opt_in"`
		StartInactive          bool `json:"start_inactive"`
		Trackerless            bool `json:"trackerless"`
		ExistingTasksPreserved bool `json:"existing_tasks_preserved"`
		DeleteEndpointCalled   bool `json:"delete_endpoint_called"`
		TestTaskRetained       bool `json:"test_task_retained"`
	} `json:"safety"`
	Add struct {
		HTTPStatus     int    `json:"http_status"`
		ResponseFormat string `json:"response_format"`
		SuccessCount   int    `json:"success_count"`
		SideEffectFact string `json:"side_effect_fact"`
	} `json:"add"`
	Confirmation struct {
		MatcherStatus string `json:"matcher_status"`
		Observed      bool   `json:"observed"`
		Inactive      bool   `json:"inactive"`
		State         string `json:"state"`
		TaskDelta     int    `json:"task_delta"`
	} `json:"confirmation"`
}

func main() {
	if err := run(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "qui inactive Add probe failed:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	if os.Getenv("VERTEX_PRO_ALLOW_LIVE_ADD") != liveAddConfirmation {
		return errors.New("explicit live Add confirmation is missing")
	}
	baseURL := strings.TrimSpace(os.Getenv("VERTEX_PRO_QUI_URL"))
	if baseURL == "" {
		return errors.New("VERTEX_PRO_QUI_URL is required")
	}
	client, err := proxy.New(ctx, proxy.Config{
		BaseURL:    baseURL,
		ProxyKey:   secrets.SecretRef{Provider: "env", Key: "VERTEX_PRO_QUI_PROXY_KEY"},
		Store:      secrets.EnvStore{},
		HTTPClient: &http.Client{Timeout: 60 * time.Second},
	})
	if err != nil {
		return err
	}
	profile, err := qbprofile.Probe(ctx, client)
	if err != nil {
		return err
	}
	if profile.ID != "QB_504" || profile.AddStartField != "stopped" {
		return fmt.Errorf("inactive Add probe requires QB_504 stopped semantics, got %s/%s", profile.ID, profile.AddStartField)
	}
	baselineDelta, err := client.Fetch(ctx, 0)
	if err != nil {
		return fmt.Errorf("read pre-Add snapshot: %w", err)
	}
	baseline, err := syncmerge.Merge(syncmerge.Snapshot{}, baselineDelta)
	if err != nil {
		return err
	}

	name := "vertex-pro-live-probe-qb504-v1"
	artifact := buildTrackerlessTorrent(name)
	parsed, err := torrentparse.Parse(artifact, torrentparse.DefaultLimits())
	if err != nil {
		return err
	}
	identity, err := torrentidentity.FromTorrent(parsed)
	if err != nil {
		return err
	}
	if existing, exists := baseline.Snapshot.Torrents[strings.ToLower(identity.V1)]; exists {
		state, _ := existing.Fields["state"].(string)
		inactive := inactiveState(state)
		output := report{Status: "PASS"}
		output.Safety.ExplicitOptIn = true
		output.Safety.StartInactive = true
		output.Safety.Trackerless = true
		output.Safety.ExistingTasksPreserved = true
		output.Safety.DeleteEndpointCalled = false
		output.Safety.TestTaskRetained = true
		output.Add.ResponseFormat = "not_dispatched_already_present"
		output.Add.SideEffectFact = "OBSERVED_EFFECT"
		output.Confirmation.MatcherStatus = identitymatcher.StatusMatched
		output.Confirmation.Observed = true
		output.Confirmation.Inactive = inactive
		output.Confirmation.State = state
		output.Confirmation.TaskDelta = 0
		if !inactive {
			output.Status = "FAIL"
		}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(output); err != nil {
			return err
		}
		if output.Status != "PASS" {
			return errors.New("existing test task is not inactive")
		}
		return nil
	}
	request, err := qbadd.BuildRequest(profile, qbadd.TorrentArtifact{Filename: name + ".torrent", Content: artifact}, qbadd.AddParams{StartInactive: true})
	if err != nil {
		return err
	}
	worker, err := dispatchworker.New(client)
	if err != nil {
		return err
	}
	result, err := worker.Dispatch(ctx, dispatchworker.Request{
		AttemptID:             "live-add-probe-attempt",
		SubmitIntentID:        "live-add-probe-intent",
		WaiterID:              "live-add-probe-waiter",
		APIVersion:            profile.WebAPIVersion,
		HTTPRequest:           request,
		AttemptState:          "RESERVED",
		AttemptDispatchState:  "NOT_STARTED",
		AttemptSideEffectFact: "",
	})
	if err != nil {
		return err
	}

	current := baseline.Snapshot
	match := identitymatcher.Result{Status: identitymatcher.StatusNotFound}
	state := ""
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		delta, fetchErr := client.Fetch(ctx, current.RID)
		if fetchErr != nil {
			return fetchErr
		}
		merged, mergeErr := syncmerge.Merge(current, delta)
		if mergeErr != nil {
			return mergeErr
		}
		current = merged.Snapshot
		match, err = identitymatcher.Match(identitymatcher.Request{Identity: identity}, snapshotCandidates(current))
		if err != nil {
			return err
		}
		if match.Status == identitymatcher.StatusMatched {
			state, _ = current.Torrents[match.CandidateID].Fields["state"].(string)
			inactive := inactiveState(state)
			if inactive {
				break
			}
		}
		time.Sleep(time.Second)
	}
	if match.Status != identitymatcher.StatusMatched {
		return errors.New("test task was not observed in Sync before the deadline")
	}
	inactive := inactiveState(state)
	preserved := containsAll(current.Torrents, baseline.Snapshot.Torrents)

	output := report{Status: "PASS"}
	output.Safety.ExplicitOptIn = true
	output.Safety.StartInactive = true
	output.Safety.Trackerless = true
	output.Safety.ExistingTasksPreserved = preserved
	output.Safety.DeleteEndpointCalled = false
	output.Safety.TestTaskRetained = true
	output.Add.HTTPStatus = result.Response.HTTPStatus
	output.Add.ResponseFormat = result.Response.ResponseFormat
	output.Add.SuccessCount = result.Response.SuccessCount
	output.Add.SideEffectFact = result.Response.SideEffectFact
	output.Confirmation.MatcherStatus = match.Status
	output.Confirmation.Observed = true
	output.Confirmation.Inactive = inactive
	output.Confirmation.State = state
	output.Confirmation.TaskDelta = len(current.Torrents) - len(baseline.Snapshot.Torrents)
	if !preserved || !inactive || output.Confirmation.TaskDelta != 1 {
		output.Status = "FAIL"
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(output); err != nil {
		return err
	}
	if output.Status != "PASS" {
		return errors.New("inactive Add safety assertions failed")
	}
	return nil
}

func buildTrackerlessTorrent(name string) []byte {
	piece := sha1.Sum([]byte{0})
	var buffer bytes.Buffer
	buffer.WriteString("d4:infod6:lengthi1e4:name")
	writeBytes(&buffer, []byte(name))
	buffer.WriteString("12:piece lengthi16384e6:pieces20:")
	buffer.Write(piece[:])
	buffer.WriteString("ee")
	return buffer.Bytes()
}

func writeBytes(buffer *bytes.Buffer, value []byte) {
	fmt.Fprintf(buffer, "%d:", len(value))
	buffer.Write(value)
}

func snapshotCandidates(snapshot syncmerge.Snapshot) []identitymatcher.Candidate {
	ids := make([]string, 0, len(snapshot.Torrents))
	for id := range snapshot.Torrents {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	candidates := make([]identitymatcher.Candidate, 0, len(ids))
	for _, id := range ids {
		identity := torrentidentity.Identity{}
		switch len(id) {
		case 40:
			identity.Version, identity.V1 = torrentidentity.VersionV1, id
		case 64:
			identity.Version, identity.V2 = torrentidentity.VersionV2, id
		default:
			continue
		}
		candidates = append(candidates, identitymatcher.Candidate{ID: id, Identity: identity, QBTorrentID: id, LegacyMapKey: id})
	}
	return candidates
}

func inactiveState(state string) bool {
	normalized := strings.ToLower(strings.TrimSpace(state))
	return strings.HasPrefix(normalized, "stopped") || strings.HasPrefix(normalized, "paused")
}

func containsAll(current, baseline map[string]syncmerge.Torrent) bool {
	for id := range baseline {
		if _, exists := current[id]; !exists {
			return false
		}
	}
	return true
}
